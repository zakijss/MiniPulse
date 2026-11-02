package main

import "testing"

// MagicDNS short names are on the tailnet too: http://nas:8080 must go
// through MiniScale's proxy, as in every other app.
func TestTailnetShortNames(t *testing.T) {
	for h, want := range map[string]bool{"nas": true, "homeserver": true, "localhost": false, "speed.cloudflare.com": false} {
		if isTailnet(h) != want {
			t.Errorf("isTailnet(%q) != %v", h, want)
		}
	}
}
