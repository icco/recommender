package omdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseScore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"84", 84, true},
		{"0", 0, true},
		{"100", 100, true},
		{" 61 ", 61, true},
		{"N/A", 0, false},
		{"n/a", 0, false},
		{"", 0, false},
		{"101", 0, false},
		{"-1", 0, false},
		{"eight", 0, false},
	}
	for _, c := range cases {
		got, ok := parseScore(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseScore(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestParseYear(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"1999":      1999,
		"2010–2015": 2010, // en dash range
		"2010-2015": 2010,
		"2010–":     2010, // still running
		"N/A":       0,
		"":          0,
	}
	for in, want := range cases {
		if got := parseYear(in); got != want {
			t.Errorf("parseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMetascorePrefersTopLevelField(t *testing.T) {
	t.Parallel()
	r := &apiResponse{Metascore: "73"}
	r.Ratings = append(r.Ratings, struct {
		Source string `json:"Source"`
		Value  string `json:"Value"`
	}{Source: "Metacritic", Value: "40/100"})

	got := r.metascore()
	if got == nil || *got != 73 {
		t.Fatalf("metascore() = %v, want 73", got)
	}
}

func TestMetascoreFallsBackToRatings(t *testing.T) {
	t.Parallel()
	r := &apiResponse{Metascore: "N/A"}
	r.Ratings = append(r.Ratings, struct {
		Source string `json:"Source"`
		Value  string `json:"Value"`
	}{Source: "Internet Movie Database", Value: "8.7/10"})
	r.Ratings = append(r.Ratings, struct {
		Source string `json:"Source"`
		Value  string `json:"Value"`
	}{Source: "Metacritic", Value: "94/100"})

	got := r.metascore()
	if got == nil || *got != 94 {
		t.Fatalf("metascore() = %v, want 94", got)
	}
}

func TestMetascoreAbsentIsNil(t *testing.T) {
	t.Parallel()
	r := &apiResponse{Metascore: "N/A"}
	if got := r.metascore(); got != nil {
		t.Fatalf("metascore() = %v, want nil", *got)
	}
}

func TestGetByIMDbIDSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("i"); got != "tt0133093" {
			t.Errorf("imdb id = %q, want tt0133093", got)
		}
		if got := r.URL.Query().Get("apikey"); got != "secret" {
			t.Errorf("apikey = %q, want secret", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Title":"The Matrix","Year":"1999","Type":"movie","Metascore":"73","Response":"True"}`))
	}))
	defer srv.Close()

	c := NewClient("secret")
	c.BaseURL = srv.URL

	got, err := c.GetByIMDbID(context.Background(), "tt0133093")
	if err != nil {
		t.Fatalf("GetByIMDbID: %v", err)
	}
	if got.Title != "The Matrix" || got.Year != 1999 || got.Type != "movie" {
		t.Errorf("got %+v", got)
	}
	if got.Metascore == nil || *got.Metascore != 73 {
		t.Errorf("Metascore = %v, want 73", got.Metascore)
	}
}

// OMDb reports a missing title with HTTP 200 and Response:"False", which must
// not be retried or mistaken for a successful empty result.
func TestGetByIMDbIDNotFound(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"Response":"False","Error":"Incorrect IMDb ID."}`))
	}))
	defer srv.Close()

	c := NewClient("secret")
	c.BaseURL = srv.URL

	_, err := c.GetByIMDbID(context.Background(), "tt0000000")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on not-found)", calls)
	}
}

// A series with no Metacritic coverage must come back with a nil Metascore
// rather than a zero, so callers can tell "unscored" from "scored 0".
func TestGetByIMDbIDSeriesWithoutMetascore(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Title":"Some Show","Year":"2010–2015","Type":"series","Metascore":"N/A","Ratings":[],"Response":"True"}`))
	}))
	defer srv.Close()

	c := NewClient("secret")
	c.BaseURL = srv.URL

	got, err := c.GetByIMDbID(context.Background(), "tt1234567")
	if err != nil {
		t.Fatalf("GetByIMDbID: %v", err)
	}
	if got.Metascore != nil {
		t.Errorf("Metascore = %d, want nil", *got.Metascore)
	}
	if got.Year != 2010 {
		t.Errorf("Year = %d, want 2010", got.Year)
	}
}

func TestGetByIMDbIDEmptyID(t *testing.T) {
	t.Parallel()
	c := NewClient("secret")
	if _, err := c.GetByIMDbID(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty imdb id")
	}
}

// The api key must never reach an error string, since those are logged.
func TestErrorsDoNotLeakAPIKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"Response":"False","Error":"Invalid API key!"}`))
	}))
	defer srv.Close()

	c := NewClient("super-secret-key")
	c.BaseURL = srv.URL

	_, err := c.GetByIMDbID(context.Background(), "tt0133093")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Errorf("error leaked api key: %v", err)
	}
}
