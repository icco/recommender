package recommend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	poolSize      = 240
	shortlistSize = 80
	targetMovies  = 4
	targetTVShows = 3
	targetBooks   = 3

	// recentExclusionDays keeps a title out of the pool for this many days after
	// it was last recommended.
	recentExclusionDays = 30
)

type promptData struct {
	TargetMovies  int
	TargetTVShows int
	TargetBooks   int
	Profile       string
	Loved         string
	Authors       string
	LovedBooks    string
	Movies        string
	TVShows       string
	Books         string
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
		return r.recordRun(ctx, date, counts{}, err)
	}
	// Books are optional: the tier stays empty unless GOODREADS_USER_ID is set and
	// a shelf sync has run, and an unreachable Goodreads must not sink the day's
	// movie and TV recommendations.
	books, err := r.loadBookCandidates(ctx, date, recentExclusionDays)
	if err != nil {
		l.Warnw("book candidates failed; continuing without books", zap.Error(err))
		books = nil
	}
	if len(movies) == 0 && len(tvshows) == 0 && len(books) == 0 {
		err := fmt.Errorf("no eligible candidates; run /cron/cache first")
		return r.recordRun(ctx, date, counts{}, err)
	}

	movieShortlist := buildShortlist(movies, date, poolSize, shortlistSize)
	tvShortlist := buildShortlist(tvshows, date, poolSize, shortlistSize)
	bookShortlist := buildShortlist(books, date, poolSize, shortlistSize)

	system, user, err := r.renderPrompts(ctx, movieShortlist, tvShortlist, bookShortlist)
	if err != nil {
		return r.recordRun(ctx, date, counts{}, err)
	}

	raw, err := r.chat.Complete(ctx, system, user, pickSchema())
	if err != nil {
		return r.recordRun(ctx, date, counts{}, fmt.Errorf("gemini: %w", err))
	}

	pr, err := parsePickResponse(raw)
	if err != nil {
		return r.recordRun(ctx, date, counts{}, err)
	}

	combined := append([]candidate{}, movieShortlist...)
	combined = append(combined, tvShortlist...)
	combined = append(combined, bookShortlist...)
	recs := selectMovies(pr.Movies, combined, targetMovies)
	recs = append(recs, selectTVShows(pr.TVShows, combined, targetTVShows)...)
	recs = append(recs, selectBooks(pr.Books, combined, targetBooks)...)
	if len(recs) == 0 {
		return r.recordRun(ctx, date, counts{}, fmt.Errorf("no recommendations selected"))
	}

	for i := range recs {
		recs[i].Date = date
		r.cachePoster(ctx, &recs[i])
	}

	c := tally(recs)

	if err := r.saveRecommendations(ctx, date, recs); err != nil {
		return r.recordRun(ctx, date, c, err)
	}

	if err := r.recordRun(ctx, date, c, nil); err != nil {
		return err
	}
	l.Infow("Generated recommendations", "movies", c.movies, "tvshows", c.tvshows,
		"books", c.books, "duration", time.Since(start))
	return nil
}

// counts is the per-tier tally recorded on a GenerationRun.
type counts struct {
	movies  int
	tvshows int
	books   int
}

// tally counts recommendations by tier. It switches on Type explicitly rather
// than treating "not a movie" as TV, which would silently file books as TV shows.
func tally(recs []models.Recommendation) counts {
	var c counts
	for _, rec := range recs {
		switch rec.Type {
		case models.TypeMovie:
			c.movies++
		case models.TypeTVShow:
			c.tvshows++
		case models.TypeBook:
			c.books++
		}
	}
	return c
}

