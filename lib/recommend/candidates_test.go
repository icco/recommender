package recommend

import (
	"testing"
	"time"

	"github.com/icco/recommender/models"
)

func mkCand(id uint, rating float64, view int) candidate {
	return candidate{ID: id, Type: "movie", Title: "T", Rating: rating, ViewCount: view}
}

func TestScoreCandidate_ratingAndNovelty(t *testing.T) {
	unwatched := scoreCandidate(mkCand(1, 8.0, 0))
	watched := scoreCandidate(mkCand(2, 8.0, 3))
	if unwatched <= watched {
		t.Errorf("unwatched (%.2f) should outscore watched (%.2f)", unwatched, watched)
	}
	high := scoreCandidate(mkCand(3, 9.0, 0))
	low := scoreCandidate(mkCand(4, 4.0, 0))
	if high <= low {
		t.Errorf("higher rating should score higher: %.2f vs %.2f", high, low)
	}
}

func TestDateSeed_stableAndDistinct(t *testing.T) {
	d1 := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	d1b := time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC) // same calendar day
	d2 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	if dateSeed(d1) != dateSeed(d1b) {
		t.Error("same UTC day must yield same seed")
	}
	if dateSeed(d1) == dateSeed(d2) {
		t.Error("different days must yield different seeds")
	}
}

func TestBuildShortlist_deterministicPerDayAndVaries(t *testing.T) {
	var cands []candidate
	for i := uint(1); i <= 200; i++ {
		cands = append(cands, mkCand(i, 5.0+float64(i%5), 0))
	}
	d1 := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)

	a := buildShortlist(cands, d1, 120, 40)
	b := buildShortlist(cands, d1, 120, 40)
	if len(a) != 40 {
		t.Fatalf("shortlist len = %d, want 40", len(a))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("same day must produce identical shortlist")
		}
	}
	c := buildShortlist(cands, d2, 120, 40)
	same := true
	for i := range a {
		if a[i].ID != c[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Error("different days should produce a different order")
	}
}

func TestLoadCandidates_excludesRecentAndWatchedTV(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()
	today := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	m1 := models.Movie{Title: "Keep", Year: 2000, Rating: 8, PlexRatingKey: "k1"}
	m2 := models.Movie{Title: "RecentlyRecd", Year: 2001, Rating: 8, PlexRatingKey: "k2"}
	if err := db.Create(&m1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&m2).Error; err != nil {
		t.Fatal(err)
	}
	watched := models.TVShow{Title: "Seen", Year: 2010, ViewCount: 5, PlexRatingKey: "t1"}
	unwatched := models.TVShow{Title: "Fresh", Year: 2011, ViewCount: 0, PlexRatingKey: "t2"}
	if err := db.Create(&watched).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&unwatched).Error; err != nil {
		t.Fatal(err)
	}

	rec := models.Recommendation{Date: today.AddDate(0, 0, -3), Title: "RecentlyRecd", Type: models.TypeMovie, Year: 2001, MovieID: &m2.ID, TMDbID: 1}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}

	movies, tv, err := r.loadCandidates(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || movies[0].Title != "Keep" {
		t.Errorf("movies = %+v, want only Keep", movies)
	}
	if len(tv) != 1 || tv[0].Title != "Fresh" {
		t.Errorf("tv = %+v, want only Fresh", tv)
	}
}

func TestScoreCandidate_watchlistBoost(t *testing.T) {
	base := mkCand(1, 7.0, 0)
	boosted := base
	boosted.Watchlisted = true
	if scoreCandidate(boosted) <= scoreCandidate(base) {
		t.Error("watchlisted candidate should score higher")
	}
}

func TestLoadCandidates_externalWatched(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()
	today := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	movie := models.Movie{Title: "M", Year: 2000, Rating: 8, ViewCount: 0, PlexRatingKey: "m1"}
	show := models.TVShow{Title: "S", Year: 2001, Rating: 8, ViewCount: 0, PlexRatingKey: "s1"}
	db.Create(&movie)
	db.Create(&show)
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "watched:m", Kind: models.SignalKindWatched, MovieID: &movie.ID, Value: 1})
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "watched:s", Kind: models.SignalKindWatched, TVShowID: &show.ID, Value: 1})

	movies, tv, err := r.loadCandidates(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(tv) != 0 {
		t.Errorf("externally-watched TV should be excluded, got %d", len(tv))
	}
	if len(movies) != 1 || movies[0].ViewCount == 0 {
		t.Errorf("externally-watched movie should be treated as watched: %+v", movies)
	}
}
