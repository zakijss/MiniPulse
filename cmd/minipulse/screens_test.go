package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zakijss/minideck/ui"
)

// TestScreens draws the app's screens to PNG files for review. It runs
// only with MINIDECK_GALLERY=DIR set.
func TestScreens(t *testing.T) {
	dir := os.Getenv("MINIDECK_GALLERY")
	if dir == "" {
		t.Skip("set MINIDECK_GALLERY=DIR to draw the screens")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := func(name string, f func(*app)) {
		t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "minipulse-"+name+".png"))
		scr, err := ui.Open()
		if err != nil {
			t.Fatal(err)
		}
		a := &app{scr: scr, st: store{dir: t.TempDir()}, changed: make(chan struct{}, 1)}
		f(a)
		a.draw()
	}
	down := []float64{3.1, 14.2, 22.5, 26.8, 27.9, 28.4, 27.2, 28.9, 29.3, 28.1, 27.7, 28.6, 29.0, 28.8, 27.9, 28.3, 28.7, 29.1, 28.2, 28.5}
	up := []float64{1.2, 8.4, 15.6, 19.8, 20.4, 21.2, 20.9, 21.5, 20.7, 21.0, 21.3}
	res := Result{When: time.Now().Add(-3 * time.Minute), Server: "Cloudflare", Where: "Frankfurt (FRA)", PingMs: 21.6, JitterMs: 3.2, Down: 28.43, Up: 20.96}

	shot("1-first-run", func(a *app) {})
	shot("2-latency", func(a *app) {
		a.test = &Test{live: Live{Phase: PhasePing, Progress: 0.5, Result: Result{Server: "Cloudflare", Where: "Frankfurt (FRA)"}}}
	})
	shot("3-download", func(a *app) {
		r := res
		r.Down, r.Up = 0, 0
		a.test = &Test{live: Live{Phase: PhaseDown, Progress: 0.7, Mbps: 28.6, Down: down[:14], Result: r}}
	})
	shot("4-upload", func(a *app) {
		r := res
		r.Up = 0
		a.test = &Test{live: Live{Phase: PhaseUp, Progress: 0.5, Mbps: 21.3, Down: down, Up: up, Result: r}}
	})
	shot("5-done", func(a *app) {
		a.shown, a.hasShown = res, true
		a.last = Live{Phase: PhaseDone, Down: down, Up: up, Result: res}
	})
	shot("6-error", func(a *app) {
		a.failed = errorString("can't reach Cloudflare: dial tcp: lookup speed.cloudflare.com: no such host")
	})
	shot("8-done-megabytes-20s", func(a *app) {
		long := append(append([]float64{}, down...), down...)
		a.set = Settings{Seconds: 20, Units: "MB/s"}
		a.shown, a.hasShown = res, true
		a.last = Live{Phase: PhaseDone, Down: long, Up: up, Result: res}
	})
	shot("9-download-5s", func(a *app) {
		r := res
		r.Down, r.Up = 0, 0
		a.test = &Test{plan: Settings{Seconds: 5}.plan(), live: Live{Phase: PhaseDown, Progress: 0.6, Mbps: 28.6, Down: down[:6], Result: r}}
	})
	t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "minipulse-10-settings.png"))
	scr, err := ui.Open()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan ui.Event)
	close(closed)
	(&app{scr: scr, input: closed, set: Settings{Server: "http://speed.example.lan:8080", Kind: "librespeed", Streams: 6}}).settings()
	scr.Close()
	t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "minipulse-11-settings-megabytes.png"))
	if scr, err = ui.Open(); err != nil {
		t.Fatal(err)
	}
	(&app{scr: scr, input: closed, set: Settings{Streams: 1, Units: "MB/s"}}).settings()
	scr.Close()

	t.Setenv("MINIDECK_UI_PNG", filepath.Join(dir, "minipulse-7-history.png"))
	scr, err = ui.Open()
	if err != nil {
		t.Fatal(err)
	}
	h := []Result{res, {When: time.Now().Add(-26 * time.Hour), Server: "speed.example.lan:8080", Where: "LibreSpeed", PingMs: 4.1, Down: 34.2, Up: 31.8, Signal: -58}}
	a := &app{scr: scr, hist: h}
	ch := make(chan ui.Event)
	close(ch)
	a.input = ch
	a.history() // draws once, then the closed input ends it
}

type errorString string

func (e errorString) Error() string { return string(e) }
