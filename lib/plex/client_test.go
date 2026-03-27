package plex

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testPlexClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	return NewClient(srvURL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
}

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

func TestGetAllLibraries_minimalJSON(t *testing.T) {
	t.Parallel()
	const payload = `{"MediaContainer":{"allowSync":true,"size":1,"Directory":[{"key":"1","title":"Movies","type":"movie","hidden":false,"language":"en","uuid":"u1"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" {
			t.Error("expected X-Plex-Token header")
		}
		if !strings.HasSuffix(r.URL.Path, "/library/sections/all") {
			t.Errorf("expected /library/sections/all, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := testPlexClient(t, srv.URL)
	dir, err := c.GetAllLibraries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 1 {
		t.Fatalf("Directory len=%d want 1", len(dir))
	}
	if dir[0].Key == nil || *dir[0].Key != "1" {
		t.Fatalf("Key=%v", dir[0].Key)
	}
	if dir[0].Type != "movie" {
		t.Fatalf("Type=%q", dir[0].Type)
	}
}

func TestGetAllLibraries_ignoresNumericBoolsOnSection(t *testing.T) {
	t.Parallel()
	// Newer PMS can send 0/1 for flags; plexgo's *bool models reject that — we skip those keys.
	const payload = `{"MediaContainer":{"allowSync":1,"size":1,"Directory":[{"key":"2","title":"TV","type":"show","content":1,"directory":0,"filters":1,"hidden":0,"language":"en","uuid":"u2"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := testPlexClient(t, srv.URL)
	dir, err := c.GetAllLibraries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) != 1 || dir[0].Type != "show" {
		t.Fatalf("got %+v", dir)
	}
}

func TestGetPlexItems_viaPlexgoListContent(t *testing.T) {
	t.Parallel()
	const payload = `{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[{"ratingKey":"42","key":"/library/metadata/42","title":"Test Film","type":"movie","addedAt":1,"year":2020}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" {
			t.Error("expected X-Plex-Token header")
		}
		if !strings.HasSuffix(r.URL.Path, "/all") {
			t.Errorf("expected path to end with /all, got %q", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "/library/sections/7/") {
			t.Errorf("expected section path, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("X-Plex-Container-Start") != "0" {
			t.Errorf("X-Plex-Container-Start=%q", r.URL.Query().Get("X-Plex-Container-Start"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := testPlexClient(t, srv.URL)
	items, err := c.GetPlexItems(t.Context(), "7", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items)=%d want 1", len(items))
	}
	if items[0].Title != "Test Film" || items[0].Type != "movie" {
		t.Fatalf("%+v", items[0])
	}
}
