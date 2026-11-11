package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zakijss/minideck/ui"
)

// An older settings.json, without the test settings, keeps today's test:
// ten seconds each way over three connections, in Mbit/s.
func TestSettingsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"server": "http://nas:8080", "kind": "librespeed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	set := store{dir: dir}.settings()
	if p := set.plan(); p != defaultPlan {
		t.Errorf("plan %+v, want %+v", p, defaultPlan)
	}
	if set.megabytes() || set.unit() != "Mbit/s" || set.speed(40) != 40 {
		t.Error("speeds aren't in Mbit/s by default")
	}
	// values that aren't offered also give the defaults
	if p := (Settings{Seconds: 7, Streams: 99}).plan(); p != defaultPlan {
		t.Errorf("odd values give plan %+v", p)
	}
}

func TestPlanFromSettings(t *testing.T) {
	p := Settings{Seconds: 20, Streams: 6}.plan()
	if p.Duration != 20*time.Second || p.Streams != 6 {
		t.Errorf("plan %+v", p)
	}
	if p.Warmup != defaultPlan.Warmup || p.Pings != defaultPlan.Pings || p.DownChunk != defaultPlan.DownChunk {
		t.Errorf("the rest of the plan changed: %+v", p)
	}
	if p := (Settings{Seconds: 5, Streams: 1}).plan(); p.Duration != 5*time.Second || p.Streams != 1 {
		t.Errorf("plan %+v", p)
	}
}

func TestUnits(t *testing.T) {
	set := Settings{Units: "MB/s"}
	if set.unit() != "MB/s" || set.speed(40) != 5 {
		t.Errorf("40 Mbit/s is %v %s", set.speed(40), set.unit())
	}
	a := &app{set: set}
	if got := a.mbps(28.43); got != "3.55" {
		t.Errorf("28.43 Mbit/s shows as %q MB/s", got)
	}
	if got := historyItem(Result{Down: 80, Up: 16}, set).Label; got != "↓ 10.0  ↑ 2.00" {
		t.Errorf("history row %q", got)
	}
	if got := historyItem(Result{Down: 80, Up: 16}, Settings{}).Label; got != "↓ 80.0  ↑ 16.0" {
		t.Errorf("history row %q", got)
	}
}

// A and right step a setting forward, left steps it back, and each change
// is saved; B goes back to the main screen.
func TestSettingsScreen(t *testing.T) {
	scr, _ := lookScreen(t)
	dir := t.TempDir()
	a := &app{scr: scr, st: store{dir: dir}, changed: make(chan struct{}, 1), input: presses(
		ui.Y,              // Settings
		ui.Down, ui.Right, // 20 s
		ui.Down, ui.Left, // 1 connection
		ui.Down, ui.A, // MB/s
		ui.B, ui.B)}
	a.run()
	want := Settings{Seconds: 20, Streams: 1, Units: "MB/s"}
	if !reflect.DeepEqual(a.set, want) {
		t.Fatalf("settings %+v, want %+v", a.set, want)
	}
	if got := (store{dir: dir}).settings(); !reflect.DeepEqual(got, want) {
		t.Errorf("saved %+v, want %+v", got, want)
	}
}

func TestSettingsCycle(t *testing.T) {
	var set Settings
	var seen []string
	for range 3 {
		change(&set, setLength, 1)
		seen = append(seen, settingsItems(set)[1].Detail)
	}
	if want := []string{"20 s", "5 s", "10 s"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("test length steps %v, want %v", seen, want)
	}
	set = Settings{}
	change(&set, setStreams, -1)
	if set.Streams != 1 {
		t.Errorf("left from 3 connections gives %d", set.Streams)
	}
	change(&set, setStreams, -1)
	if set.Streams != 6 {
		t.Errorf("left from 1 connection gives %d", set.Streams)
	}
	change(&set, setUnits, 1)
	change(&set, setUnits, -1)
	if set.Units != "" {
		t.Errorf("units %q after two steps", set.Units)
	}
}

// A test runs for the length and over the connections set. (The server
// is a closed local port; the test is stopped at once.)
func TestTestUsesSettings(t *testing.T) {
	scr, _ := lookScreen(t)
	a := &app{scr: scr, set: Settings{Server: "http://127.0.0.1:1", Kind: "librespeed", Seconds: 5, Streams: 1},
		client: newClient(), changed: make(chan struct{}, 1)}
	a.start()
	if a.test == nil {
		t.Skip("Wi-Fi reported off")
	}
	plan := a.test.plan
	a.stop()
	<-a.done
	if plan.Duration != 5*time.Second || plan.Streams != 1 {
		t.Errorf("test plan %+v", plan)
	}
}

// In History, R2 jumps to the oldest result and A shows it on the main
// screen.
func TestHistoryShow(t *testing.T) {
	scr, _ := lookScreen(t)
	h := []Result{{When: time.Now(), Down: 10}, {When: time.Now().Add(-time.Hour), Down: 20}, {When: time.Now().Add(-2 * time.Hour), Down: 30}}
	a := &app{scr: scr, st: store{dir: t.TempDir()}, changed: make(chan struct{}, 1), hist: h,
		input: presses(ui.X, ui.R2, ui.A)}
	a.run()
	if !a.hasShown || a.shown.Down != 30 {
		t.Errorf("shown %+v, want the oldest result", a.shown)
	}
}
