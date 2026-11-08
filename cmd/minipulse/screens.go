package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"

	"github.com/zakijss/minideck/ui"
)

// setting is a Settings row that left and right change in place.
type setting int

const (
	setLength setting = iota
	setStreams
	setUnits
)

// settingsItems is the Settings list: the server, the test's length and
// connections, the unit, and Appearance.
func settingsItems(set Settings) []ui.Item {
	p := set.plan()
	srv := set.server().Name()
	if set.Server != "" {
		srv += " · " + kindName(set.Kind)
	}
	streams := ui.Item{Label: "Connections", Icon: "list", Detail: fmt.Sprint(p.Streams), Value: setStreams,
		Sub: "Three fill the Mini Plus's Wi-Fi"}
	switch p.Streams {
	case 1:
		streams.Sub = "Like a single download"
	case 6:
		streams.Sub = "Busier CPU, rarely faster"
	}
	units := ui.Item{Label: "Speeds in", Icon: "chart", Detail: "Mbit/s", Value: setUnits,
		Sub: "Megabits, as internet plans"}
	if set.megabytes() {
		units.Detail, units.Sub = "MB/s", "Megabytes, as downloads show"
	}
	return []ui.Item{
		{Label: "Server", Icon: "server", Sub: srv, Chevron: true},
		{Label: "Test length", Icon: "clock", Detail: fmt.Sprintf("%.0f s", p.Duration.Seconds()), Value: setLength,
			Sub: "Each way, after the latency check"},
		streams,
		units,
		ui.AppearanceItem(),
	}
}

// change steps setting r forward (step 1) or back (-1), wrapping round.
func change(set *Settings, r setting, step int) {
	p := set.plan()
	switch r {
	case setLength:
		set.Seconds = stepInt(lengths, int(p.Duration/time.Second), step)
	case setStreams:
		set.Streams = stepInt(connections, p.Streams, step)
	case setUnits:
		if set.megabytes() {
			set.Units = ""
		} else {
			set.Units = "MB/s"
		}
	}
}

// stepInt moves cur step places along list, wrapping round; a value not
// in list starts from the first.
func stepInt(list []int, cur, step int) int {
	i := max(0, slices.Index(list, cur))
	return list[((i+step)%len(list)+len(list))%len(list)]
}

// settings is the Settings screen. A opens a row; on a setting, A and
// right step it forward and left steps it back.
func (a *app) settings() {
	s := a.scr
	l := ui.List{}
	for {
		l.Items = settingsItems(a.set)
		top := s.StaticFrame("Settings", "Left and right change a setting", ui.Hint{Button: "A", Label: "Choose"}, ui.Hint{Button: "B", Label: "Back"})
		l.Draw(s, image.Rect(20, top, ui.W-26, ui.H-58))
		s.Present()
		ev, ok, frame := s.Next(a.input)
		if !ok {
			return
		}
		if frame || !ev.Pressed {
			continue
		}
		it := l.Selected()
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.A, ui.Left, ui.Right:
			if r, ok := it.Value.(setting); ok {
				step := 1
				if ev.Button == ui.Left {
					step = -1
				}
				old := a.set
				change(&a.set, r, step)
				if err := a.st.save("settings.json", a.set); err != nil {
					a.set = old
					s.Notice(a.input, "Couldn't save", err.Error()+". Check that the card has free space.")
				}
				continue
			}
			if ev.Button != ui.A {
				l.Handle(ev)
				continue
			}
			if it.Label == "Appearance" {
				s.Appearance(a.input)
			} else {
				a.chooseServer()
			}
		default:
			l.Handle(ev)
		}
	}
}

// chooseServer picks Cloudflare, a saved server, or a new one.
func (a *app) chooseServer() {
	items := []ui.Item{{Label: "Cloudflare", Sub: "The nearest Cloudflare data centre · no sign-up", Icon: "wifi"}}
	for _, u := range a.set.Saved {
		items = append(items, ui.Item{Label: selfHosted{base: u}.Name(), Sub: u, Icon: "server", Value: u})
	}
	items = append(items, ui.Item{Label: "Add your own server", Sub: "LibreSpeed or OpenSpeedTest, at home or on your tailnet", Icon: "plus", Chevron: true})
	for i, it := range items {
		if (i == 0 && a.set.Server == "") || (it.Value != nil && it.Value == a.set.Server) {
			items[i].Detail = "in use"
		}
	}
	i, ok := a.scr.Choose(a.input, "Test against", items)
	if !ok {
		return
	}
	switch {
	case i == 0:
		a.set.Server, a.set.Kind = "", ""
	case i == len(items)-1:
		a.addServer()
		return
	default:
		if err := a.useServer(items[i].Value.(string)); err != nil {
			a.scr.Notice(a.input, "Can't use that server", err.Error())
		}
		return
	}
	a.saveSettings()
}

func (a *app) addServer() {
	addr := "http://"
	for {
		in, ok := a.scr.Prompt(a.input, "Server address", addr, "e.g. http://nas.local:8080 or https://speed.your-tailnet.ts.net", false)
		if !ok {
			return
		}
		addr = in
		base, err := normalizeServer(in)
		if err == nil {
			err = a.useServer(base)
		}
		if err == nil {
			return
		}
		a.scr.Notice(a.input, "Can't use that server", err.Error())
	}
}

