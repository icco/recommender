package recommend

import (
	"strings"
	"testing"
	"time"

	"github.com/icco/recommender/models"
)

func intPtr(n int) *int { return &n }

func TestMetascoreBoost(t *testing.T) {
	t.Parallel()

	// An unscored title must land exactly on neutral, not below it: most TV
	// shows have no Metascore, and penalizing them would bias every TV slot.
	if got := metascoreBoost(nil); got != 0 {
		t.Errorf("metascoreBoost(nil) = %v, want 0", got)
	}
	if got := metascoreBoost(intPtr(int(metascoreNeutral))); got != 0 {
		t.Errorf("metascoreBoost(neutral) = %v, want 0", got)
	}
	if got := metascoreBoost(intPtr(95)); got <= 0 {
		t.Errorf("metascoreBoost(95) = %v, want > 0", got)
	}
	if got := metascoreBoost(intPtr(20)); got >= 0 {
		t.Errorf("metascoreBoost(20) = %v, want < 0", got)
	}

	// The term must stay small enough that it nudges rather than dominates.
	if got := metascoreBoost(intPtr(100)); got > watchlistBoost {
		t.Errorf("metascoreBoost(100) = %v, want <= watchlistBoost (%v)", got, watchlistBoost)
	}
}

func TestScoreCandidateUsesMetascore(t *testing.T) {
	t.Parallel()
	base := candidate{Rating: 7.0, ViewCount: 0}

	unscored := scoreCandidate(base)

	acclaimed := base
	acclaimed.Metascore = intPtr(90)
	panned := base
	panned.Metascore = intPtr(20)

	if scoreCandidate(acclaimed) <= unscored {
		t.Error("a well-reviewed title should outrank an unscored one")
	}
	if scoreCandidate(panned) >= unscored {
		t.Error("a poorly-reviewed title should rank below an unscored one")
	}
}

func TestFormatShortlistIncludesMetascore(t *testing.T) {
	t.Parallel()
	out := formatShortlist([]candidate{
		{ID: 1, Title: "Scored", Year: 1999, Rating: 8.2, Genres: []string{"Drama"}, Metascore: intPtr(73)},
		{ID: 2, Title: "Unscored", Year: 2001, Rating: 7.1, Genres: []string{"Comedy"}},
	})

	if !strings.Contains(out, "Metacritic: 73") {
		t.Errorf("scored title missing Metacritic in prompt line:\n%s", out)
	}
	// The unscored line must say nothing at all, so the model does not read a
	// coverage gap as a bad review.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "Unscored") && strings.Contains(line, "Metacritic") {
			t.Errorf("unscored title should omit Metacritic entirely: %s", line)
		}
	}
}

func TestToRecCarriesMetacriticLink(t *testing.T) {
	t.Parallel()
	date := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	movie := toRec(candidate{
		ID: 1, Type: models.TypeMovie, Title: "The Matrix", Year: 1999, Metascore: intPtr(73),
	}, "why", date)
	if movie.MetascoreValue() != 73 {
		t.Errorf("Metascore = %d, want 73", movie.MetascoreValue())
	}
	if want := "https://www.metacritic.com/movie/the-matrix/"; movie.MetacriticURL != want {
		t.Errorf("MetacriticURL = %q, want %q", movie.MetacriticURL, want)
	}

	show := toRec(candidate{
		ID: 2, Type: models.TypeTVShow, Title: "Severance", Year: 2022, Metascore: intPtr(83),
	}, "why", date)
	if want := "https://www.metacritic.com/tv/severance/"; show.MetacriticURL != want {
		t.Errorf("MetacriticURL = %q, want %q", show.MetacriticURL, want)
	}
}

// Without a Metascore there is no evidence Metacritic covers the title, so no
// link is published rather than a guessed one that may 404.
func TestToRecOmitsLinkWithoutMetascore(t *testing.T) {
	t.Parallel()
	rec := toRec(candidate{
		ID: 1, Type: models.TypeMovie, Title: "Obscure Home Video", Year: 1984,
	}, "why", time.Now())

	if rec.HasMetascore() {
		t.Error("HasMetascore() = true, want false")
	}
	if rec.MetacriticURL != "" {
		t.Errorf("MetacriticURL = %q, want empty", rec.MetacriticURL)
	}
}

func TestSplitBatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                  string
		movies, shows, batch  int
		wantMovies, wantShows int
	}{
		// The case that matters: a huge movie backlog must not shut TV out.
		{"large movie backlog", 3860, 590, 40, 20, 20},
		{"no shows pending", 100, 0, 40, 40, 0},
		{"no movies pending", 0, 100, 40, 0, 40},
		{"shows nearly done", 100, 3, 40, 37, 3},
		{"both under budget", 5, 4, 40, 5, 4},
		{"nothing pending", 0, 0, 40, 0, 0},
		{"odd batch favors movies", 10, 10, 5, 3, 2},
		{"zero batch", 10, 10, 0, 0, 0},
	}
	for _, c := range cases {
		gotM, gotS := splitBatch(c.movies, c.shows, c.batch)
		if gotM != c.wantMovies || gotS != c.wantShows {
			t.Errorf("%s: splitBatch(%d, %d, %d) = (%d, %d), want (%d, %d)",
				c.name, c.movies, c.shows, c.batch, gotM, gotS, c.wantMovies, c.wantShows)
		}
		if total := gotM + gotS; total > c.batch {
			t.Errorf("%s: took %d, over the batch cap of %d", c.name, total, c.batch)
		}
	}
}
