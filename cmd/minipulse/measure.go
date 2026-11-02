package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// A test measures latency, then download, then upload, the way the
// browser speed tests do: several connections at once, for a fixed time,
// ignoring the first moments while TCP ramps up.

// sampleEvery is how often the live speed is measured (and drawn).
const sampleEvery = 500 * time.Millisecond

// Phase is the part of the test running.
type Phase int

// The phases, in the order a test runs them.
const (
	PhaseIdle Phase = iota
	PhasePing
	PhaseDown
	PhaseUp
	PhaseDone
)

// String names the phase for the -cli progress lines.
func (p Phase) String() string {
	return [...]string{"Ready", "Latency", "Download", "Upload", "Done"}[p]
}

// Plan sets how long each part runs and how hard it pushes.
type Plan struct {
	Pings     int           // latency samples
	Duration  time.Duration // per direction
	Warmup    time.Duration // ignored at the start of each direction
	Streams   int           // parallel connections
	DownChunk int64         // bytes per download request
	UpChunk   int64         // bytes per upload request
}

// The Mini Plus's Wi-Fi tops out around 40 Mbit/s and its CPU has to do
// all the TLS work, so three connections fill it; more only add overhead.
var defaultPlan = Plan{
	Pings:     12,
	Duration:  10 * time.Second,
	Warmup:    2 * time.Second,
	Streams:   3,
	DownChunk: 25 << 20,
	UpChunk:   2 << 20,
}

// Live is what the screen shows while the test runs. Read it with Test.Live.
type Live struct {
	Phase    Phase
	Progress float64   // of the current phase, 0..1
	Mbps     float64   // current speed (last half second)
	Down, Up []float64 // speed every half second, Mbit/s
	Result   Result
	Err      error
}

// Result is a finished (or partly finished) test.
type Result struct {
	When     time.Time `json:"when"`
	Server   string    `json:"server"`
	Where    string    `json:"where,omitempty"` // the server's location, if known
	PingMs   float64   `json:"ping_ms"`
	JitterMs float64   `json:"jitter_ms"`
	Down     float64   `json:"down_mbps"`
	Up       float64   `json:"up_mbps"`
	Signal   int       `json:"signal_dbm,omitempty"`
	Loss     float64   `json:"loss,omitempty"` // share of latency probes that failed
	// the speed every half second, for the graphs
	DownS []float64 `json:"down_samples,omitempty"`
	UpS   []float64 `json:"up_samples,omitempty"`
}

// Test runs one speed test in the background.
type Test struct {
	srv    Server
	plan   Plan
	client *http.Client
	poke   func()

	mu   sync.Mutex
	live Live
}

// NewTest prepares a test against srv; poke is called whenever its Live
// state changes. Start it with Run.
func NewTest(srv Server, plan Plan, client *http.Client, poke func()) *Test {
	return &Test{srv: srv, plan: plan, client: client, poke: poke}
}

// Live returns a copy of the test's current state.
func (t *Test) Live() Live {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.live
	l.Down, l.Up = slices.Clone(l.Down), slices.Clone(l.Up)
	return l
}

func (t *Test) set(f func(*Live)) {
	t.mu.Lock()
	f(&t.live)
	t.mu.Unlock()
	if t.poke != nil {
		t.poke()
	}
}

// Run does the whole test; cancel ctx to stop it.
func (t *Test) Run(ctx context.Context) Result {
	res := Result{When: time.Now(), Server: t.srv.Name()}
	if w, err := t.srv.Locate(ctx, t.client); err == nil {
		res.Where = w
	}
	t.set(func(l *Live) { l.Phase, l.Result = PhasePing, res })
	ping, jitter, loss, err := t.latency(ctx)
	if err != nil {
		return t.fail(res, err)
	}
	res.PingMs, res.JitterMs, res.Loss = ping, jitter, loss
	t.set(func(l *Live) { l.Result = res; l.Phase, l.Progress, l.Mbps = PhaseDown, 0, 0 })

	res.Down, err = t.transfer(ctx, false)
	if err != nil {
		return t.fail(res, err)
	}
	t.set(func(l *Live) { l.Result = res; l.Phase, l.Progress, l.Mbps = PhaseUp, 0, 0 })

	res.Up, err = t.transfer(ctx, true)
	if err != nil {
		return t.fail(res, err)
	}
	t.set(func(l *Live) { l.Result = res; l.Phase, l.Progress = PhaseDone, 1 })
	return res
}

func (t *Test) fail(res Result, err error) Result {
	if errors.Is(err, context.Canceled) {
		err = errStopped
	}
	t.set(func(l *Live) { l.Result, l.Err, l.Phase = res, err, PhaseDone })
	return res
}

var errStopped = errors.New("stopped")

