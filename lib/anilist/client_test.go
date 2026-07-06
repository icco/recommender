package anilist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestList_normalizesScoresAndPicksTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"User":{"mediaListOptions":{"scoreFormat":"POINT_100"}},
			"MediaListCollection":{"lists":[{"entries":[
				{"score":90,"media":{"seasonYear":2019,"title":{"romaji":"Kimetsu","english":"Demon Slayer"}}},
				{"score":0,"media":{"seasonYear":2020,"title":{"romaji":"Unrated","english":null}}}
			]}]}}}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.URL = srv.URL
	entries, err := c.List(context.Background(), "nat")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 rated entry, got %d (%+v)", len(entries), entries)
	}
	if entries[0].Title != "Demon Slayer" || entries[0].Year != 2019 {
		t.Errorf("bad title/year: %+v", entries[0])
	}
	if entries[0].Score < 8.9 || entries[0].Score > 9.1 {
		t.Errorf("POINT_100 90 should normalize to ~9.0, got %.2f", entries[0].Score)
	}
}
