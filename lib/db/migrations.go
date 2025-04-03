package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

// TablesToDrop is a list of tables that should be dropped if they exist
var (
	tablesToDrop = []string{
		"anime_items",
		"animes",
		"old_recommendations",
		"plex_anime",
		"plex_cache",
		"plex_cache_anime",
		"plex_cache_movies",
		"plex_cache_tvshows",
		"plex_items",
		"plex_movies",
		"plex_tv_shows",
		"plex_tvshows",
		"recommendation_anime",
		"recommendation_movies",
		"recommendation_tvshows",
		"recommendations",
		"user_preferences",
		"user_ratings",
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

	// Auto-migrate the schema first to ensure tables exist
	if err := db.AutoMigrate(&models.Movie{}, &models.TVShow{}, &models.Recommendation{}); err != nil {
		slog.Error("Failed to migrate database", slog.Any("error", err))
		os.Exit(1)
	}

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

// dropIndexes drops the indexes if they exist
func dropIndexes(ctx context.Context, db *gorm.DB, logger *slog.Logger) error {
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
