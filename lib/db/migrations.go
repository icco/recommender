package db

import (
	"context"
	"fmt"
	"log/slog"

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
