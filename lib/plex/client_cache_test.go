package plex

import (
	"testing"
	"time"

	"github.com/icco/recommender/lib/dbtest"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

func testPlexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := dbtest.New(t)
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
		db:      db,
	}
	ctx := t.Context()

	v1 := []Item{{RatingKey: "501", Key: "/m/501", Title: "Alpha", Type: models.TypeMovie, AddedAt: 1}}
	if err := c.upsertMovieBatch(ctx, v1); err != nil {
		t.Fatal(err)
	}
	var id1 uint
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "501").Select("id").Scan(&id1).Error; err != nil || id1 == 0 {
		t.Fatalf("first insert id=%d err=%v", id1, err)
	}

	v2 := []Item{{RatingKey: "501", Key: "/m/501", Title: "Beta", Type: models.TypeMovie, AddedAt: 2}}
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

// TestUpsertMovieBatch_reassignedRatingKey covers Plex reusing a tmdb_id
// under a new rating key, which used to fail the whole batch.
func TestUpsertMovieBatch_reassignedRatingKey(t *testing.T) {
	db := testPlexDB(t)
	c := &Client{
		plexURL: "http://localhost:32400",
		db:      db,
	}
	ctx := t.Context()

	v1 := []Item{{RatingKey: "501", Key: "/m/501", Title: "Alpha", Type: models.TypeMovie, AddedAt: 1, Guids: []string{"tmdb://603"}}}
	if err := c.upsertMovieBatch(ctx, v1); err != nil {
		t.Fatal(err)
	}
	var id1 uint
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "501").Select("id").Scan(&id1).Error; err != nil || id1 == 0 {
		t.Fatalf("first insert id=%d err=%v", id1, err)
	}

	v2 := []Item{{RatingKey: "999", Key: "/m/999", Title: "Alpha Remastered", Type: models.TypeMovie, AddedAt: 2, Guids: []string{"tmdb://603"}}}
	if err := c.upsertMovieBatch(ctx, v2); err != nil {
		t.Fatalf("upsert with reassigned rating key: %v", err)
	}

	var n int64
	if err := db.Model(&models.Movie{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("movie count = %d want 1", n)
	}
	row := struct {
		ID            uint
		Title         string
		PlexRatingKey string
	}{}
	if err := db.Model(&models.Movie{}).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID != id1 || row.Title != "Alpha Remastered" || row.PlexRatingKey != "999" {
		t.Fatalf("got %+v, want id=%d title=Alpha Remastered plex_rating_key=999", row, id1)
	}
}

// TestUpsertTVShowBatch_reassignedRatingKey mirrors
// TestUpsertMovieBatch_reassignedRatingKey for the TV show upsert path.
func TestUpsertTVShowBatch_reassignedRatingKey(t *testing.T) {
	db := testPlexDB(t)
	c := &Client{
		plexURL: "http://localhost:32400",
		db:      db,
	}
	ctx := t.Context()

	v1 := []Item{{RatingKey: "701", Key: "/s/701", Title: "Gamma", Type: models.TypeTVShow, AddedAt: 1, Guids: []string{"tmdb://1399"}}}
	if err := c.upsertTVShowBatch(ctx, v1); err != nil {
		t.Fatal(err)
	}
	var id1 uint
	if err := db.Model(&models.TVShow{}).Where("plex_rating_key = ?", "701").Select("id").Scan(&id1).Error; err != nil || id1 == 0 {
		t.Fatalf("first insert id=%d err=%v", id1, err)
	}

	v2 := []Item{{RatingKey: "888", Key: "/s/888", Title: "Gamma Remastered", Type: models.TypeTVShow, AddedAt: 2, Guids: []string{"tmdb://1399"}}}
	if err := c.upsertTVShowBatch(ctx, v2); err != nil {
		t.Fatalf("upsert with reassigned rating key: %v", err)
	}

	var n int64
	if err := db.Model(&models.TVShow{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("tv show count = %d want 1", n)
	}
	row := struct {
		ID            uint
		Title         string
		PlexRatingKey string
	}{}
	if err := db.Model(&models.TVShow{}).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID != id1 || row.Title != "Gamma Remastered" || row.PlexRatingKey != "888" {
		t.Fatalf("got %+v, want id=%d title=\"Gamma Remastered\" plex_rating_key=888", row, id1)
	}
}

func TestRemoveMoviesNotInSnapshot_clearsRecFK(t *testing.T) {
	db := testPlexDB(t)
	c := &Client{
		plexURL: "http://localhost:32400",
		db:      db,
	}
	ctx := t.Context()

	if err := c.upsertMovieBatch(ctx, []Item{
		{RatingKey: "10", Key: "/m/10", Title: "Keep", Type: models.TypeMovie, AddedAt: 1},
		{RatingKey: "11", Key: "/m/11", Title: "Drop", Type: models.TypeMovie, AddedAt: 1},
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
		Title: "Rec", Type: models.TypeMovie, Year: 2020, Rating: 8, Genre: "x", TMDbID: 1,
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
