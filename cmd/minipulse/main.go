// MiniPulse measures the Miyoo's internet connection over Wi-Fi: latency
// and jitter, then download and upload speed, against Cloudflare's speed
// test (no sign-up) or a LibreSpeed or OpenSpeedTest server you run
// yourself, on your network or your tailnet. Results are kept in a history.
//
//	minipulse            the app
//	minipulse -cli       run one test and print the result (no screen)
//	minipulse -version   print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // the card has no CA bundle

	"github.com/zakijss/minideck/appdir"
	"github.com/zakijss/minideck/ui"
	"github.com/zakijss/minideck/version"
)

func main() {
	cli := flag.Bool("cli", false, "run one test and print the result")
	server := flag.String("server", "", "with -cli: a LibreSpeed or OpenSpeedTest address instead of Cloudflare")
	streams := flag.Int("streams", defaultPlan.Streams, "with -cli: parallel connections")
	verbose := flag.Bool("v", false, "with -cli: print the speed every half second")
	version.Flag("minipulse")
	flag.Parse()
	if *cli {
		plan := defaultPlan
		plan.Streams = max(1, *streams)
		os.Exit(runCLI(*server, plan, *verbose))
	}

	version.Note("minipulse")
	scr, err := ui.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer scr.Close()
	st := store{dir: appdir.Dir()}
	a := &app{scr: scr, input: ui.Input(), st: st, set: st.settings(), hist: st.history(),
		client: newClient(), changed: make(chan struct{}, 1)}
	if len(a.hist) > 0 {
		a.shown, a.hasShown = a.hist[0], true
		a.last = Live{Phase: PhaseDone, Down: a.shown.DownS, Up: a.shown.UpS, Result: a.shown}
	}
	a.run()
}

