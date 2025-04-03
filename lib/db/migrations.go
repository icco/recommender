package db

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// TablesToDrop is a list of tables that should be dropped if they exist
var (
	tablesToDrop = []string{
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
	indexesToDrop = []string{
		"idx_animes_title",
		"idx_movies_title",
		"idx_plex_animes_title",
		"idx_plex_tv_shows_title",
		"idx_recommendations_date",
		"idx_tv_shows_title",
	}
)

// RunMigrations runs all database migrations
func RunMigrations(db *gorm.DB, logger *slog.Logger) error {
	ctx := context.Background()

	// Drop old tables
	for _, table := range tablesToDrop {
		if err := dropTableIfExists(ctx, db, table, logger); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	// Drop indexes if it exists
	if err := dropIndexes(ctx, db, logger); err != nil {
		return fmt.Errorf("failed to drop indexes: %w", err)
	}

	return nil
}

// dropMoviesTitleIndex drops the index on movie titles if it exists
func dropIndexes(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
	// Drop unique indexes

	for _, index := range indexesToDrop {
		if err := db.WithContext(ctx).Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
			return fmt.Errorf("failed to drop index %s: %w", index, err)
		} else {
			logger.InfoContext(ctx, "Dropped index", slog.String("index", index))
		}
	}
	return nil
}

// dropTableIfExists drops a table if it exists
func dropTableIfExists(ctx context.Context, db *gorm.DB, tableName string, logger *slog.Logger) error {
	if err := db.WithContext(ctx).Exec("DROP TABLE IF EXISTS " + tableName).Error; err != nil {
		return fmt.Errorf("failed to drop table: %w", err)
	} else {
		logger.Info("Successfully dropped table", slog.String("table", tableName))
	}

	return nil
}
