// Package recommend orchestrates daily movie and TV-show recommendation
// generation, combining cached Plex library data with OpenAI-powered
// prompting and result validation.
package recommend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

// StatsData represents statistics about the recommendations database.
type StatsData struct {
	TotalRecommendations        int64
	TotalMovies                 int64
	TotalTVShows                int64
	FirstDate                   time.Time
	LastDate                    time.Time
	AverageDailyRecommendations float64
	GenreDistribution           []struct {
		Genre string
		Count int64
	}
	TotalCachedMovies  int64
	TotalCachedTVShows int64
	LastCacheUpdate    time.Time
}

// Recommender produces and serves daily Plex/TMDb recommendations using
// Gemini. Loggers are taken from per-call ctx via gutil/logging.
type Recommender struct {
	db    *gorm.DB
	plex  *plex.Client
	tmdb  *tmdb.Client
	chat  Chatter
	model string
}

// New creates a new Recommender instance with the provided dependencies.
// Loggers are sourced from per-call ctx via gutil/logging.
func New(db *gorm.DB, plexClient *plex.Client, tmdbClient *tmdb.Client, chat Chatter, model string) (*Recommender, error) {
	return &Recommender{
		db:    db,
		plex:  plexClient,
		tmdb:  tmdbClient,
		chat:  chat,
		model: model,
	}, nil
}

// recommendationUTCDayRange returns [start, end) for the calendar day of t in UTC.
// Cron and HandleHome use UTC midnight for "today"; rows store that same instant in `date`.
func recommendationUTCDayRange(t time.Time) (start, end time.Time) {
	t = t.In(time.UTC)
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = start.Add(24 * time.Hour)
	return start, end
}

// GetRecommendationsForDate retrieves all recommendations for a specific date
func (r *Recommender) GetRecommendationsForDate(ctx context.Context, date time.Time) ([]models.Recommendation, error) {
	var recommendations []models.Recommendation
	start, end := recommendationUTCDayRange(date)
	// Half-open range matches how GORM persists time.Time and avoids SQLite date() quirks
	// on a column named `date`.
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where(`"date" >= ? AND "date" < ?`, start, end).
		Find(&recommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get recommendations: %w", err)
	}
	return recommendations, nil
}

// DidRunToday reports whether a successful generation run exists for the day.
func (r *Recommender) DidRunToday(ctx context.Context, date time.Time) (bool, error) {
	start, end := recommendationUTCDayRange(date)
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.GenerationRun{}).
		Where(`"date" >= ? AND "date" < ? AND status = ?`, start, end, models.RunStatusOK).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check run: %w", err)
	}
	return count > 0, nil
}

// GetRecommendationDates retrieves a paginated list of distinct calendar dates that have recommendations.
func (r *Recommender) GetRecommendationDates(ctx context.Context, page, pageSize int) ([]time.Time, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM recommendations
			GROUP BY strftime('%Y-%m-%d', "date")
		)`).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get total distinct dates: %w", err)
	}

	offset := (page - 1) * pageSize
	var dateRows []struct {
		D string `gorm:"column:d"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT strftime('%Y-%m-%d', "date") AS d FROM recommendations
		GROUP BY strftime('%Y-%m-%d', "date")
		ORDER BY d DESC
		LIMIT ? OFFSET ?`, pageSize, offset).Scan(&dateRows).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get dates: %w", err)
	}

	dateStrs := make([]string, len(dateRows))
	for i, row := range dateRows {
		dateStrs[i] = row.D
	}

	dates := make([]time.Time, 0, len(dateStrs))
	for _, s := range dateStrs {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, 0, fmt.Errorf("parse date %q: %w", s, err)
		}
		dates = append(dates, t.UTC())
	}

	return dates, total, nil
}

// GetStats retrieves statistics about the recommendations database.
// It returns counts of recommendations by type, date range, and genre distribution.
func (r *Recommender) GetStats(ctx context.Context) (*StatsData, error) {
	var stats StatsData

	// Get total recommendations
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Count(&stats.TotalRecommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get total recommendations: %w", err)
	}

	// Get counts by type
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", models.TypeMovie).Count(&stats.TotalMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", models.TypeTVShow).Count(&stats.TotalTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total TV shows: %w", err)
	}

	// Get date range
	var firstDate, lastDate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date ASC").Limit(1).Pluck("date", &firstDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get first date: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date DESC").Limit(1).Pluck("date", &lastDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last date: %w", err)
		}
	}
	stats.FirstDate = firstDate
	stats.LastDate = lastDate

	// Calculate average daily recommendations
	if !firstDate.IsZero() && !lastDate.IsZero() {
		days := lastDate.Sub(firstDate).Hours() / 24
		if days > 0 {
			stats.AverageDailyRecommendations = float64(stats.TotalRecommendations) / days
		}
	}

	// Get genre distribution
	type genreCount struct {
		Genre string
		Count int64
	}
	var genreCounts []genreCount
	if err := r.db.WithContext(ctx).
		Model(&models.Recommendation{}).
		Select("genre, count(*) as count").
		Group("genre").
		Order("count DESC").
		Find(&genreCounts).Error; err != nil {
		return nil, fmt.Errorf("failed to get genre distribution: %w", err)
	}

	stats.GenreDistribution = make([]struct {
		Genre string
		Count int64
	}, len(genreCounts))
	for i, gc := range genreCounts {
		stats.GenreDistribution[i] = struct {
			Genre string
			Count int64
		}{
			Genre: gc.Genre,
			Count: gc.Count,
		}
	}

	// Get cache database statistics
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&stats.TotalCachedMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&stats.TotalCachedTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached TV shows: %w", err)
	}

	// Get last cache update time from the most recent movie or TV show update
	var lastMovieUpdate, lastTVShowUpdate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastMovieUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last movie update: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastTVShowUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last TV show update: %w", err)
		}
	}

	// Use the most recent update time
	if lastMovieUpdate.After(lastTVShowUpdate) {
		stats.LastCacheUpdate = lastMovieUpdate
	} else {
		stats.LastCacheUpdate = lastTVShowUpdate
	}

	return &stats, nil
}
