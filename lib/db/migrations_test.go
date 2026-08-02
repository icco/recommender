package db

import (
	"testing"
	"time"

	"github.com/icco/recommender/lib/dbtest"
	"github.com/icco/recommender/models"
)

func TestRunMigrations_createsNewTables(t *testing.T) {
	gdb := dbtest.New(t)
	if err := RunMigrations(t.Context(), gdb); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if !gdb.Migrator().HasTable(&models.GenerationRun{}) {
		t.Fatal("generation_runs table missing")
	}
	if !gdb.Migrator().HasTable(&models.ExternalSignal{}) {
		t.Fatal("external_signals table missing")
	}
	if !gdb.Migrator().HasTable(&models.OAuthToken{}) {
		t.Fatal("oauth_tokens table missing")
	}
	run := models.GenerationRun{Date: time.Now().UTC().Truncate(24 * time.Hour), Status: models.RunStatusOK, MovieCount: 4}
	if err := gdb.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("expected assigned ID")
	}
}

func TestRunMigrations_acceptsBookRecommendations(t *testing.T) {
	gdb := dbtest.New(t)
	if err := RunMigrations(t.Context(), gdb); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if !gdb.Migrator().HasTable(&models.Book{}) {
		t.Fatal("books table missing")
	}
	rec := models.Recommendation{
		Date: time.Now().UTC().Truncate(24 * time.Hour), Title: "Piranesi",
		Type: models.TypeBook, Year: 2020, Author: "Susanna Clarke",
	}
	if err := gdb.Create(&rec).Error; err != nil {
		t.Fatalf("create book recommendation: %v", err)
	}
}

// Simulates a database migrated before books existed: the old CHECK rejects
// 'book', and AutoMigrate alone never replaces it.
func TestRunMigrations_widensPreexistingTypeCheck(t *testing.T) {
	gdb := dbtest.New(t)
	if err := gdb.AutoMigrate(&models.Recommendation{}); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	for _, sql := range []string{
		`ALTER TABLE recommendations DROP CONSTRAINT IF EXISTS chk_recommendations_type`,
		`ALTER TABLE recommendations ADD CONSTRAINT chk_recommendations_type CHECK (type IN ('movie', 'tvshow'))`,
	} {
		if err := gdb.Exec(sql).Error; err != nil {
			t.Fatalf("seed old constraint: %v", err)
		}
	}

	date := time.Now().UTC().Truncate(24 * time.Hour)
	old := models.Recommendation{Date: date, Title: "Rejected", Type: models.TypeBook}
	if err := gdb.Create(&old).Error; err == nil {
		t.Fatal("want the pre-books constraint to reject a book, got nil")
	}

	if err := RunMigrations(t.Context(), gdb); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if err := gdb.Create(&models.Recommendation{Date: date, Title: "Accepted", Type: models.TypeBook}).Error; err != nil {
		t.Fatalf("create book after widening: %v", err)
	}
	// The widened constraint must still reject a bogus type.
	if err := gdb.Create(&models.Recommendation{Date: date, Title: "Bogus", Type: "podcast"}).Error; err == nil {
		t.Error("want an unknown type rejected, got nil")
	}
}

// A book and a film sharing a title must both survive the same day.
func TestRunMigrations_titleUniquenessIsPerType(t *testing.T) {
	gdb := dbtest.New(t)
	if err := RunMigrations(t.Context(), gdb); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	date := time.Now().UTC().Truncate(24 * time.Hour)
	if err := gdb.Create(&models.Recommendation{Date: date, Title: "Dune", Type: models.TypeMovie, Year: 2021}).Error; err != nil {
		t.Fatalf("create movie: %v", err)
	}
	if err := gdb.Create(&models.Recommendation{Date: date, Title: "Dune", Type: models.TypeBook, Year: 1965}).Error; err != nil {
		t.Fatalf("create book with the same title: %v", err)
	}
	// Same type and title on one day is still a conflict.
	if err := gdb.Create(&models.Recommendation{Date: date, Title: "Dune", Type: models.TypeBook, Year: 1965}).Error; err == nil {
		t.Error("want a duplicate (date, type, title) rejected, got nil")
	}
}
