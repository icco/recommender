package recommend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icco/recommender/lib/dbtest"
	"github.com/icco/recommender/lib/goodreads"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

func bookDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := dbtest.New(t)
	if err := db.AutoMigrate(&models.Book{}, &models.Recommendation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// shelfServer serves a per-shelf RSS feed keyed by the shelf query parameter.
func shelfServer(t *testing.T, byShelf map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		if r.URL.Query().Get("page") == "1" {
			body = byShelf[r.URL.Query().Get("shelf")]
		}
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>%s</channel></rss>`, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func item(id, title, author, shelfExtra string) string {
	return fmt.Sprintf(`<item>
    <title><![CDATA[%s]]></title>
    <book_id>%s</book_id>
    <author_name>%s</author_name>
    <book id="%s"><num_pages>300</num_pages></book>
    %s
  </item>`, title, id, author, id, shelfExtra)
}

func TestGoodreadsSync_storesBothShelvesWithRescaledRating(t *testing.T) {
	db := bookDB(t)
	srv := shelfServer(t, map[string]string{
		goodreads.ShelfRead: item("1", "A Wizard of Earthsea", "Ursula K. Le Guin",
			`<user_rating>5</user_rating><average_rating>4.03</average_rating><book_published>1968</book_published>`),
		goodreads.ShelfToRead: item("2", "Goodbye Chinatown", "Kit Fan",
			`<user_rating>0</user_rating><average_rating>3.50</average_rating>`),
	})

	src := &goodreadsSource{db: db, client: &goodreads.Client{URL: srv.URL}, userID: "12680"}
	n, err := src.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced %d books, want 2", n)
	}

	var read models.Book
	if err := db.Where("goodreads_id = ?", "1").First(&read).Error; err != nil {
		t.Fatalf("load read book: %v", err)
	}
	if read.Shelf != models.ShelfRead {
		t.Errorf("Shelf = %q, want read", read.Shelf)
	}
	if read.UserRating != 5 {
		t.Errorf("UserRating = %d, want 5", read.UserRating)
	}
	// Stored on the 0-10 scale shared with movies and TV.
	if read.Rating != 8.06 {
		t.Errorf("Rating = %v, want 8.06 (4.03 rescaled)", read.Rating)
	}
	if read.Pages != 300 {
		t.Errorf("Pages = %d, want 300", read.Pages)
	}

	var toRead models.Book
	if err := db.Where("goodreads_id = ?", "2").First(&toRead).Error; err != nil {
		t.Fatalf("load to-read book: %v", err)
	}
	if toRead.Shelf != models.ShelfToRead {
		t.Errorf("Shelf = %q, want to-read", toRead.Shelf)
	}
}

// A finished book moves shelves. The upsert must follow it, or the book stays in
// the candidate pool forever.
func TestGoodreadsSync_movesBookBetweenShelves(t *testing.T) {
	db := bookDB(t)
	body := item("1", "Piranesi", "Susanna Clarke", `<average_rating>4.20</average_rating>`)

	srv := shelfServer(t, map[string]string{goodreads.ShelfToRead: body})
	src := &goodreadsSource{db: db, client: &goodreads.Client{URL: srv.URL}, userID: "12680"}
	if _, err := src.Sync(context.Background()); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	moved := shelfServer(t, map[string]string{
		goodreads.ShelfRead: item("1", "Piranesi", "Susanna Clarke",
			`<user_rating>5</user_rating><average_rating>4.20</average_rating>`),
	})
	src.client = &goodreads.Client{URL: moved.URL}
	if _, err := src.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	var count int64
	if err := db.Model(&models.Book{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d rows, want 1 upserted", count)
	}
	var b models.Book
	if err := db.Where("goodreads_id = ?", "1").First(&b).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if b.Shelf != models.ShelfRead {
		t.Errorf("Shelf = %q, want read after the move", b.Shelf)
	}
	if b.UserRating != 5 {
		t.Errorf("UserRating = %d, want 5", b.UserRating)
	}
}

// One unreachable shelf must not discard the other.
func TestGoodreadsSync_partialShelfFailure(t *testing.T) {
	db := bookDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("shelf") == goodreads.ShelfRead {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body := ""
		if r.URL.Query().Get("page") == "1" {
			body = item("2", "Babel", "R.F. Kuang", `<average_rating>4.10</average_rating>`)
		}
		_, _ = fmt.Fprintf(w, `<rss version="2.0"><channel>%s</channel></rss>`, body)
	}))
	defer srv.Close()

	src := &goodreadsSource{db: db, client: &goodreads.Client{URL: srv.URL}, userID: "12680"}
	n, err := src.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 1 {
		t.Errorf("synced %d, want the one reachable shelf", n)
	}
}

// A user id that resolves to nothing must error rather than silently emptying
// the pool.
func TestGoodreadsSync_errorsWhenNothingSynced(t *testing.T) {
	db := bookDB(t)
	srv := shelfServer(t, map[string]string{})
	src := &goodreadsSource{db: db, client: &goodreads.Client{URL: srv.URL}, userID: "99999"}
	if _, err := src.Sync(context.Background()); err == nil {
		t.Fatal("Sync: want error when no books synced, got nil")
	}
}

func seedBooks(t *testing.T, db *gorm.DB, books ...models.Book) {
	t.Helper()
	for i := range books {
		if books[i].GoodreadsID == "" {
			books[i].GoodreadsID = fmt.Sprintf("gr-%d-%s", i, books[i].Title)
		}
		if err := db.Create(&books[i]).Error; err != nil {
			t.Fatalf("seed %q: %v", books[i].Title, err)
		}
	}
}

func TestAuthorAffinity_centersOnThreeStars(t *testing.T) {
	db := bookDB(t)
	seedBooks(t, db,
		models.Book{Title: "Earthsea", Author: "Ursula K. Le Guin", Shelf: models.ShelfRead, UserRating: 5},
		models.Book{Title: "Lathe of Heaven", Author: "Ursula K. Le Guin", Shelf: models.ShelfRead, UserRating: 5},
		models.Book{Title: "Meh", Author: "Middling Author", Shelf: models.ShelfRead, UserRating: 3},
		models.Book{Title: "Bad", Author: "Disliked Author", Shelf: models.ShelfRead, UserRating: 1},
		// Unrated and to-read rows contribute nothing.
		models.Book{Title: "Unrated", Author: "Unknown", Shelf: models.ShelfRead, UserRating: 0},
		models.Book{Title: "Queued", Author: "Queued Author", Shelf: models.ShelfToRead},
	)

	r := &Recommender{db: db}
	aff, err := r.authorAffinity(context.Background())
	if err != nil {
		t.Fatalf("authorAffinity: %v", err)
	}
	if got := aff["ursula k. le guin"]; got != 1.0 {
		t.Errorf("Le Guin affinity = %v, want 1.0 (the peak)", got)
	}
	if got := aff["middling author"]; got != 0 {
		t.Errorf("3-star author affinity = %v, want 0", got)
	}
	if got := aff["disliked author"]; got >= 0 {
		t.Errorf("1-star author affinity = %v, want negative", got)
	}
	if _, ok := aff["queued author"]; ok {
		t.Error("to-read author should not contribute affinity")
	}
	if _, ok := aff["unknown"]; ok {
		t.Error("unrated book should not contribute affinity")
	}
}

func TestLoadBookCandidates_toReadOnlyAndExcludesRecent(t *testing.T) {
	db := bookDB(t)
	seedBooks(t, db,
		models.Book{Title: "Fresh Pick", Author: "Ursula K. Le Guin", Shelf: models.ShelfToRead, Rating: 8.0, Pages: 300},
		models.Book{Title: "Recently Recommended", Author: "Someone", Shelf: models.ShelfToRead, Rating: 9.0},
		models.Book{Title: "Already Read", Author: "Ursula K. Le Guin", Shelf: models.ShelfRead, UserRating: 5},
	)

	var recent models.Book
	if err := db.Where("title = ?", "Recently Recommended").First(&recent).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	date := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Recommendation{
		Date: date.AddDate(0, 0, -3), Title: "Recently Recommended", Type: models.TypeBook,
		BookID: &recent.ID,
	}).Error; err != nil {
		t.Fatalf("seed recommendation: %v", err)
	}

	r := &Recommender{db: db}
	cands, err := r.loadBookCandidates(context.Background(), date, 30)
	if err != nil {
		t.Fatalf("loadBookCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Title != "Fresh Pick" {
		t.Errorf("Title = %q, want Fresh Pick", c.Title)
	}
	if c.Type != models.TypeBook {
		t.Errorf("Type = %q, want book", c.Type)
	}
	if c.Runtime != 300 {
		t.Errorf("Runtime = %d, want the page count 300", c.Runtime)
	}
	// The Le Guin read-shelf 5-star rating is the pool's only signal, so it peaks.
	if c.Affinity != 1.0 {
		t.Errorf("Affinity = %v, want 1.0 from the read-shelf rating", c.Affinity)
	}
	if !strings.Contains(c.SourceURL, "goodreads.com/book/show/") {
		t.Errorf("SourceURL = %q, want a goodreads book link", c.SourceURL)
	}
}

func TestLovedBooksAndFavoriteAuthors(t *testing.T) {
	db := bookDB(t)
	seedBooks(t, db,
		models.Book{Title: "Earthsea", Author: "Ursula K. Le Guin", Shelf: models.ShelfRead, UserRating: 5},
		models.Book{Title: "Fine", Author: "Okay Author", Shelf: models.ShelfRead, UserRating: 3},
	)

	r := &Recommender{db: db}
	loved, err := r.lovedBooks(context.Background())
	if err != nil {
		t.Fatalf("lovedBooks: %v", err)
	}
	if !strings.Contains(loved, "Earthsea by Ursula K. Le Guin") {
		t.Errorf("lovedBooks = %q, want the 5-star book with its author", loved)
	}
	if strings.Contains(loved, "Fine") {
		t.Errorf("lovedBooks = %q, should omit the 3-star book", loved)
	}

	authors, err := r.favoriteAuthors(context.Background())
	if err != nil {
		t.Fatalf("favoriteAuthors: %v", err)
	}
	// Display spelling, not the normalized affinity key.
	if !strings.Contains(authors, "Ursula K. Le Guin") {
		t.Errorf("favoriteAuthors = %q, want the display spelling", authors)
	}
	if strings.Contains(authors, "Okay Author") {
		t.Errorf("favoriteAuthors = %q, should omit the neutral author", authors)
	}
}

// With no books at all, both prompt fragments are empty rather than erroring, so
// the run proceeds without a books tier.
func TestBookPromptFragments_emptyWithoutBooks(t *testing.T) {
	db := bookDB(t)
	r := &Recommender{db: db}
	loved, err := r.lovedBooks(context.Background())
	if err != nil || loved != "" {
		t.Errorf("lovedBooks = %q, %v; want empty, nil", loved, err)
	}
	authors, err := r.favoriteAuthors(context.Background())
	if err != nil || authors != "" {
		t.Errorf("favoriteAuthors = %q, %v; want empty, nil", authors, err)
	}
}