func (r *Recommender) renderPrompts(ctx context.Context, movies, tvshows, books []candidate) (system, user string, err error) {
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
	profile, err := r.tasteProfile(ctx)
	if err != nil {
		logging.FromContext(ctx).Warnw("taste profile failed; continuing without", zap.Error(err))
		profile = ""
	}
	loved, err := r.lovedTitles(ctx)
	if err != nil {
		logging.FromContext(ctx).Warnw("loved titles failed; continuing without", zap.Error(err))
		loved = ""
	}
	// Book taste context is best-effort for the same reason the book pool is:
	// a Goodreads problem should cost the books tier, not the whole run.
	authors, err := r.favoriteAuthors(ctx)
	if err != nil {
		logging.FromContext(ctx).Warnw("favorite authors failed; continuing without", zap.Error(err))
		authors = ""
	}
	lovedBooks, err := r.lovedBooks(ctx)
	if err != nil {
		logging.FromContext(ctx).Warnw("loved books failed; continuing without", zap.Error(err))
		lovedBooks = ""
	}
	var b strings.Builder
	if err := userTmpl.Execute(&b, promptData{
		// TargetBooks collapses to 0 with an empty shelf, which drops the books
		// section from the prompt rather than asking for picks from nothing.
		TargetMovies: targetMovies, TargetTVShows: targetTVShows, TargetBooks: min(len(books), targetBooks),
		Profile: profile, Loved: loved, Authors: authors, LovedBooks: lovedBooks,
		Movies: formatShortlist(movies), TVShows: formatShortlist(tvshows),
		Books: formatBookShortlist(books),
	}); err != nil {
		return "", "", fmt.Errorf("execute user prompt: %w", err)
	}
	return string(sysTmpl), b.String(), nil
}

// cachePoster downloads the finalist's Plex poster into the local poster dir and
// rewrites PosterURL to a public /posters/ path the web page can load. Plex thumb
// URLs point at a private, token-gated host browsers can't reach. Bounded to the
// finalist set, so at most a handful of downloads per run.
//
// Books are skipped: Goodreads cover URLs are already public on i.gr-assets.com,
// so the browser loads them directly. Routing them through the Plex downloader
// would fetch every cover only to have the host check reject it, logging a
// warning per book.
func (r *Recommender) cachePoster(ctx context.Context, rec *models.Recommendation) {
	if r.posterDir == "" || rec.PosterURL == "" || r.plex == nil || rec.Type == models.TypeBook {
		return
	}
	name := fmt.Sprintf("%s-%d.jpg", rec.Type, posterID(rec))
	dest := filepath.Join(r.posterDir, name)
	if err := r.plex.DownloadImage(ctx, rec.PosterURL, dest); err != nil {
		logging.FromContext(ctx).Warnw("cache poster failed", "title", rec.Title, zap.Error(err))
		return
	}
	rec.PosterURL = "/posters/" + name
}

// posterID returns the Plex-backed ID used to name the cached poster file.
func posterID(rec *models.Recommendation) uint {
	switch {
	case rec.MovieID != nil:
		return *rec.MovieID
	case rec.TVShowID != nil:
		return *rec.TVShowID
	}
	return 0
}

func (r *Recommender) saveRecommendations(ctx context.Context, date time.Time, recs []models.Recommendation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(`"date" = ?`, date).Delete(&models.Recommendation{}).Error; err != nil {
			return fmt.Errorf("clear existing recs: %w", err)
		}
		// The (date, type, title) unique index rejects two items of the same type
		// sharing a title on one day; skip in-batch collisions rather than fail the
		// run. Type is part of the key so a book and a film of the same name — Dune,
		// say — can both appear.
		type key struct{ recType, title string }
		seen := make(map[key]bool, len(recs))
		for i := range recs {
			k := key{recs[i].Type, recs[i].Title}
			if seen[k] {
				continue
			}
			seen[k] = true
			if err := tx.Create(&recs[i]).Error; err != nil {
				return fmt.Errorf("create rec %q: %w", recs[i].Title, err)
			}
		}
		return nil
	})
}

func (r *Recommender) recordRun(ctx context.Context, date time.Time, c counts, genErr error) error {
	run := models.GenerationRun{
		Date: date, Status: models.RunStatusOK, MovieCount: c.movies,
		TVShowCount: c.tvshows, BookCount: c.books, Model: r.model,
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
