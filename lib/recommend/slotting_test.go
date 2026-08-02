package recommend

import (
	"testing"
	"time"

	"github.com/icco/recommender/models"
)

func cand(id uint, view int, genres ...string) candidate {
	return candidate{ID: id, Type: models.TypeMovie, Title: "t", Genres: genres, ViewCount: view, Rating: 7}
}

func TestParsePickResponse_ok(t *testing.T) {
	raw := `{"movies":[{"id":5,"explanation":"funny"}],"tvshows":[{"id":9,"explanation":"good"}]}`
	pr, err := parsePickResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Movies) != 1 || pr.Movies[0].ID != 5 || pr.Movies[0].Explanation != "funny" {
		t.Errorf("bad movies parse: %+v", pr.Movies)
	}
}

func TestSelectMovies_ignoresUnknownIDsAndFillsRoles(t *testing.T) {
	shortlist := []candidate{
		cand(1, 0, "Comedy"),
		cand(2, 0, "Action"),
		cand(3, 4, "Drama"), // watched -> eligible for rewatch slot
		cand(4, 0, "Horror"),
	}
	picks := []pick{
		{ID: 1, Explanation: "funny"},
		{ID: 999, Explanation: "hallucinated"}, // unknown -> ignored
		{ID: 2, Explanation: "action"},
		{ID: 3, Explanation: "rewatch"},
		{ID: 4, Explanation: "extra"},
	}
	recs := selectMovies(picks, shortlist, 4)
	if len(recs) != 4 {
		t.Fatalf("got %d movies, want 4", len(recs))
	}
	ids := map[uint]bool{}
	for _, r := range recs {
		if r.MovieID != nil {
			ids[*r.MovieID] = true
		}
	}
	if ids[999] {
		t.Error("hallucinated ID must not appear")
	}
}

func TestSelectMovies_rewatchRequiresWatched(t *testing.T) {
	// Only unwatched titles available: rewatch slot cannot be filled by a watched
	// title, but the target count is still met by padding.
	shortlist := []candidate{cand(1, 0, "Comedy"), cand(2, 0, "Action"), cand(3, 0, "Drama")}
	picks := []pick{{ID: 1}, {ID: 2}, {ID: 3}}
	recs := selectMovies(picks, shortlist, 4)
	if len(recs) != 3 {
		t.Fatalf("got %d, want 3 (only three candidates exist)", len(recs))
	}
	for _, r := range recs {
		c := findCand(shortlist, *r.MovieID)
		if c.ViewCount != 0 {
			t.Error("no watched candidate exists; none should be selected as watched")
		}
	}
}

func findCand(cs []candidate, id uint) candidate {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return candidate{}
}

func TestSelectBooks_ignoresOtherTypesAndPads(t *testing.T) {
	shortlist := []candidate{
		{ID: 1, Type: models.TypeMovie, Title: "A Movie"},
		{ID: 2, Type: models.TypeBook, Title: "Piranesi", Author: "Susanna Clarke", Runtime: 245},
		{ID: 3, Type: models.TypeBook, Title: "Babel", Author: "R.F. Kuang"},
		{ID: 4, Type: models.TypeBook, Title: "Padded In"},
	}
	// The movie id and an unknown id are both ignored; the third slot pads.
	picks := []pick{{ID: 1, Explanation: "wrong type"}, {ID: 99}, {ID: 3, Explanation: "picked"}}

	got := selectBooks(picks, shortlist, 3)
	if len(got) != 3 {
		t.Fatalf("got %d books, want 3", len(got))
	}
	if got[0].Title != "Babel" || got[0].Explanation != "picked" {
		t.Errorf("first = %q/%q, want Babel/picked", got[0].Title, got[0].Explanation)
	}
	for _, rec := range got {
		if rec.Type != models.TypeBook {
			t.Errorf("%q has type %q, want book", rec.Title, rec.Type)
		}
		if rec.BookID == nil {
			t.Errorf("%q has nil BookID", rec.Title)
		}
		if rec.MovieID != nil || rec.TVShowID != nil {
			t.Errorf("%q set a screen-tier FK", rec.Title)
		}
	}
	if got[0].Author != "R.F. Kuang" {
		t.Errorf("Author = %q, want R.F. Kuang", got[0].Author)
	}
}

// Metacritic has no book section, so a book must never get a metacritic.com link
// even if a Metascore somehow reaches it.
func TestToRec_bookNeverGetsMetacriticURL(t *testing.T) {
	score := 90
	rec := toRec(candidate{ID: 1, Type: models.TypeBook, Title: "Dune", Metascore: &score}, "", time.Time{})
	if rec.MetacriticURL != "" {
		t.Errorf("MetacriticURL = %q, want empty for a book", rec.MetacriticURL)
	}
	movie := toRec(candidate{ID: 2, Type: models.TypeMovie, Title: "Dune", Metascore: &score}, "", time.Time{})
	if movie.MetacriticURL == "" {
		t.Error("movie with a Metascore should still get a MetacriticURL")
	}
}

func TestParsePickResponse_books(t *testing.T) {
	pr, err := parsePickResponse(`{"movies":[],"tvshows":[],"books":[{"id":7,"explanation":"why"}]}`)
	if err != nil {
		t.Fatalf("parsePickResponse: %v", err)
	}
	if len(pr.Books) != 1 || pr.Books[0].ID != 7 || pr.Books[0].Explanation != "why" {
		t.Errorf("Books = %+v", pr.Books)
	}
}