// useServer checks that a speed test server answers at base, then makes it
// the one in use and remembers it.
func (a *app) useServer(base string) error {
	s := a.scr
	top := s.StaticFrame("Test against", "Checking "+base)
	s.Loading(top+120, "Looking for a speed test server…")
	s.Present()
	sv, err := detect(context.Background(), a.client, base)
	if err != nil {
		if isTailnet(hostOf(base)) && !fileExists(tailscaleSock) {
			err = fmt.Errorf("%v. It's on your tailnet: turn MiniScale on first", err)
		}
		return err
	}
	a.set.Server, a.set.Kind = sv.base, sv.kind
	a.set.Saved = slices.DeleteFunc(a.set.Saved, func(u string) bool { return u == sv.base })
	a.set.Saved = append([]string{sv.base}, a.set.Saved...)
	if len(a.set.Saved) > 8 {
		a.set.Saved = a.set.Saved[:8]
	}
	a.saveSettings()
	a.note = "Testing against " + sv.Name() + " (" + kindName(sv.kind) + ")"
	return nil
}

func kindName(k string) string {
	if k == "openspeedtest" {
		return "OpenSpeedTest"
	}
	return "LibreSpeed"
}

func (a *app) saveSettings() {
	if err := a.st.save("settings.json", a.set); err != nil {
		a.note = "Couldn't save the setting: " + err.Error()
	}
}

// history lists past results, newest first.
func (a *app) history() {
	s := a.scr
	var l ui.List
	for {
		if len(a.hist) == 0 {
			top := s.StaticFrame("History", "No tests yet", ui.Hint{Button: "B", Label: "Back"})
			msg := "Tests you run with A show up here, newest first."
			s.TextCenter(ui.W/2, top+120, msg, ui.Body, ui.Muted)
			s.Present()
		} else {
			l.Items = l.Items[:0]
			for _, r := range a.hist {
				l.Items = append(l.Items, historyItem(r, a.set))
			}
			if l.Sel >= len(l.Items) {
				l.Sel = len(l.Items) - 1
			}
			sub := fmt.Sprintf("%s · %s, median ↓ %s  ↑ %s", plural(len(a.hist), "test"), a.set.unit(),
				orDash(a.mbps(median(a.hist, func(r Result) float64 { return r.Down }))),
				orDash(a.mbps(median(a.hist, func(r Result) float64 { return r.Up }))))
			top := s.StaticFrame("History", sub, ui.Hint{Button: "Y", Label: "Delete"},
				ui.Hint{Button: "X", Label: "Clear all"}, ui.Hint{Button: "B", Label: "Back"})
			l.Draw(s, image.Rect(20, top, ui.W-26, ui.H-58))
			s.Present()
		}
		ev, ok := <-a.input
		if !ok {
			return
		}
		if !ev.Pressed {
			continue
		}
		switch ev.Button {
		case ui.B, ui.Menu:
			return
		case ui.Y:
			// Asks first, like Clear all: one stray press used to remove a
			// result with no way back.
			if len(a.hist) > 0 && !ev.Repeat && s.Confirm(a.input, "Delete this result?",
				"The test from "+a.hist[l.Sel].When.Local().Format("Jan 2 15:04")+" will be removed.", "Delete") {
				a.hist = slices.Delete(a.hist, l.Sel, l.Sel+1)
				a.saveHistory()
			}
		case ui.X:
			if len(a.hist) > 0 && !ev.Repeat && s.Confirm(a.input, "Clear history?", "All "+plural(len(a.hist), "result")+" will be removed.", "Clear") {
				a.hist = nil
				a.saveHistory()
			}
		default:
			l.Handle(ev)
		}
	}
}

func (a *app) saveHistory() {
	if len(a.hist) == 0 {
		a.hasShown, a.last = false, Live{}
	} else if a.hist[0].When != a.shown.When {
		a.shown = a.hist[0]
		a.last = Live{Phase: PhaseDone, Down: a.shown.DownS, Up: a.shown.UpS, Result: a.shown}
	}
	if err := a.st.save("history.json", a.hist); err != nil {
		a.scr.Notice(a.input, "Couldn't save", err.Error()+". Check that the card has free space.")
	}
}

// historyItem is one result in History, its speeds in the unit set chooses.
func historyItem(r Result, set Settings) ui.Item {
	where := r.Server
	if r.Where != "" && r.Where != r.Server {
		where = r.Where
		if r.Server != "Cloudflare" {
			where = r.Server
		}
	}
	sub := r.When.Local().Format("Jan 2 15:04") + " · " + where
	if r.Signal != 0 {
		sub += fmt.Sprintf(" · %d dBm", r.Signal)
	}
	return ui.Item{
		Label:  fmt.Sprintf("↓ %s  ↑ %s", orDash(mbps(set.speed(r.Down))), orDash(mbps(set.speed(r.Up)))),
		Sub:    sub,
		Detail: orDash(ms(r.PingMs)) + " ms",
		Icon:   "chart",
	}
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func median(h []Result, f func(Result) float64) float64 {
	var v []float64
	for _, r := range h {
		if x := f(r); x > 0 {
			v = append(v, x)
		}
	}
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	if len(v)%2 == 0 {
		return (v[len(v)/2-1] + v[len(v)/2]) / 2
	}
	return v[len(v)/2]
}

func hostOf(base string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.Trim(h, "[]")
}
