package recommend

import (
	"context"
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// testGenreComedy is the shared fixture genre value used across recommendation
// tests; centralized so we don't sprinkle the same literal everywhere.
const testGenreComedy = "Comedy"

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Unique named in-memory DB per test so shared-cache state never leaks across tests.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(
		&models.Recommendation{}, &models.Movie{}, &models.TVShow{},
		&models.GenerationRun{}, &models.ExternalSignal{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func testRecommender(db *gorm.DB) *Recommender {
	return &Recommender{db: db}
}

func TestGetRecommendationDates_distinctDaysAndPagination(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()

	day1 := time.Date(2025, 3, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 3, 11, 8, 0, 0, 0, time.UTC)

	for _, title := range []string{"M1", "M2"} {
		if err := db.Create(&models.Recommendation{
			Date: day1, Title: title, Type: models.TypeMovie, Year: 2020,
			Rating: 8, Genre: testGenreComedy, TMDbID: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.Recommendation{
		Date: day2, Title: "M3", Type: models.TypeMovie, Year: 2021,
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
	if !dates[0].Truncate(24 * time.Hour).Equal(day2.Truncate(24 * time.Hour)) {
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

func TestGetRecommendationsForDate_sameUTCCalendarDay(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()

	stored := time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&models.Recommendation{
		Date: stored, Title: "Abbott Elementary", Type: models.TypeTVShow, Year: 2021,
		Rating: 0, Genre: testGenreComedy, TMDbID: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// Same calendar day in UTC but not midnight — should still match stored rows.
	queryDay := time.Date(2026, 3, 27, 18, 0, 0, 0, time.UTC)
	recs, err := r.GetRecommendationsForDate(ctx, queryDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Title != "Abbott Elementary" {
		t.Fatalf("got %+v", recs)
	}
}

func distinctDateCount(ctx context.Context, db *gorm.DB) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM recommendations
			GROUP BY strftime('%Y-%m-%d', "date")
		)`).Scan(&n).Error
	return n, err
}

func TestDidRunToday(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()
	day := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)

	// No run yet.
	done, err := r.DidRunToday(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("expected no successful run initially")
	}

	// An error run does not count as done.
	if err := db.Create(&models.GenerationRun{Date: day, Status: models.RunStatusError}).Error; err != nil {
		t.Fatal(err)
	}
	done, err = r.DidRunToday(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("an error run must not count as done")
	}

	// A successful run counts.
	if err := db.Create(&models.GenerationRun{Date: day, Status: models.RunStatusOK, MovieCount: 4}).Error; err != nil {
		t.Fatal(err)
	}
	done, err = r.DidRunToday(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("expected done after a successful run")
	}
}
