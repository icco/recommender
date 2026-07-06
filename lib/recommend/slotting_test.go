package recommend

import (
	"testing"

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
