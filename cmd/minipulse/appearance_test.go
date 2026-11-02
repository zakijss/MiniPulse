package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zakijss/minideck/ui"
)

// presses is input that presses each button in turn, then ends.
func presses(bs ...ui.Button) <-chan ui.Event {
	ch := make(chan ui.Event, len(bs))
	for _, b := range bs {
		ch <- ui.Event{Button: b, Pressed: true}
	}
	close(ch)
	return ch
}

// lookScreen opens a screen that draws nowhere, with the shared look saved
// to a temporary file. The look in use is put back after the test.
func lookScreen(t *testing.T) (*ui.Screen, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "appearance.json")
	t.Setenv("MINIDECK_UI_NULL", "1")
	t.Setenv("MINIDECK_APPEARANCE", path)
	scr, err := ui.Open()
	if err != nil {
		t.Fatal(err)
	}
	start := ui.CurrentAppearance()
	t.Cleanup(func() { scr.SetAppearance(start) })
	return scr, path
}

// wantLookChanged checks that Appearance was opened: RIGHT on its first
// row moved to the next theme, and B saved it.
func wantLookChanged(t *testing.T, start ui.Appearance, path string) {
	t.Helper()
	if ui.CurrentAppearance().Theme == start.Theme {
		t.Errorf("theme still %q: Appearance didn't open", start.Theme)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("look not saved: %v", err)
	}
}

// SELECT on the main screen opens Appearance while no test runs.
func TestSelectOpensAppearance(t *testing.T) {
	scr, path := lookScreen(t)
	start := ui.CurrentAppearance()
	a := &app{scr: scr, input: presses(ui.Select, ui.Right, ui.B, ui.B), st: store{dir: t.TempDir()}, changed: make(chan struct{}, 1)}
	a.run()
	wantLookChanged(t, start, path)
}
