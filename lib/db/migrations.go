package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

// TablesToDrop is a list of tables that should be dropped if they exist
var TablesToDrop = []string{
	"old_recommendations", // Example of an old table to drop
	"plex_items",          // Old table used for Plex cache
	"anime_items",         // Old table used for Anilist cache
	"plex_movies",         // Old table for Plex movies
	"plex_anime",          // Old table for Plex anime
	"plex_tvshows",        // Old table for Plex TV shows
	"plex_cache_movies",   // Old cache table for movies
	"plex_cache_anime",    // Old cache table for anime
	"plex_cache_tvshows",  // Old cache table for TV shows
	"plex_cache",          // Old general cache table
	"user_preferences",    // Old user preferences table
	"user_ratings",        // Old user ratings table
}

// RunMigrations runs all database migrations
func RunMigrations(db *gorm.DB, logger *slog.Logger) error {
	ctx := context.Background()

	// Drop old tables
	for _, table := range TablesToDrop {
		if err := dropTableIfExists(ctx, db, table, logger); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	// Fix movie title index
	if err := fixMovieTitleIndex(ctx, db, logger); err != nil {
		return fmt.Errorf("failed to fix movie title index: %w", err)
	}

	// Recreate recommendations table
	if err := recreateRecommendationsTable(ctx, db, logger); err != nil {
		return fmt.Errorf("failed to recreate recommendations table: %w", err)
	}

	return nil
}

// dropTableIfExists drops a table if it exists
func dropTableIfExists(ctx context.Context, db *gorm.DB, tableName string, logger *slog.Logger) error {
	// Check if table exists
	var count int64
	if err := db.WithContext(ctx).Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&count).Error; err != nil {
		return fmt.Errorf("failed to check if table exists: %w", err)
	}

	if count > 0 {
		logger.Info("Dropping old table", slog.String("table", tableName))
		if err := db.WithContext(ctx).Exec("DROP TABLE " + tableName).Error; err != nil {
			return fmt.Errorf("failed to drop table: %w", err)
		}
		logger.Info("Successfully dropped table", slog.String("table", tableName))
	} else {
		logger.Debug("Table does not exist, skipping drop", slog.String("table", tableName))
	}

	return nil
}

// fixMovieTitleIndex drops the unique index on movie titles and creates a regular index
func fixMovieTitleIndex(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
	// Check if unique index exists
	var count int64
	if err := db.WithContext(ctx).Raw("SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_movies_title'").Scan(&count).Error; err != nil {
		return fmt.Errorf("failed to check if index exists: %w", err)
	}

	if count > 0 {
		logger.Info("Dropping unique index on movie titles")
		if err := db.WithContext(ctx).Exec("DROP INDEX idx_movies_title").Error; err != nil {
			return fmt.Errorf("failed to drop unique index: %w", err)
		}
		logger.Info("Successfully dropped unique index")
	}

	// Create regular index if it doesn't exist
	if err := db.WithContext(ctx).Exec("CREATE INDEX IF NOT EXISTS idx_movies_title ON movies(title)").Error; err != nil {
		return fmt.Errorf("failed to create regular index: %w", err)
	}
	logger.Info("Successfully created regular index on movie titles")

	return nil
}

// recreateRecommendationsTable drops and recreates the recommendations table
func recreateRecommendationsTable(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
	logger.InfoContext(ctx, "Recreating recommendations table")

	// Drop unique indexes
	indexesToDrop := []string{
		"idx_recommendations_date",
		"idx_tv_shows_title",
		"idx_animes_title",
		"idx_plex_animes_title",
		"idx_plex_tv_shows_title",
	}

	for _, index := range indexesToDrop {
		if err := db.WithContext(ctx).Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
			return fmt.Errorf("failed to drop index %s: %w", index, err)
		}
		logger.InfoContext(ctx, "Dropped index", slog.String("index", index))
	}

	logger.InfoContext(ctx, "Successfully recreated recommendations table")
	return nil
}
