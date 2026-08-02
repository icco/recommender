package recommend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/omdb"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	// defaultMetascoreBatch caps OMDb lookups per cache run. OMDb's free tier
	// allows 1000 requests/day and /cron/cache runs hourly, so 40 per run keeps
	// a full library backfill inside the quota. Override with OMDB_BATCH_SIZE.
	defaultMetascoreBatch = 40

	// metascoreTTL is how long a *miss* stays fresh. A title that came back with
	// a score is never re-checked: critic scores are final once a title is
	// released. Only unscored titles are retried, to catch a score that landed
	// after we first looked -- which also keeps the steady-state cost near zero
	// instead of re-sweeping the whole library every TTL.
	metascoreTTL = 90 * 24 * time.Hour

	// metascoreBudget bounds a single enrichment run. /cron/cache and
	// /cron/recommend share one lock and cron.sh only sleeps 120s between them,
	// so a slow OMDb must not hold the lock long enough to starve the day's
	// recommendation run -- one hard-failing lookup alone can burn ~93s in
	// timeouts and retries. Enrichment is resumable, so giving up early just
	// defers those titles to the next run.
	metascoreBudget = 60 * time.Second
)

// metascoreSource fills Metacritic scores on owned Plex titles via OMDb, keyed
// by the IMDb ID cached from Plex GUIDs. Unlike the Trakt and AniList sources it
// writes title metadata rather than ExternalSignal rows, but it implements
// SignalSource so it runs and logs alongside them after each cache update.
type metascoreSource struct {
	db     *gorm.DB
	client *omdb.Client
	batch  int
}

func (s *metascoreSource) Name() string { return models.SourceOMDb }

// Sync enriches up to batch titles that have never been looked up or whose
// lookup has gone stale, oldest first. It returns the number of titles that
// came back with a Metascore.
func (s *metascoreSource) Sync(ctx context.Context) (int, error) {
	l := logging.FromContext(ctx)

	ctx, cancel := context.WithTimeout(ctx, metascoreBudget)
	defer cancel()

	cutoff := time.Now().Add(-metascoreTTL)

	// Each side is queried for the whole batch, then the budget is split. A Plex
	// library holds far more movies than shows, so letting movies claim the
	// batch first would starve TV for the entire movie backfill -- days of runs
	// during which no show gets a score at all.
	movieCands, err := staleMetascoreRows[models.Movie](ctx, s.db, cutoff, s.batch)
	if err != nil {
		return 0, fmt.Errorf("load movies needing metascore: %w", err)
	}
	showCands, err := staleMetascoreRows[models.TVShow](ctx, s.db, cutoff, s.batch)
	if err != nil {
		return 0, fmt.Errorf("load tv shows needing metascore: %w", err)
	}
	movieTake, showTake := splitBatch(len(movieCands), len(showCands), s.batch)
	movies, shows := movieCands[:movieTake], showCands[:showTake]

	scored := 0
	for _, m := range movies {
		ok, err := s.enrich(ctx, "movies", m.ID, m.IMDbID)
		if err != nil {
			// A dead breaker or exhausted quota fails every remaining lookup the
			// same way, so stop rather than burn the batch on certain failures.
			l.Warnw("metascore lookup failed; ending run", "title", m.Title, zap.Error(err))
			return scored, nil
		}
		if ok {
			scored++
		}
	}
	for _, sh := range shows {
		ok, err := s.enrich(ctx, "tv_shows", sh.ID, sh.IMDbID)
		if err != nil {
			l.Warnw("metascore lookup failed; ending run", "title", sh.Title, zap.Error(err))
			return scored, nil
		}
		if ok {
			scored++
		}
	}

	l.Infow("metascore enrichment",
		"movies_checked", len(movies),
		"tvshows_checked", len(shows),
		"scored", scored,
		"batch", s.batch,
	)
	return scored, nil
}

// enrich looks one title up and stamps the row. A title Metacritic does not
// cover still gets MetascoreAt set, so it is not retried until the TTL expires.
// It reports whether a Metascore was found; only unexpected transport-level
// failures return an error.
func (s *metascoreSource) enrich(ctx context.Context, table string, id uint, imdbID string) (bool, error) {
	title, err := s.client.GetByIMDbID(ctx, imdbID)
	switch {
	case errors.Is(err, omdb.ErrNotFound):
		// OMDb has no record; stamp it so the batch moves on to other titles.
		return false, s.stamp(ctx, table, id, nil)
	case err != nil:
		return false, err
	}
	if err := s.stamp(ctx, table, id, title.Metascore); err != nil {
		return false, err
	}
	return title.Metascore != nil, nil
}

// stamp writes the score (possibly nil) and the lookup time.
func (s *metascoreSource) stamp(ctx context.Context, table string, id uint, score *int) error {
	now := time.Now()
	return s.db.WithContext(ctx).Table(table).Where("id = ?", id).
		Updates(map[string]any{"metascore": score, "metascore_at": now}).Error
}

// splitBatch divides a run's lookup budget between pending movies and TV shows.
// Each gets roughly half so both backfill together, and whatever one side leaves
// unused goes to the other so no quota is wasted. Movies win the odd slot,
// since they are the larger share of a library.
func splitBatch(movies, shows, batch int) (movieTake, showTake int) {
	if batch <= 0 {
		return 0, 0
	}
	half := batch / 2
	movieTake = min(movies, batch-half)
	showTake = min(shows, half)

	if rem := batch - movieTake - showTake; rem > 0 {
		if add := min(rem, movies-movieTake); add > 0 {
			movieTake += add
			rem -= add
		}
		if add := min(rem, shows-showTake); add > 0 {
			showTake += add
		}
	}
	return movieTake, showTake
}

// metascoreRow is the projection needed to look a title up and update it.
type metascoreRow struct {
	ID     uint
	Title  string
	IMDbID string `gorm:"column:im_db_id"`
}

// staleMetascoreRows returns up to limit owned titles with an IMDb ID that need
// a lookup: never checked, or checked but unscored and older than cutoff.
// Never-checked rows come first, so a backfill finishes before any re-checking
// starts. A title that already has a score is never returned -- see metascoreTTL.
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

// metascoreMaps returns Metascore lookups for owned movies and TV shows, keyed
// by row ID, for scoring and prompt context.
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
