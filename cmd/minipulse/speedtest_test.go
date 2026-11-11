package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingStats(t *testing.T) {
	m, j := pingStats([]float64{20, 22, 21, 30, 20})
	if m != 21 {
		t.Errorf("median %v, want 21", m)
	}
	// |22-20| + |21-22| + |30-21| + |20-30| = 22, over 4
	if j != 5.5 {
		t.Errorf("jitter %v, want 5.5", j)
	}
	if m, _ := pingStats([]float64{10, 20}); m != 15 {
		t.Errorf("even median %v, want 15", m)
	}
}

func TestServerTime(t *testing.T) {
	h := http.Header{}
	h.Add("Server-Timing", "cfSpeedEdge;dur=3, cfSpeedWorker;dur=19")
	h.Add("Server-Timing", `cfL4;desc="?proto=TCP&rtt=20990&min_rtt=18477"`)
	if got := serverTime(h); got != 22*time.Millisecond {
		t.Errorf("serverTime = %v, want 22ms", got)
	}
	if got := serverTime(http.Header{}); got != 0 {
		t.Errorf("no header: %v", got)
	}
}

func TestParseWireless(t *testing.T) {
	in := `Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE
 face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22
 wlan0: 0000  100.  -74.  -256.       0      0      0      0      0        0
 wlan1: 0000    0.  -256.  -256.       0      0      0      0      0        0
`
	if d, ok := parseWireless(strings.NewReader(in), "wlan0"); !ok || d != -74 {
		t.Errorf("wlan0 = %d %v", d, ok)
	}
	if _, ok := parseWireless(strings.NewReader(in), "wlan1"); ok {
		t.Error("wlan1 (-256, not associated) should be unknown")
	}
	if signalWords(-74) != "Weak" || signalWords(-50) != "Excellent" {
		t.Error("signal words")
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"speed.example.lan:8080":           "http://speed.example.lan:8080/",
		"https://speed.example.ts.net":     "https://speed.example.ts.net/",
		"http://10.0.0.2/speed/index.html": "http://10.0.0.2/speed/",
		" http://10.0.0.2/speed/?x=1#top ": "http://10.0.0.2/speed/",
	} {
		if got, err := normalizeServer(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "http://"} {
		if _, err := normalizeServer(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestIsTailnet(t *testing.T) {
	for h, want := range map[string]bool{"box.tail1234.ts.net": true, "100.101.102.103": true, "100.128.0.1": false, "192.168.1.5": false, "ts.net.example.com": false} {
		if isTailnet(h) != want {
			t.Errorf("isTailnet(%q) != %v", h, want)
		}
	}
	if hostOf("http://[fd7a::1]:80/x") != "fd7a::1" || hostOf("https://a.b:8080/") != "a.b" || hostOf("http://a.b/") != "a.b" {
		t.Error("hostOf")
	}
}

func TestHistoryCap(t *testing.T) {
	var h []Result
	for i := range maxHistory + 5 {
		h = addResult(h, Result{Down: float64(i)})
	}
	if len(h) != maxHistory || h[0].Down != maxHistory+4 {
		t.Errorf("len %d, first %v", len(h), h[0].Down)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st := store{dir: t.TempDir()}
	want := Settings{Server: "http://nas/", Kind: "librespeed", Saved: []string{"http://nas/"}}
	if err := st.save("settings.json", want); err != nil {
		t.Fatal(err)
	}
	if got := st.settings(); got.Server != want.Server || got.Kind != want.Kind || len(got.Saved) != 1 {
		t.Errorf("got %+v", got)
	}
	if got := (store{dir: t.TempDir()}).settings(); got.Server != "" {
		t.Error("missing file should give defaults")
	}
	os.WriteFile(filepath.Join(st.dir, "history.json"), []byte(`[{"when": "yesterday"`), 0o644)
	if h := st.history(); h != nil {
		t.Errorf("corrupt history gave %v", h)
	}
}

// Streams that fail in different ways (an error status, a dropped
// connection) are all reported without upsetting the test.
func TestTransferMixedErrors(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			c.Close()
		}
	}))
	defer srv.Close()
	plan := Plan{Duration: 1200 * time.Millisecond, Warmup: 200 * time.Millisecond, Streams: 3, DownChunk: 1 << 20, UpChunk: 1 << 20}
	test := NewTest(selfHosted{base: srv.URL + "/", kind: "librespeed"}, plan, newClient(), nil)
	if _, err := test.transfer(context.Background(), false); err == nil {
		t.Error("no error from a server that never answers properly")
	}
}

// A LibreSpeed-like server on loopback: the whole test runs against it.
func TestRunAgainstLibreSpeed(t *testing.T) {
	var upBytes atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/backend/empty.php", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		upBytes.Add(n)
	})
	mux.HandleFunc("/backend/garbage.php", func(w http.ResponseWriter, r *http.Request) {
		mb, _ := strconv.Atoi(r.URL.Query().Get("ckSize"))
		buf := make([]byte, 1<<20)
		for range mb {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newClient()
	base, _ := normalizeServer(srv.URL)
	sv, err := detect(context.Background(), c, base)
	if err != nil || sv.kind != "librespeed" {
		t.Fatalf("detect: %+v %v", sv, err)
	}
	plan := Plan{Pings: 4, Duration: 1500 * time.Millisecond, Warmup: 500 * time.Millisecond, Streams: 2, DownChunk: 4 << 20, UpChunk: 1 << 20}
	test := NewTest(sv, plan, c, nil)
	r := test.Run(context.Background())
	if l := test.Live(); l.Err != nil || l.Phase != PhaseDone {
		t.Fatalf("live %+v", l)
	}
	if r.Down <= 0 || r.Up <= 0 || r.PingMs < 0 {
		t.Errorf("result %+v", r)
	}
	if upBytes.Load() == 0 {
		t.Error("nothing uploaded")
	}
}

func TestStopEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	sv := selfHosted{base: srv.URL + "/", kind: "librespeed"}
	test := NewTest(sv, defaultPlan, newClient(), func() {})
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	test.Run(ctx)
	if time.Since(start) > 3*time.Second {
		t.Error("stop took too long")
	}
	if err := test.Live().Err; err != errStopped {
		t.Errorf("err = %v, want stopped", err)
	}
}

func TestSteadyPeak(t *testing.T) {
	// the first samples (buffers filling) don't count
	v := []float64{7.9, 3.1, 1.5, 1.4, 1.6, 1.5}
	if got := steadyPeak(v); got != 1.6 {
		t.Errorf("steadyPeak = %v, want 1.6", got)
	}
	if got := steadyPeak([]float64{2, 3}); got != 3 {
		t.Errorf("short test: %v, want 3", got)
	}
	if steadyPeak(nil) != 0 {
		t.Error("no samples")
	}
}

// A new result is compared with the median of the earlier ones, once
// there are three.
func TestVsUsual(t *testing.T) {
	a := &app{hist: []Result{{Down: 50}, {Down: 100}}}
	if got := a.vsUsual(Result{Down: 80}); got != "" {
		t.Errorf("two earlier results: %q, want nothing", got)
	}
	a.hist = append(a.hist, Result{Down: 200})
	for down, want := range map[float64]string{
		120: "Download 20% faster than your usual 100 Mbit/s",
		50:  "Download 50% slower than your usual 100 Mbit/s",
		95:  "Download about your usual 100 Mbit/s",
		0:   "",
	} {
		if got := a.vsUsual(Result{Down: down}); got != want {
			t.Errorf("%v Mbit/s: %q, want %q", down, got, want)
		}
	}
}
