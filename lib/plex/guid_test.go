package plex

import (
	"testing"

	"github.com/LukeHagar/plexgo/models/components"
)

func TestParseGUIDs(t *testing.T) {
	imdb, tmdb, tvdb := parseGUIDs([]string{
		"imdb://tt0133093",
		"tmdb://603",
		"tvdb://12345",
	})
	if imdb != "tt0133093" {
		t.Errorf("imdb = %q, want tt0133093", imdb)
	}
	if tmdb == nil || *tmdb != 603 {
		t.Errorf("tmdb = %v, want 603", tmdb)
	}
	if tvdb != "12345" {
		t.Errorf("tvdb = %q, want 12345", tvdb)
	}
}

func TestParseGUIDs_empty(t *testing.T) {
	imdb, tmdb, tvdb := parseGUIDs(nil)
	if imdb != "" || tmdb != nil || tvdb != "" {
		t.Errorf("expected zero values, got %q %v %q", imdb, tmdb, tvdb)
	}
}

func TestJoinGenres(t *testing.T) {
	got := joinGenres([]components.Tag{{Tag: "Comedy"}, {Tag: "Drama"}, {Tag: "Comedy"}})
	if got != "Comedy, Drama" {
		t.Errorf("joinGenres = %q, want %q", got, "Comedy, Drama")
	}
}
