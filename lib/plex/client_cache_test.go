package plex

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testPlexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Movie{}, &models.TVShow{}, &models.Recommendation{}); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE movies SET plex_rating_key = 'legacy-' || CAST(id AS TEXT) WHERE plex_rating_key IS NULL OR TRIM(plex_rating_key) = ''`)
	db.Exec(`UPDATE tv_shows SET plex_rating_key = 'legacy-' || CAST(id AS TEXT) WHERE plex_rating_key IS NULL OR TRIM(plex_rating_key) = ''`)
	return db
}

func TestUpsertMovieBatch_updatesSameRow(t *testing.T) {
	db := testPlexDB(t)
	c := &Client{
		plexURL: "http://localhost:32400",
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		db:      db,
	}
	ctx := t.Context()

	v1 := []PlexItem{{RatingKey: "501", Key: "/m/501", Title: "Alpha", Type: "movie", AddedAt: 1}}
	if err := c.upsertMovieBatch(ctx, v1); err != nil {
		t.Fatal(err)
	}
	var id1 uint
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "501").Select("id").Scan(&id1).Error; err != nil || id1 == 0 {
		t.Fatalf("first insert id=%d err=%v", id1, err)
	}

	v2 := []PlexItem{{RatingKey: "501", Key: "/m/501", Title: "Beta", Type: "movie", AddedAt: 2}}
	if err := c.upsertMovieBatch(ctx, v2); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&models.Movie{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("movie count = %d want 1", n)
	}
	row := struct {
		ID    uint
		Title string
	}{}
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "501").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID != id1 || row.Title != "Beta" {
		t.Fatalf("got id=%d title=%q want id=%d Beta", row.ID, row.Title, id1)
	}
}

func TestRemoveMoviesNotInSnapshot_clearsRecFK(t *testing.T) {
	db := testPlexDB(t)
	c := &Client{
		plexURL: "http://localhost:32400",
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		db:      db,
	}
	ctx := t.Context()

	if err := c.upsertMovieBatch(ctx, []PlexItem{
		{RatingKey: "10", Key: "/m/10", Title: "Keep", Type: "movie", AddedAt: 1},
		{RatingKey: "11", Key: "/m/11", Title: "Drop", Type: "movie", AddedAt: 1},
	}); err != nil {
		t.Fatal(err)
	}
	var dropID uint
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "11").Select("id").Scan(&dropID).Error; err != nil || dropID == 0 {
		t.Fatalf("drop movie id=%d err=%v", dropID, err)
	}
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Recommendation{
		Date:  day,
		Title: "Rec", Type: "movie", Year: 2020, Rating: 8, Genre: "x", TMDbID: 1,
		MovieID: &dropID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	present := map[string]struct{}{"10": {}}
	if err := c.removeMoviesNotInSnapshot(ctx, present); err != nil {
		t.Fatal(err)
	}
	var cnt int64
	if err := db.Model(&models.Movie{}).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("movies left = %d want 1", cnt)
	}
	var rec models.Recommendation
	if err := db.Where("title = ?", "Rec").First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if rec.MovieID != nil {
		t.Fatalf("movie_id = %v want nil", rec.MovieID)
	}
}
