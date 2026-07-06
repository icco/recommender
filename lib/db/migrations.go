package db

import (
	"context"
	"fmt"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TablesToDrop is a list of tables that should be dropped if they exist.
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
		"user_preferences",
		"user_ratings",
	}
	indexesToDrop = []string{
		"idx_animes_title",
		"idx_movies_title",
		"idx_movies_title_year", // was unique; conflicts with multiple Plex items same title+year
		"idx_plex_animes_title",
		"idx_plex_tv_shows_title",
		"idx_recommendations_date",
		"idx_tv_shows_title",
		"idx_tvshows_title_year", // same as movies
	}
)

// RunMigrations runs all database migrations.
func RunMigrations(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&models.Movie{}, &models.TVShow{}, &models.Recommendation{},
		&models.GenerationRun{}, &models.ExternalSignal{}, &models.OAuthToken{},
	); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	// Plex cache upserts use unique plex_rating_key; backfill legacy rows before unique conflicts.
	if err := backfillPlexRatingKeys(ctx, db); err != nil {
		return fmt.Errorf("backfill plex_rating_key: %w", err)
	}

	for _, table := range tablesToDrop {
		if err := dropTableIfExists(ctx, db, table); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	if err := dropIndexes(ctx, db); err != nil {
		return fmt.Errorf("failed to drop indexes: %w", err)
	}

	createAdditionalIndexes(ctx, db)

	return nil
}

func backfillPlexRatingKeys(ctx context.Context, db *gorm.DB) error {
	l := logging.FromContext(ctx)
	stmts := []string{
		`UPDATE movies SET plex_rating_key = 'legacy-' || CAST(id AS TEXT) WHERE plex_rating_key IS NULL OR TRIM(plex_rating_key) = ''`,
		`UPDATE tv_shows SET plex_rating_key = 'legacy-' || CAST(id AS TEXT) WHERE plex_rating_key IS NULL OR TRIM(plex_rating_key) = ''`,
	}
	for _, sql := range stmts {
		res := db.WithContext(ctx).Exec(sql)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			l.Infow("Backfilled plex_rating_key on legacy cache rows", "rows", res.RowsAffected)
		}
	}
	return nil
}

// dropIndexes drops the indexes if they exist.
func dropIndexes(ctx context.Context, db *gorm.DB) error {
	l := logging.FromContext(ctx)
	for _, index := range indexesToDrop {
		if err := db.WithContext(ctx).Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
			return fmt.Errorf("failed to drop index %s: %w", index, err)
		}
		l.Infow("Dropped index", "index", index)
	}
	return nil
}

// dropTableIfExists drops a table if it exists.
func dropTableIfExists(ctx context.Context, db *gorm.DB, tableName string) error {
	l := logging.FromContext(ctx)
	if err := db.WithContext(ctx).Exec("DROP TABLE IF EXISTS " + tableName).Error; err != nil {
		return fmt.Errorf("failed to drop table: %w", err)
	}
	l.Infow("Successfully dropped table", "table", tableName)
	return nil
}

// createAdditionalIndexes creates additional indexes for performance.
// Failures to create an individual index are logged but never aborts startup.
func createAdditionalIndexes(ctx context.Context, db *gorm.DB) {
	l := logging.FromContext(ctx)
	additionalIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_movies_title_year ON movies(title, year)",
		"CREATE INDEX IF NOT EXISTS idx_movies_rating_year ON movies(rating, year)",
		"CREATE INDEX IF NOT EXISTS idx_movies_genre_year ON movies(genre, year)",
		"CREATE INDEX IF NOT EXISTS idx_tvshows_title_year ON tv_shows(title, year)",
		"CREATE INDEX IF NOT EXISTS idx_tvshows_rating_year ON tv_shows(rating, year)",
		"CREATE INDEX IF NOT EXISTS idx_tvshows_genre_year ON tv_shows(genre, year)",
		"CREATE INDEX IF NOT EXISTS idx_recommendations_date_type ON recommendations(date, type)",
		"CREATE INDEX IF NOT EXISTS idx_recommendations_rating_year ON recommendations(rating, year)",
		"CREATE INDEX IF NOT EXISTS idx_recommendations_genre_type ON recommendations(genre, type)",
	}

	for _, indexSQL := range additionalIndexes {
		if err := db.WithContext(ctx).Exec(indexSQL).Error; err != nil {
			l.Warnw("Failed to create index", "sql", indexSQL, zap.Error(err))
		} else {
			l.Infow("Successfully created index", "sql", indexSQL)
		}
	}
}
