package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zakijss/minideck/atomicfile"
)

// Settings are the app's choices, kept in settings.json in the app's
// folder. Releases don't ship that file (nor history.json), so an update
// keeps both.
type Settings struct {
	// Server is "" for Cloudflare, else a self-hosted server's address.
	Server string `json:"server,omitempty"`
	Kind   string `json:"kind,omitempty"` // its kind, as detected
	// Recent self-hosted servers, newest first.
	Saved []string `json:"saved,omitempty"`
	// Seconds each way: 5, 10 or 20 (0 means the default, 10).
	Seconds int `json:"test_seconds,omitempty"`
	// Connections at once: 1, 3 or 6 (0 means the default, 3).
	Streams int `json:"connections,omitempty"`
	// Units is "" for Mbit/s or "MB/s". Results are kept in Mbit/s.
	Units string `json:"units,omitempty"`
}

// Choices for Settings' Test length and Connections rows.
var (
	lengths     = []int{5, 10, 20}
	connections = []int{1, 3, 6}
)

// plan is defaultPlan with the test length and connections chosen.
func (s Settings) plan() Plan {
	p := defaultPlan
	if slices.Contains(lengths, s.Seconds) {
		p.Duration = time.Duration(s.Seconds) * time.Second
	}
	if slices.Contains(connections, s.Streams) {
		p.Streams = s.Streams
	}
	return p
}

// megabytes reports whether speeds are shown in MB/s.
func (s Settings) megabytes() bool { return s.Units == "MB/s" }

// speed converts a speed in Mbit/s to the unit chosen.
func (s Settings) speed(mbit float64) float64 {
	if s.megabytes() {
		return mbit / 8
	}
	return mbit
}

// unit names the unit speeds are shown in.
func (s Settings) unit() string {
	if s.megabytes() {
		return "MB/s"
	}
	return "Mbit/s"
}

func (s Settings) server() Server {
	if s.Server == "" {
		return cloudflare{}
	}
	return selfHosted{base: s.Server, kind: s.Kind}
}

// maxHistory is how many results history.json keeps.
const maxHistory = 50

// store reads and writes the app's JSON files in dir.
type store struct{ dir string }

func (st store) load(name string, v any) error {
	b, err := os.ReadFile(filepath.Join(st.dir, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// save writes v as JSON, atomically: a power cut mid-write leaves the old
// file.
func (st store) save(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(st.dir, name), append(b, '\n'), 0o644)
}

// settings reads settings.json. A missing or unreadable file gives the
// defaults (Cloudflare); an unreadable one is logged and replaced on the
// next save.
func (st store) settings() Settings {
	var s Settings
	if err := st.load("settings.json", &s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("settings.json: %v", err)
		return Settings{}
	}
	return s
}

// history reads history.json, newest first. A missing or unreadable file
// gives an empty history.
func (st store) history() []Result {
	var h []Result
	if err := st.load("history.json", &h); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("history.json: %v", err)
		return nil
	}
	return h
}

// addResult puts r first and keeps the newest maxHistory.
func addResult(h []Result, r Result) []Result {
	h = append([]Result{r}, h...)
	if len(h) > maxHistory {
		h = h[:maxHistory]
	}
	return h
}

// ---- Wi-Fi ----

// wifiSignal reads wlan0's signal level in dBm from /proc/net/wireless:
//
//	wlan0: 0000  100.  -74.  -256. ...
func wifiSignal() (int, bool) {
	f, err := os.Open("/proc/net/wireless")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return parseWireless(f, "wlan0")
}

// parseWireless finds iface's signal level in the /proc/net/wireless table.
func parseWireless(r io.Reader, iface string) (int, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok || name != iface {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 3 {
			return 0, false
		}
		lvl, err := strconv.ParseFloat(strings.TrimSuffix(f[2], "."), 64)
		if err != nil || lvl >= 0 || lvl < -110 {
			return 0, false // not associated, or a driver that reports otherwise
		}
		return int(lvl), true
	}
	return 0, false
}

// signalWords rates a signal level the usual way.
func signalWords(dbm int) string {
	switch {
	case dbm >= -55:
		return "Excellent"
	case dbm >= -65:
		return "Good"
	case dbm >= -72:
		return "Fair"
	case dbm >= -80:
		return "Weak"
	}
	return "Very weak"
}

// wifiUp reports whether wlan0 is up with an address.
func wifiUp() bool {
	b, err := os.ReadFile("/sys/class/net/wlan0/operstate")
	if err != nil {
		return true // not a Miyoo (testing on a computer): assume a network
	}
	return strings.TrimSpace(string(b)) == "up"
}

// fileExists reports whether p can be stat'ed.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