// runCLI runs one test without the screen, printing progress to stderr and
// the result to stdout, and returns the exit status.
func runCLI(addr string, plan Plan, verbose bool) int {
	c := newClient()
	var srv Server = cloudflare{}
	if addr != "" {
		base, err := normalizeServer(addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if srv, err = detect(context.Background(), c, base); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	last, n := PhaseIdle, 0
	var t *Test
	t = NewTest(srv, plan, c, func() {
		l := t.Live()
		if l.Phase != last {
			last, n = l.Phase, 0
			fmt.Fprintf(os.Stderr, "%s...\n", l.Phase)
		}
		if s := append(l.Down, l.Up...); verbose && l.Phase != PhasePing && len(s) > n {
			n = len(s)
			fmt.Fprintf(os.Stderr, "  %5.1fs %7.2f Mbit/s\n", float64(len(l.Up)+len(l.Down))/2, s[len(s)-1])
		}
	})
	r := t.Run(context.Background())
	if err := t.Live().Err; err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("server   %s %s\nlatency  %.1f ms (jitter %.1f ms, %.0f%% lost)\ndownload %.2f Mbit/s\nupload   %.2f Mbit/s\n",
		r.Server, r.Where, r.PingMs, r.JitterMs, r.Loss*100, r.Down, r.Up)
	return 0
}

type app struct {
	scr     *ui.Screen
	input   <-chan ui.Event
	st      store
	set     Settings
	hist    []Result
	client  *http.Client
	changed chan struct{}

	test     *Test
	cancel   context.CancelFunc
	done     chan Result
	shown    Result // the last finished test, shown when idle
	last     Live   // its graphs
	hasShown bool
	note     string // replaces the subtitle until the next press
	failed   error
}

func (a *app) poke() {
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

func (a *app) running() bool { return a.test != nil }

func (a *app) start() {
	if a.running() {
		return
	}
	if !wifiUp() {
		a.note = "Wi-Fi is off: turn it on in Onion's settings"
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.failed = cancel, nil
	a.test = NewTest(a.set.server(), a.set.plan(), a.client, a.poke)
	a.done = make(chan Result, 1)
	t, done := a.test, a.done
	go func() { done <- t.Run(ctx) }()
}

func (a *app) stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// finished takes a test's result once its goroutine is done.
func (a *app) finished(r Result) {
	l := a.test.Live()
	a.test, a.cancel = nil, nil
	if l.Err != nil {
		if !errors.Is(l.Err, errStopped) {
			a.failed = humanize(l.Err)
		} else {
			a.note = "Test stopped"
		}
		return
	}
	if dbm, ok := wifiSignal(); ok {
		r.Signal = dbm
	}
	r.DownS, r.UpS = round1(l.Down), round1(l.Up)
	a.shown, a.hasShown = r, true
	a.last = l
	a.hist = addResult(a.hist, r)
	if err := a.st.save("history.json", a.hist); err != nil {
		a.note = "Couldn't save the result: " + err.Error()
	}
}

func (a *app) run() {
	for {
		a.draw()
		var done chan Result
		if a.running() {
			done = a.done
		}
		select {
		case ev, ok := <-a.input:
			if !ok {
				a.stop()
				return
			}
			if !ev.Pressed || ev.Repeat {
				continue
			}
			a.note = ""
			switch ev.Button {
			case ui.B, ui.Menu:
				if a.running() {
					a.stop()
					continue
				}
				return
			case ui.A, ui.Start:
				if a.running() {
					a.stop()
				} else {
					a.start()
				}
			case ui.Y:
				if !a.running() {
					a.settings()
				}
			case ui.X:
				if !a.running() {
					a.history()
				}
			case ui.Select:
				if !a.running() {
					a.scr.Appearance(a.input)
				}
			}
		case r := <-done:
			a.finished(r)
		case <-a.changed:
		}
	}
}

// ---- main screen ----

// Colours of the three measures, used for their tiles, ring and graph.
// Download is the theme's accent, so it is read when drawing (see colDown).
var (
	colPing = ui.Warn
	colUp   = ui.Good
)

func colDown() color.RGBA { return ui.Accent }

func (a *app) subtitle() string {
	if a.note != "" {
		return a.note
	}
	s := a.set.server().Name()
	if a.running() {
		if w := a.test.Live().Result.Where; w != "" && w != s {
			s += " " + w
		}
	} else if a.hasShown && a.shown.Server == s && a.shown.Where != "" && a.shown.Where != s {
		s += " " + a.shown.Where
	}
	if dbm, ok := wifiSignal(); ok {
		s += fmt.Sprintf(" · Wi-Fi %s (%d dBm)", signalWords(dbm), dbm)
	}
	return s
}

func (a *app) hints() []ui.Hint {
	if a.running() {
		return []ui.Hint{{Button: "A", Label: "Stop"}}
	}
	return []ui.Hint{{Button: "A", Label: "Start"}, {Button: "Y", Label: "Settings"},
		{Button: "X", Label: "History"}, ui.AppearanceHint, {Button: "B", Label: "Exit"}}
}

// liveOrLast is the running test's state, or else the last finished test's,
// so its graphs stay on screen.
func (a *app) liveOrLast() (Live, bool) {
	if a.running() {
		return a.test.Live(), true
	}
	return a.last, false
}

func (a *app) draw() {
	s := a.scr
	top := s.StaticFrame("MiniPulse", ui.Fit(ui.Small, a.subtitle(), ui.W-60), a.hints()...)
	l, live := a.liveOrLast()

	// the dial, left
	dial := image.Rect(20, top, 300, top+212)
	s.Panel(dial, 16)
	a.drawDial(dial, l, live)

	// the three measures, right
	r := a.shown
	if live {
		r = l.Result
	}
	have := a.hasShown || live
	tileH := 64
	x0, x1 := dial.Max.X+12, ui.W-20
	for i, t := range []struct {
		label string
		phase Phase
		val   string
		unit  string
		extra string
		c     color.RGBA
	}{
		{"PING", PhasePing, ms(r.PingMs), "ms", jitter(r), colPing},
		{"DOWNLOAD", PhaseDown, a.mbps(r.Down), a.set.unit(), "", colDown()},
		{"UPLOAD", PhaseUp, a.mbps(r.Up), a.set.unit(), "", colUp},
	} {
		y := top + i*(tileH+10)
		tr := image.Rect(x0, y, x1, y+tileH)
		active := live && l.Phase == t.phase
		if active {
			s.PanelTint(tr, 12, 0.15)
		} else {
			s.Panel(tr, 12)
		}
		s.Fill(image.Rect(tr.Min.X+14, tr.Min.Y+10, tr.Min.X+22, tr.Min.Y+18), t.c)
		s.Text(tr.Min.X+30, tr.Min.Y+8, t.label, ui.Caps, ui.Muted)
		val := "–"
		valCol := ui.Faint
		done := live && l.Phase > t.phase
		if (have && !live) || done {
			val, valCol = t.val, ui.Text
		}
		if active {
			val, valCol = "…", ui.Muted
			if t.phase != PhasePing {
				val, valCol = a.mbps(l.Mbps), ui.Text
			}
		}
		if val == "" {
			val, valCol = "–", ui.Faint
		}
		vx, vy := tr.Min.X+14, tr.Min.Y+30
		s.Text(vx, vy, val, ui.Big, valCol)
		// units and notes sit on the number's baseline
		by := vy + ui.LineHeight(ui.Big) - ui.LineHeight(ui.Small) - 4
		if val != "–" && val != "…" {
			s.Text(vx+ui.Measure(ui.Big, val)+8, by, t.unit, ui.Small, ui.Muted)
		}
		if t.extra != "" && ((have && !live) || done) {
			s.TextRight(tr.Max.X-14, tr.Min.Y+8, t.extra, ui.Caps, ui.Muted)
		}
	}

	// the graph, below
	g := image.Rect(20, dial.Max.Y+12, ui.W-20, ui.H-62)
	s.Panel(g, 16)
	a.drawGraphs(g, l)
	s.Present()
}

func ms(v float64) string {
	if v <= 0 {
		return ""
	}
	if v < 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

func jitter(r Result) string {
	if r.PingMs <= 0 {
		return ""
	}
	s := "JITTER " + ms(max(r.JitterMs, 0.1)) + " MS"
	if r.Loss > 0 {
		s += fmt.Sprintf(" · %.0f%% LOST", r.Loss*100)
	}
	return s
}

// mbps formats a speed in Mbit/s in the unit chosen in Settings.
func (a *app) mbps(v float64) string { return mbps(a.set.speed(v)) }

func mbps(v float64) string {
	switch {
	case v <= 0:
		return ""
	case v < 10:
		return fmt.Sprintf("%.2f", v)
	case v < 100:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// dialFrac maps a speed onto the dial: logarithmic, so 1, 10 and 100
// Mbit/s sit at a third, two thirds and the end.
func dialFrac(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Min(1, math.Log10(1+v)/math.Log10(101))
}

func (a *app) drawDial(r image.Rectangle, l Live, live bool) {
	s := a.scr
	cx, cy := float64(r.Min.X+r.Dx()/2), float64(r.Min.Y+r.Dy()/2+2)
	const rad, th = 80.0, 14.0
	s.Ring(cx, cy, rad, th, 1, ui.Line)

	label, val, unit := "READY", "GO", "PRESS A"
	col := colDown()
	frac := 0.0
	switch {
	case live && l.Phase == PhasePing:
		label, val, unit, col, frac = "LATENCY", "…", "ms", colPing, l.Progress
	case live && (l.Phase == PhaseDown || l.Phase == PhaseUp):
		label, unit = "DOWNLOAD", a.set.unit()
		if l.Phase == PhaseUp {
			label, col = "UPLOAD", colUp
		}
		val = a.mbps(l.Mbps)
		if val == "" {
			val = "0"
		}
		frac = dialFrac(l.Mbps)
	case a.failed != nil:
		label, val, unit, col = "ERROR", "!", "SEE BELOW", ui.Bad
	case a.hasShown:
		label, val, unit, frac = "DOWNLOAD", a.mbps(a.shown.Down), a.set.unit(), dialFrac(a.shown.Down)
	}
	if frac > 0 {
		s.Ring(cx, cy, rad, th, frac, col)
	}
	icx := int(cx)
	s.TextCenter(icx, int(cy)-46, label, ui.Caps, ui.Muted)
	s.TextCenter(icx, int(cy)-ui.LineHeight(ui.Big)/2, val, ui.Big, ui.Text)
	s.TextCenter(icx, int(cy)+24, unit, ui.Caps, ui.Muted)
	// the test's progress, as a thin bar under the dial
	if live {
		total := (float64(l.Phase-PhasePing) + l.Progress) / 3
		s.Bar(image.Rect(r.Min.X+24, r.Max.Y-14, r.Max.X-24, r.Max.Y-10), math.Min(1, total), col)
	}
}

func (a *app) drawGraphs(g image.Rectangle, l Live) {
	s := a.scr
	if a.failed != nil && !a.running() {
		s.Text(g.Min.X+18, g.Min.Y+14, "The test didn't finish", ui.Heading, ui.Text)
		y := g.Min.Y + 18 + ui.LineHeight(ui.Heading)
		for _, line := range ui.Wrap(ui.Small, a.failed.Error(), g.Dx()-36) {
			if y+ui.LineHeight(ui.Small) > g.Max.Y-8 {
				break
			}
			s.Text(g.Min.X+18, y, line, ui.Small, ui.Muted)
			y += ui.LineHeight(ui.Small)
		}
		return
	}
	if len(l.Down) == 0 && len(l.Up) == 0 {
		msg := "Press A to test your connection"
		if a.hasShown {
			msg = "Last test " + ago(a.shown.When) + ". Press A to test again."
		}
		lines := ui.Wrap(ui.Body, msg, g.Dx()-40)
		y := g.Min.Y + (g.Dy()-len(lines)*ui.LineHeight(ui.Body))/2
		for _, line := range lines {
			s.TextCenter(g.Min.X+g.Dx()/2, y, line, ui.Body, ui.Muted)
			y += ui.LineHeight(ui.Body)
		}
		return
	}
	half := g.Dx() / 2
	// Scale to what the connection sustained: the first moments of an
	// upload only fill the network buffers and read far higher than the
	// line can carry (the result leaves them out the same way).
	peak := max(1.0, steadyPeak(l.Down), steadyPeak(l.Up))
	// one bar per sample: as many as the running test will take, or as
	// the shown result has
	slots := max(len(l.Down), len(l.Up), 1)
	if a.running() {
		d := a.test.plan.Duration
		if d == 0 {
			d = defaultPlan.Duration
		}
		slots = max(slots, int(d/sampleEvery))
	}
	for i, p := range []struct {
		name string
		v    []float64
		c    color.RGBA
	}{{"DOWNLOAD", l.Down, colDown()}, {"UPLOAD", l.Up, colUp}} {
		r := image.Rect(g.Min.X+16+i*half, g.Min.Y+12, g.Min.X+i*half+half-16, g.Max.Y-12)
		s.Text(r.Min.X, r.Min.Y, p.name, ui.Caps, ui.Muted)
		if len(p.v) > 0 {
			s.TextRight(r.Max.X, r.Min.Y, "peak "+a.mbps(steadyPeak(p.v)), ui.Caps, ui.Muted)
		}
		bars(s, image.Rect(r.Min.X, r.Min.Y+20, r.Max.X, r.Max.Y), p.v, slots, peak, p.c)
	}
}

// steadyPeak is the highest speed after the warm-up (all of them if the
// test stopped before it ended).
func steadyPeak(v []float64) float64 {
	warm := int(defaultPlan.Warmup / sampleEvery)
	if len(v) > warm {
		v = v[warm:]
	}
	if len(v) == 0 {
		return 0
	}
	return slices.Max(v)
}

// bars draws one bar per half-second sample in n slots, snapped to the
// 2-pixel grid.
func bars(s *ui.Screen, r image.Rectangle, v []float64, n int, peak float64, c color.RGBA) {
	s.Fill(image.Rect(r.Min.X, r.Max.Y-2, r.Max.X, r.Max.Y), ui.Line) // baseline
	step := (r.Dx() / n) &^ 1
	w := max(2, step-4) &^ 1
	for i, x := range v {
		if i >= n {
			break
		}
		h := int(float64(r.Dy()-4)*min(x, peak)/peak) &^ 1
		if x > 0 {
			h = max(h, 2)
		}
		bx := r.Min.X + i*step
		s.Fill(image.Rect(bx, r.Max.Y-2-h, bx+w, r.Max.Y-2), c)
	}
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	}
	return t.Format("Jan 2")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// humanize turns common network errors into advice; others pass through.
func humanize(err error) error {
	msg := err.Error()
	var hint string
	switch {
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving"):
		hint = "The server's name couldn't be looked up: is Wi-Fi connected to the internet?"
	case strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "no route to host"):
		hint = "No network: turn Wi-Fi on in Onion's settings."
	case strings.Contains(msg, "connection refused"):
		hint = "The server refused the connection: check its address and port."
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		hint = "The server took too long to answer."
	case strings.Contains(msg, "certificate"):
		hint = "The server's certificate isn't valid (or the Miyoo's clock is wrong)."
	default:
		return err
	}
	return fmt.Errorf("%s (%s)", hint, msg)
}

// round1 keeps a tenth of a Mbit/s: plenty for a graph, and short in JSON.
func round1(v []float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Round(x*10) / 10
	}
	return out
}
