package recommend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/omdb"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	// OMDb's free tier is 1000/day and /cron/cache runs hourly.
	defaultMetascoreBatch = 40

	// How long a miss stays fresh. Scored titles are never re-checked.
	metascoreTTL = 90 * 24 * time.Hour

	// Bounds a run: /cron/cache and /cron/recommend share a lock and cron.sh
	// sleeps only 120s between them. One failing lookup can burn ~93s.
	metascoreBudget = 60 * time.Second
)

// metascoreSource fills Metacritic scores via OMDb, keyed by Plex's IMDb GUID.
// It implements SignalSource but writes title metadata, not ExternalSignal rows.
type metascoreSource struct {
	db     *gorm.DB
	client *omdb.Client
	batch  int
}

func (s *metascoreSource) Name() string { return models.SourceOMDb }

// Sync enriches up to batch titles, never-checked first. Returns how many
// came back with a Metascore.
func (s *metascoreSource) Sync(ctx context.Context) (int, error) {
	l := logging.FromContext(ctx)

	ctx, cancel := context.WithTimeout(ctx, metascoreBudget)
	defer cancel()

	cutoff := time.Now().Add(-metascoreTTL)

	// Movies only: OMDb has no Metacritic data for TV (verified — 0 of 12 series
	// scored, by title or id; season/episode endpoints have no score field).
	// TV ratings come from Plex audienceRating instead.
	movies, err := staleMetascoreRows[models.Movie](ctx, s.db, cutoff, s.batch)
	if err != nil {
		return 0, fmt.Errorf("load movies needing metascore: %w", err)
	}

	scored := 0
	for _, m := range movies {
		ok, err := s.enrich(ctx, m.ID, m.IMDbID)
		if err != nil {
			// A dead breaker or spent quota fails every remaining lookup alike.
			l.Warnw("metascore lookup failed; ending run", "title", m.Title, zap.Error(err))
			return scored, nil
		}
		if ok {
			scored++
		}
	}
	l.Infow("metascore enrichment",
		"movies_checked", len(movies),
		"scored", scored,
		"batch", s.batch,
	)
	return scored, nil
}

// enrich looks one title up and stamps the row. Uncovered titles are stamped
// too, so they wait out the TTL instead of being retried every run.
func (s *metascoreSource) enrich(ctx context.Context, id uint, imdbID string) (bool, error) {
	title, err := s.client.GetByIMDbID(ctx, imdbID)
	switch {
	case errors.Is(err, omdb.ErrNotFound):
		return false, s.stamp(ctx, id, nil)
	case err != nil:
		return false, err
	}
	if err := s.stamp(ctx, id, title.Metascore); err != nil {
		return false, err
	}
	return title.Metascore != nil, nil
}

// stamp writes the score (possibly nil) and the lookup time.
func (s *metascoreSource) stamp(ctx context.Context, id uint, score *int) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Movie{}).Where("id = ?", id).
		Updates(map[string]any{"metascore": score, "metascore_at": now}).Error
}

// metascoreRow is the projection needed to look a title up and update it.
type metascoreRow struct {
	ID     uint
	Title  string
	IMDbID string `gorm:"column:im_db_id"`
}

// staleMetascoreRows returns up to limit titles needing a lookup: never checked,
// or unscored and older than cutoff. Never-checked first; scored rows never.
func staleMetascoreRows[T models.Movie | models.TVShow](ctx context.Context, db *gorm.DB, cutoff time.Time, limit int) ([]metascoreRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []metascoreRow
	var model T
	err := db.WithContext(ctx).Model(&model).
		Select("id", "title", "im_db_id").
		Where(`im_db_id <> '' AND metascore IS NULL AND (metascore_at IS NULL OR metascore_at < ?)`, cutoff).
		Order("metascore_at ASC NULLS FIRST").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// metascoreMaps returns Metascores keyed by row ID, for scoring and prompts.
func (r *Recommender) metascoreMaps(ctx context.Context) (movies, tvshows map[uint]int, err error) {
	load := func(table string) (map[uint]int, error) {
		var rows []struct {
			ID        uint
			Metascore int
		}
		if err := r.db.WithContext(ctx).Table(table).
			Select("id", "metascore").
			Where("metascore IS NOT NULL").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make(map[uint]int, len(rows))
		for _, row := range rows {
			out[row.ID] = row.Metascore
		}
		return out, nil
	}

	movies, err = load("movies")
	if err != nil {
		return nil, nil, fmt.Errorf("load movie metascores: %w", err)
	}
	tvshows, err = load("tv_shows")
	if err != nil {
		return nil, nil, fmt.Errorf("load tv show metascores: %w", err)
	}
	return movies, tvshows, nil
}
