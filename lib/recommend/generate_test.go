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

func TestGenerateRecommendations_includesBookTier(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	date := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	movie := models.Movie{Title: "Funny", Year: 2000, Rating: 8, Genre: "Comedy", PlexRatingKey: "m1"}
	if err := db.Create(&movie).Error; err != nil {
		t.Fatal(err)
	}
	// Same title as the movie, to prove (date, type, title) keeps both.
	book := models.Book{GoodreadsID: "gr1", Title: "Funny", Author: "Susanna Clarke",
		Shelf: models.ShelfToRead, Rating: 8.4, Pages: 245}
	if err := db.Create(&book).Error; err != nil {
		t.Fatal(err)
	}

	reply := fmt.Sprintf(`{"movies":[{"id":%d,"explanation":"lol"}],"tvshows":[],"books":[{"id":%d,"explanation":"strange and lovely"}]}`,
		movie.ID, book.ID)
	r := &Recommender{db: db, chat: fakeChatter{reply: reply}, model: "test"}

	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("generate: %v", err)
	}

	recs, err := r.GetRecommendationsForDate(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d recs, want a movie and a book sharing the title", len(recs))
	}

	var bookRec *models.Recommendation
	for i := range recs {
		if recs[i].Type == models.TypeBook {
			bookRec = &recs[i]
		}
	}
	if bookRec == nil {
		t.Fatal("no book recommendation stored")
	}
	if bookRec.BookID == nil || *bookRec.BookID != book.ID {
		t.Errorf("BookID = %v, want %d", bookRec.BookID, book.ID)
	}
	if bookRec.Author != "Susanna Clarke" {
		t.Errorf("Author = %q, want Susanna Clarke", bookRec.Author)
	}
	if bookRec.Runtime != 245 {
		t.Errorf("Runtime = %d, want the page count 245", bookRec.Runtime)
	}
	if bookRec.Explanation != "strange and lovely" {
		t.Errorf("Explanation = %q", bookRec.Explanation)
	}
	if got := bookRec.GoodreadsRating(); got != 4.2 {
		t.Errorf("GoodreadsRating = %v, want 4.2", got)
	}

	var run models.GenerationRun
	if err := db.Where("status = ?", models.RunStatusOK).First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.BookCount != 1 || run.MovieCount != 1 || run.TVShowCount != 0 {
		t.Errorf("counts = movies %d, tv %d, books %d; want 1, 0, 1",
			run.MovieCount, run.TVShowCount, run.BookCount)
	}
}

// With no books shelved, the run still produces the screen tiers.
func TestGenerateRecommendations_withoutBooks(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	date := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

	movie := models.Movie{Title: "Solo", Year: 2000, Rating: 8, Genre: "Comedy", PlexRatingKey: "m1"}
	if err := db.Create(&movie).Error; err != nil {
		t.Fatal(err)
	}

	reply := fmt.Sprintf(`{"movies":[{"id":%d,"explanation":"lol"}],"tvshows":[],"books":[]}`, movie.ID)
	r := &Recommender{db: db, chat: fakeChatter{reply: reply}, model: "test"}
	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("generate: %v", err)
	}
	recs, err := r.GetRecommendationsForDate(ctx, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Type != models.TypeMovie {
		t.Fatalf("got %+v, want just the movie", recs)
	}
}
