// Package dbtest provides an isolated Postgres database for tests.
//
// Each call to New creates a fresh, uniquely named schema on the Postgres
// instance identified by DATABASE_URL (defaulting to a local dev instance),
// scopes the connection to it, and drops it on cleanup. This gives every test
// the same isolation the old in-memory SQLite setup provided while exercising
// the same Postgres dialect the service runs against in production.
package dbtest

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// defaultDSN matches the Postgres service wired up in .github/workflows/test.yml
// and the docker-compose dev database, so tests run with no extra config locally.
const defaultDSN = "postgres://postgres:postgres@localhost:5432/recommender_test?sslmode=disable"

// New returns a *gorm.DB scoped to a private schema for the duration of the
// test. It does not run migrations; callers migrate the models (or invoke
// db.RunMigrations) they need.
func New(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("connect to test Postgres (%s): %v", dsn, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	// Pin a single connection so the session-level search_path set below is
	// retained for every query the test issues, and never expires mid-test.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)

	schema := schemaName(t)
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatalf("set search_path %s: %v", schema, err)
	}

	t.Cleanup(func() {
		if err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
		_ = sqlDB.Close()
	})

	return db
}

// schemaName derives a unique, valid Postgres identifier from the test name.
func schemaName(t *testing.T) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	// Identifiers cap at 63 bytes; leave room for the random suffix.
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("test_%s_%d", safe, rand.Intn(1_000_000)) //nolint:gosec // test-only schema name, not security-sensitive
}
