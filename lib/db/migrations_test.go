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
