package plex

import (
	"encoding/json"
	"testing"

	"github.com/LukeHagar/plexgo/models/components"
)

func TestSectionMetadata_tolerantGuid(t *testing.T) {
	// Array form (Plex with includeGuids=1): extract ids.
	var arr sectionListMetadata
	if err := json.Unmarshal([]byte(`{"ratingKey":"1","Guid":[{"id":"imdb://tt1"},{"id":"tmdb://2"}]}`), &arr); err != nil {
		t.Fatalf("array guid: %v", err)
	}
	if got := sectionMetadataToPlexItem(arr).Guids; len(got) != 2 {
		t.Errorf("array guids = %v, want 2", got)
	}
	// String form: Go's case-insensitive matching binds Plex's lowercase `guid`
	// string to this field when the `Guid` array is absent. Must not error.
	var str sectionListMetadata
	if err := json.Unmarshal([]byte(`{"ratingKey":"2","Guid":"plex://movie/abc"}`), &str); err != nil {
		t.Fatalf("string guid should not error: %v", err)
	}
	// Absent.
	var none sectionListMetadata
	if err := json.Unmarshal([]byte(`{"ratingKey":"3"}`), &none); err != nil {
		t.Fatalf("absent guid: %v", err)
	}
}

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
