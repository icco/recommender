package recommend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	poolSize      = 240
	shortlistSize = 80
	targetMovies  = 4
	targetTVShows = 3
)

type promptData struct {
	TargetMovies  int
	TargetTVShows int
	Profile       string
	Movies        string
	TVShows       string
}

// GenerateRecommendations builds the day's recommendations from the cached Plex
// library using Gemini to pick from a scored shortlist. It records a
// GenerationRun and is a no-op if a successful run already exists for the day.
func (r *Recommender) GenerateRecommendations(ctx context.Context, date time.Time) error {
	l := logging.FromContext(ctx)
	start := time.Now()

	done, err := r.DidRunToday(ctx, date)
	if err != nil {
		return err
	}
	if done {
		l.Infow("Recommendations already generated for date", "date", date)
		return nil
	}

	movies, tvshows, err := r.loadCandidates(ctx, date)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}
	if len(movies) == 0 && len(tvshows) == 0 {
		err := fmt.Errorf("no eligible candidates; run /cron/cache first")
		return r.recordRun(ctx, date, 0, 0, err)
	}

	movieShortlist := buildShortlist(movies, date, poolSize, shortlistSize)
	tvShortlist := buildShortlist(tvshows, date, poolSize, shortlistSize)

	system, user, err := r.renderPrompts(ctx, movieShortlist, tvShortlist)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}

	raw, err := r.chat.Complete(ctx, system, user, pickSchema())
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, fmt.Errorf("gemini: %w", err))
	}

	pr, err := parsePickResponse(raw)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}

	combined := append([]candidate{}, movieShortlist...)
	combined = append(combined, tvShortlist...)
	recs := selectMovies(pr.Movies, combined, targetMovies)
	recs = append(recs, selectTVShows(pr.TVShows, combined, targetTVShows)...)
	if len(recs) == 0 {
		return r.recordRun(ctx, date, 0, 0, fmt.Errorf("no recommendations selected"))
	}

	for i := range recs {
		recs[i].Date = date
		r.fillPoster(ctx, &recs[i])
	}

	movieCount, tvCount := 0, 0
	for _, rec := range recs {
		if rec.Type == models.TypeMovie {
			movieCount++
		} else {
			tvCount++
		}
	}

	if err := r.saveRecommendations(ctx, date, recs); err != nil {
		return r.recordRun(ctx, date, movieCount, tvCount, err)
	}

	if err := r.recordRun(ctx, date, movieCount, tvCount, nil); err != nil {
		return err
	}
	l.Infow("Generated recommendations", "movies", movieCount, "tvshows", tvCount, "duration", time.Since(start))
	return nil
}

func (r *Recommender) renderPrompts(ctx context.Context, movies, tvshows []candidate) (system, user string, err error) {
	sysTmpl, err := prompts.FS.ReadFile("system.txt")
	if err != nil {
		return "", "", fmt.Errorf("read system prompt: %w", err)
	}
	userTmplBytes, err := prompts.FS.ReadFile("recommendation.txt")
	if err != nil {
		return "", "", fmt.Errorf("read user prompt: %w", err)
	}
	userTmpl, err := template.New("rec").Parse(string(userTmplBytes))
	if err != nil {
		return "", "", fmt.Errorf("parse user prompt: %w", err)
	}
	profile, err := r.tasteProfile(ctx) // Phase 2; returns "" until Task 8
	if err != nil {
		logging.FromContext(ctx).Warnw("taste profile failed; continuing without", zap.Error(err))
		profile = ""
	}
	var b strings.Builder
	if err := userTmpl.Execute(&b, promptData{
		TargetMovies: targetMovies, TargetTVShows: targetTVShows, Profile: profile,
		Movies: formatShortlist(movies), TVShows: formatShortlist(tvshows),
	}); err != nil {
		return "", "", fmt.Errorf("execute user prompt: %w", err)
	}
	return string(sysTmpl), b.String(), nil
}

// fillPoster lazily fetches a TMDb poster only when one is missing. Bounded to the
// finalist set, so at most a handful of calls per run.
func (r *Recommender) fillPoster(ctx context.Context, rec *models.Recommendation) {
	if rec.PosterURL != "" || r.tmdb == nil {
		return
	}
	l := logging.FromContext(ctx)
	switch rec.Type {
	case models.TypeMovie:
		res, err := r.tmdb.SearchMovie(ctx, rec.Title, rec.Year)
		if err != nil {
			if !errors.Is(err, tmdb.ErrCircuitOpen) {
				l.Warnw("poster fill (movie) failed", "title", rec.Title, zap.Error(err))
			}
			return
		}
		if len(res.Results) > 0 {
			rec.PosterURL = r.tmdb.GetPosterURL(res.Results[0].PosterPath)
		}
	case models.TypeTVShow:
		res, err := r.tmdb.SearchTVShow(ctx, rec.Title, rec.Year)
		if err != nil {
			if !errors.Is(err, tmdb.ErrCircuitOpen) {
				l.Warnw("poster fill (tv) failed", "title", rec.Title, zap.Error(err))
			}
			return
		}
		if len(res.Results) > 0 {
			rec.PosterURL = r.tmdb.GetPosterURL(res.Results[0].PosterPath)
		}
	}
}

func (r *Recommender) saveRecommendations(ctx context.Context, date time.Time, recs []models.Recommendation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(`"date" = ?`, date).Delete(&models.Recommendation{}).Error; err != nil {
			return fmt.Errorf("clear existing recs: %w", err)
		}
		// The (date, title) unique index rejects two Plex items with the same title
		// on one day; skip in-batch title collisions rather than fail the run.
		seen := make(map[string]bool, len(recs))
		for i := range recs {
			if seen[recs[i].Title] {
				continue
			}
			seen[recs[i].Title] = true
			if err := tx.Create(&recs[i]).Error; err != nil {
				return fmt.Errorf("create rec %q: %w", recs[i].Title, err)
			}
		}
		return nil
	})
}

func (r *Recommender) recordRun(ctx context.Context, date time.Time, movieCount, tvCount int, genErr error) error {
	run := models.GenerationRun{
		Date: date, Status: models.RunStatusOK, MovieCount: movieCount,
		TVShowCount: tvCount, Model: r.model,
	}
	if genErr != nil {
		run.Status = models.RunStatusError
		run.Error = genErr.Error()
	}
	if err := r.db.WithContext(ctx).Create(&run).Error; err != nil {
		return fmt.Errorf("record run: %w", errors.Join(err, genErr))
	}
	return genErr
}
