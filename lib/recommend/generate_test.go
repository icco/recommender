package recommend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"google.golang.org/genai"
)

type fakeChatter struct{ reply string }

func (f fakeChatter) Complete(_ context.Context, _, _ string, _ *genai.Schema) (string, error) {
	return f.reply, nil
}

func TestGenerateRecommendations_endToEnd(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	comedy := models.Movie{Title: "Funny", Year: 2000, Rating: 8, Genre: "Comedy", PosterURL: "p1", PlexRatingKey: "m1"}
	action := models.Movie{Title: "Boom", Year: 2001, Rating: 8, Genre: "Action", PosterURL: "p2", PlexRatingKey: "m2"}
	show := models.TVShow{Title: "Series", Year: 2010, Rating: 8, Genre: "Drama", PosterURL: "p3", ViewCount: 0, PlexRatingKey: "s1"}
	for _, m := range []*models.Movie{&comedy, &action} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&show).Error; err != nil {
		t.Fatal(err)
	}

	reply := fmt.Sprintf(`{"movies":[{"id":%d,"explanation":"lol"},{"id":%d,"explanation":"bang"}],"tvshows":[{"id":%d,"explanation":"gripping"}]}`,
		comedy.ID, action.ID, show.ID)
	r := &Recommender{db: db, chat: fakeChatter{reply: reply}, model: "test"}

	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("generate: %v", err)
	}

	recs, err := r.GetRecommendationsForDate(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d recs, want 3", len(recs))
	}
	var gotExpl bool
	for _, rec := range recs {
		if rec.Explanation != "" {
			gotExpl = true
		}
	}
	if !gotExpl {
		t.Error("expected explanations stored")
	}

	done, err := r.DidRunToday(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("expected a successful GenerationRun")
	}

	// Second call is a no-op (already ran).
	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	recs2, _ := r.GetRecommendationsForDate(ctx, date)
	if len(recs2) != 3 {
		t.Fatalf("rerun changed rec count to %d", len(recs2))
	}
}
