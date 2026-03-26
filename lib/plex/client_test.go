package plex

import "testing"

func TestClient_resolvePosterURL(t *testing.T) {
	c := &Client{plexURL: "https://plex.example.com:32400"}

	if got := c.resolvePosterURL(""); got != fallbackPosterURL {
		t.Fatalf("empty: %q", got)
	}
	if got := c.resolvePosterURL("https://cdn.example/p.jpg"); got != "https://cdn.example/p.jpg" {
		t.Fatalf("absolute: %q", got)
	}
	if got := c.resolvePosterURL("/library/metadata/1/thumb/abc"); got != "https://plex.example.com:32400/library/metadata/1/thumb/abc" {
		t.Fatalf("relative slash: %q", got)
	}
	if got := c.resolvePosterURL("library/foo"); got != "https://plex.example.com:32400/library/foo" {
		t.Fatalf("relative no slash: %q", got)
	}
}