// latency times small requests on one warm connection: the time to the
// first byte of an empty response, minus what the server says it spent.
// The first request (DNS, TCP and TLS set-up) isn't counted.
func (t *Test) latency(ctx context.Context) (ping, jitter, loss float64, err error) {
	var samples []float64
	failed := 0
	for i := 0; i <= t.plan.Pings; i++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, err
		}
		d, err := t.probe(ctx)
		if i == 0 {
			if err != nil {
				return 0, 0, 0, fmt.Errorf("can't reach %s: %w", t.srv.Name(), err)
			}
			continue
		}
		if err != nil {
			failed++
		} else {
			samples = append(samples, float64(d)/float64(time.Millisecond))
		}
		t.set(func(l *Live) { l.Progress = float64(i) / float64(t.plan.Pings) })
	}
	if len(samples) == 0 {
		return 0, 0, 1, errors.New("no answer to any latency probe")
	}
	ping, jitter = pingStats(samples)
	return ping, jitter, float64(failed) / float64(t.plan.Pings), nil
}

func (t *Test) probe(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", t.srv.PingURL(), nil)
	if err != nil {
		return 0, err
	}
	var sent, first time.Time
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { sent = time.Now() },
		GotFirstResponseByte: func() { first = time.Now() },
	}))
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("server said %s", resp.Status)
	}
	if sent.IsZero() || first.IsZero() {
		return 0, errors.New("no timing")
	}
	d := first.Sub(sent) - serverTime(resp.Header)
	return max(d, 0), nil
}

// pingStats gives the median and the jitter (the mean difference between
// one sample and the next, as RFC 3550 and most speed tests have it).
func pingStats(ms []float64) (median, jitter float64) {
	s := slices.Clone(ms)
	slices.Sort(s)
	n := len(s)
	median = s[n/2]
	if n%2 == 0 {
		median = (s[n/2-1] + s[n/2]) / 2
	}
	for i := 1; i < len(ms); i++ {
		d := ms[i] - ms[i-1]
		if d < 0 {
			d = -d
		}
		jitter += d
	}
	if len(ms) > 1 {
		jitter /= float64(len(ms) - 1)
	}
	return median, jitter
}

// transfer keeps Streams connections busy for Duration and returns the
// speed in Mbit/s over the time after Warmup.
func (t *Test) transfer(ctx context.Context, up bool) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, t.plan.Duration)
	defer cancel()
	var total atomic.Int64
	var (
		errOnce  sync.Once
		firstErr error // read after wg.Wait
	)
	var wg sync.WaitGroup
	for range t.plan.Streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				var err error
				if up {
					err = t.upload(ctx, &total)
				} else {
					err = t.download(ctx, &total)
				}
				if err != nil && ctx.Err() == nil {
					errOnce.Do(func() { firstErr = err })
					time.Sleep(200 * time.Millisecond) // don't spin on a refusing server
				}
			}
		}()
	}

	start := time.Now()
	var warmBytes int64
	var warmAt time.Time
	lastBytes, lastAt := int64(0), start
	tick := time.NewTicker(sampleEvery)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case now := <-tick.C:
			b := total.Load()
			mbps := mbit(b-lastBytes, now.Sub(lastAt))
			lastBytes, lastAt = b, now
			if warmAt.IsZero() && now.Sub(start) >= t.plan.Warmup {
				warmBytes, warmAt = b, now
			}
			t.set(func(l *Live) {
				l.Mbps = mbps
				if up {
					l.Up = append(l.Up, mbps)
				} else {
					l.Down = append(l.Down, mbps)
				}
				l.Progress = min(1, float64(now.Sub(start))/float64(t.plan.Duration))
			})
		}
	}
	end := time.Now()
	wg.Wait()
	if err := context.Cause(ctx); errors.Is(err, context.Canceled) {
		return 0, err
	}
	b := total.Load()
	if b == 0 {
		if firstErr != nil {
			return 0, firstErr
		}
		return 0, errors.New("nothing was transferred")
	}
	if warmAt.IsZero() { // too short a test to skip the ramp-up
		return mbit(b, end.Sub(start)), nil
	}
	return mbit(b-warmBytes, end.Sub(warmAt)), nil
}

func mbit(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

func (t *Test) download(ctx context.Context, total *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", t.srv.DownURL(t.plan.DownChunk), nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download: server said %s", resp.Status)
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		total.Add(int64(n))
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// One random megabyte, sent over and over: random so that no compression
// on the way flatters the result, shared so uploads allocate nothing.
var noise = func() []byte {
	b := make([]byte, 1<<20)
	rand.Read(b)
	return b
}()

// noiseReader yields n bytes of noise, counting what was read (the HTTP
// client reads in small pieces just before writing them out).
type noiseReader struct {
	left  int64
	off   int
	total *atomic.Int64
}

func (r *noiseReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n := copy(p, noise[r.off:])
	r.off = (r.off + n) % len(noise)
	r.left -= int64(n)
	r.total.Add(int64(n))
	return n, nil
}

func (t *Test) upload(ctx context.Context, total *atomic.Int64) error {
	body := &noiseReader{left: t.plan.UpChunk, total: total}
	req, err := http.NewRequestWithContext(ctx, "POST", t.srv.UpURL(), body)
	if err != nil {
		return err
	}
	req.ContentLength = t.plan.UpChunk
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("upload: server said %s", resp.Status)
	}
	return nil
}
