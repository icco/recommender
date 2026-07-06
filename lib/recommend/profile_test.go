package recommend

import (
	"context"
	"testing"

	"github.com/icco/recommender/models"
)

func TestGenreAffinity_favorsWatchedAndRated(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()

	// Two watched high-rated comedies, one unwatched horror.
	db.Create(&models.Movie{Title: "C1", Genre: "Comedy", Rating: 9, ViewCount: 3, PlexRatingKey: "a"})
	db.Create(&models.Movie{Title: "C2", Genre: "Comedy", Rating: 8, ViewCount: 2, PlexRatingKey: "b"})
	db.Create(&models.Movie{Title: "H1", Genre: "Horror", Rating: 8, ViewCount: 0, PlexRatingKey: "c"})

	aff, err := r.genreAffinity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if aff["Comedy"] <= aff["Horror"] {
		t.Errorf("Comedy affinity (%.2f) should exceed Horror (%.2f)", aff["Comedy"], aff["Horror"])
	}
	if aff["Comedy"] > 1.0 || aff["Comedy"] < 0 {
		t.Errorf("affinity must be normalized 0..1, got %.2f", aff["Comedy"])
	}
}

func TestTasteProfile_nonEmptyWhenSignalsExist(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()
	db.Create(&models.Movie{Title: "C1", Genre: "Comedy", Rating: 9, ViewCount: 3, PlexRatingKey: "a"})
	p, err := r.tasteProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p == "" {
		t.Error("expected a non-empty profile when watched titles exist")
	}
}
