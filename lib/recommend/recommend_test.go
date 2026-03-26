package recommend

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Recommendation{}, &models.Movie{}, &models.TVShow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func testRecommender(db *gorm.DB) *Recommender {
	return &Recommender{
		db:     db,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cache:  make(map[string]*CacheEntry),
	}
}

func TestGetRecommendationDates_distinctDaysAndPagination(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()

	day1 := time.Date(2025, 3, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 3, 11, 8, 0, 0, 0, time.UTC)

	for _, title := range []string{"M1", "M2"} {
		if err := db.Create(&models.Recommendation{
			Date: day1, Title: title, Type: "movie", Year: 2020,
			Rating: 8, Genre: "Comedy", TMDbID: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.Recommendation{
		Date: day2, Title: "M3", Type: "movie", Year: 2021,
		Rating: 7, Genre: "Drama", TMDbID: 2,
	}).Error; err != nil {
		t.Fatal(err)
	}

	total, err := distinctDateCount(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("distinct date count = %d, want 2", total)
	}

	dates, n, err := r.GetRecommendationDates(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("total = %d, want 2", n)
	}
	if len(dates) != 2 {
		t.Fatalf("len(dates) = %d, want 2", len(dates))
	}
	// Newest first
	if !dates[0].Truncate(24*time.Hour).Equal(day2.Truncate(24 * time.Hour)) {
		t.Fatalf("first date = %v, want %v", dates[0], day2)
	}

	datesP2, n2, err := r.GetRecommendationDates(ctx, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 2 {
		t.Fatalf("page2 total = %d, want 2", n2)
	}
	if len(datesP2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(datesP2))
	}
}

func distinctDateCount(ctx context.Context, db *gorm.DB) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM (SELECT 1 FROM recommendations GROUP BY date(date))").Scan(&n).Error
	return n, err
}

func TestCheckRecommendationsExist_partialDay(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()

	if err := db.Create(&models.Movie{Title: "LibMovie", Year: 2020, Rating: 8, Genre: "Action"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.TVShow{Title: "LibShow", Year: 2019, Rating: 8, Genre: "Drama", Seasons: 3}).Error; err != nil {
		t.Fatal(err)
	}

	day := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Recommendation{
		Date: day, Title: "OnlyMovie", Type: "movie", Year: 2020,
		Rating: 8, Genre: "Comedy", TMDbID: 10,
	}).Error; err != nil {
		t.Fatal(err)
	}

	exists, err := r.CheckRecommendationsExist(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected incomplete day to report exists=false so cron can regenerate")
	}

	complete, err := r.CheckRecommendationsComplete(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("expected day with only movie rec to be incomplete when both library types exist")
	}
}
