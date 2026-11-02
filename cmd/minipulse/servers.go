package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zakijss/minideck/tnet"
)

// Server is where a test downloads from and uploads to.
type Server interface {
	// Name is the server as shown on screen and in the history.
	Name() string
	// Locate names where the server is, e.g. "Frankfurt (FRA)".
	Locate(ctx context.Context, c *http.Client) (string, error)
	// PingURL answers quickly with an empty body, for latency probes.
	PingURL() string
	// DownURL serves about bytes of data.
	DownURL(bytes int64) string
	// UpURL accepts a POST body and discards it.
	UpURL() string
}

// cloudflare is Cloudflare's speed test (speed.cloudflare.com), answered by
// the nearest of its data centres. Free, no sign-up, used by many open
// speed tests.
type cloudflare struct{}

const cfBase = "https://speed.cloudflare.com/"

func (cloudflare) Name() string           { return "Cloudflare" }
func (cloudflare) PingURL() string        { return cfBase + "__down?bytes=0" }
func (cloudflare) DownURL(n int64) string { return cfBase + "__down?bytes=" + strconv.FormatInt(n, 10) }
func (cloudflare) UpURL() string          { return cfBase + "__up" }
func (cloudflare) Locate(ctx context.Context, c *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", cfBase+"cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 4096))
	for sc.Scan() {
		if colo, ok := strings.CutPrefix(sc.Text(), "colo="); ok && colo != "" {
			if city := coloCity[colo]; city != "" {
				return city + " (" + colo + ")", nil
			}
			return colo, nil
		}
	}
	return "", errors.New("no location")
}

// The cities of Cloudflare's larger data centres, by airport code; others
// show as the code alone.
var coloCity = map[string]string{
	"YYZ": "Toronto", "YUL": "Montreal", "YVR": "Vancouver", "YYC": "Calgary", "YOW": "Ottawa", "YWG": "Winnipeg",
	"EWR": "Newark", "JFK": "New York", "IAD": "Ashburn", "ORD": "Chicago", "DFW": "Dallas", "ATL": "Atlanta",
	"MIA": "Miami", "LAX": "Los Angeles", "SJC": "San Jose", "SEA": "Seattle", "DEN": "Denver", "BOS": "Boston",
	"PHX": "Phoenix", "DTW": "Detroit", "MSP": "Minneapolis", "IAH": "Houston", "LHR": "London", "MAN": "Manchester",
	"CDG": "Paris", "FRA": "Frankfurt", "AMS": "Amsterdam", "MAD": "Madrid", "MXP": "Milan", "ARN": "Stockholm",
	"WAW": "Warsaw", "DUB": "Dublin", "ZRH": "Zurich", "VIE": "Vienna", "NRT": "Tokyo", "KIX": "Osaka",
	"ICN": "Seoul", "HKG": "Hong Kong", "SIN": "Singapore", "SYD": "Sydney", "MEL": "Melbourne", "BOM": "Mumbai",
	"DEL": "Delhi", "DXB": "Dubai", "GRU": "São Paulo", "MEX": "Mexico City", "JNB": "Johannesburg",
}

// selfHosted is a speed test server you run yourself: LibreSpeed (backend/empty.php,
// backend/garbage.php) or OpenSpeedTest (/downloading, /upload). Address is
// where its web page is, e.g. http://nas.local:8080/.
type selfHosted struct {
	base string // ends in /
	kind string // "librespeed" or "openspeedtest"
}

func (s selfHosted) Name() string {
	if u, err := url.Parse(s.base); err == nil {
		return u.Host
	}
	return s.base
}

func (s selfHosted) Locate(context.Context, *http.Client) (string, error) {
	if s.kind == "openspeedtest" {
		return "OpenSpeedTest", nil
	}
	return "LibreSpeed", nil
}

func (s selfHosted) PingURL() string {
	if s.kind == "openspeedtest" {
		return s.base + "upload?n=" + nonce() // answers a GET with nothing
	}
	return s.base + "backend/empty.php?r=" + nonce()
}

func (s selfHosted) DownURL(n int64) string {
	if s.kind == "openspeedtest" {
		return s.base + "downloading?n=" + nonce()
	}
	mb := max(1, n>>20) // garbage.php counts in megabytes
	return s.base + "backend/garbage.php?ckSize=" + strconv.FormatInt(mb, 10) + "&r=" + nonce()
}

func (s selfHosted) UpURL() string {
	if s.kind == "openspeedtest" {
		return s.base + "upload?n=" + nonce()
	}
	return s.base + "backend/empty.php?r=" + nonce()
}

// nonce is a cache buster: some proxies keep answers to identical GETs.
func nonce() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }

// normalizeServer turns what was typed into a base address.
func normalizeServer(in string) (string, error) {
	u := strings.TrimSpace(in)
	if u == "" {
		return "", errors.New("enter the address of your speed test server")
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") {
		return "", fmt.Errorf("%q is not a web address", in)
	}
	p.RawQuery, p.Fragment = "", ""
	p.Path = strings.TrimSuffix(p.Path, "index.html")
	if !strings.HasSuffix(p.Path, "/") {
		p.Path += "/"
	}
	return p.String(), nil
}

// detect finds out which kind of server answers at base.
func detect(ctx context.Context, c *http.Client, base string) (selfHosted, error) {
	try := func(path string) bool {
		ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if err != nil {
			return false
		}
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		return resp.StatusCode/100 == 2
	}
	switch {
	case try("backend/empty.php"):
		return selfHosted{base, "librespeed"}, nil
	case try("upload"):
		return selfHosted{base, "openspeedtest"}, nil
	}
	return selfHosted{}, fmt.Errorf("no LibreSpeed or OpenSpeedTest server answers at %s", base)
}

// serverTime is how long the server says it spent on a request, from its
// Server-Timing header (dur= in milliseconds, summed over the entries).
// Taking it off the time to the first byte leaves the network's share.
var durRE = regexp.MustCompile(`(?:^|[;,]\s*)dur=([0-9.]+)`)

func serverTime(h http.Header) time.Duration {
	var ms float64
	for _, v := range h.Values("Server-Timing") {
		for _, m := range durRE.FindAllStringSubmatch(v, -1) {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				ms += f
			}
		}
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// newClient makes the HTTP client for tests. Each stream gets its own
// connection; nothing is compressed. Tailnet addresses go through
// MiniScale's HTTP proxy when it runs, since MiniScale works in userspace
// and the system can't route to them.
func newClient() *http.Client {
	tr := &http.Transport{
		Proxy:               tailnetProxy,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		ReadBufferSize:      32 << 10,
		WriteBufferSize:     32 << 10,
		ForceAttemptHTTP2:   false, // one TCP connection per stream, as intended
	}
	return &http.Client{Transport: tr}
}

const tailscaleSock = "/tmp/miniscale/tailscaled.sock"

func tailnetProxy(r *http.Request) (*url.URL, error) {
	if !isTailnet(r.URL.Hostname()) {
		return nil, nil
	}
	if _, err := os.Stat(tailscaleSock); err != nil {
		return nil, nil
	}
	return tnet.HTTPProxyURL(), nil
}

// isTailnet reports whether host is reached through the tailnet: the same
// rule as every other app's (tnet.OnTailnet), MagicDNS short names
// included.
func isTailnet(host string) bool { return tnet.OnTailnet(host) }
