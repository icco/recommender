package recommend

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/icco/recommender/models"
)

// candidate is a Plex-owned title eligible for recommendation, with a computed score.
type candidate struct {
	ID          uint
	Type        string
	Title       string
	Year        int
	Rating      float64
	Genres      []string
	PosterURL   string
	Runtime     int // minutes (movie) or seasons (tv)
	ViewCount   int
	TMDbID      *int
	Affinity    float64 // taste-profile boost (Phase 2); 0 otherwise
	Watchlisted bool    // present on an external watchlist (Trakt)
	Metascore   *int    // Metacritic critic score 0-100; nil when unknown
}

// dateSeed derives a stable per-UTC-day seed so shortlists are reproducible.
func dateSeed(date time.Time) int64 {
	y, m, d := date.UTC().Date()
	return int64(y)*10000 + int64(m)*100 + int64(d)
}

// watchlistBoost lifts titles the user has explicitly watchlisted externally.
const watchlistBoost = 1.5

// metascoreWeight scales the Metacritic term. Kept below watchlistBoost so
// critics nudge the ranking rather than dominate the user's own signals.
const metascoreWeight = 1.0

// metascoreNeutral is the Metascore treated as "no opinion". The term is
// centered here so a title Metacritic never scored ranks the same as a merely
// average one — otherwise every unscored title (most TV, since Metacritic rates
// seasons rather than series) would be pushed down for lack of data.
const metascoreNeutral = 55.0

// metascoreBoost maps a 0-100 Metascore onto roughly [-1, +1] × weight, or 0
// when unknown.
func metascoreBoost(score *int) float64 {
	if score == nil {
		return 0
	}
	return (float64(*score) - metascoreNeutral) / metascoreNeutral * metascoreWeight
}

// scoreCandidate ranks a title: rating drives it, unwatched gets a novelty
// boost, taste affinity, watchlist membership, and critic reception add on top.
func scoreCandidate(c candidate) float64 {
	s := c.Rating / 10.0 * 2.0
	if c.ViewCount == 0 {
		s += 1.0
	}
	s += c.Affinity
	if c.Watchlisted {
		s += watchlistBoost
	}
	s += metascoreBoost(c.Metascore)
	return s
}

// buildShortlist takes the top poolSize by score, then a date-seeded shuffle to
// shortlistSize — quality plus deterministic daily variety.
func buildShortlist(cands []candidate, date time.Time, poolSize, shortlistSize int) []candidate {
	sorted := make([]candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := scoreCandidate(sorted[i]), scoreCandidate(sorted[j])
		if si == sj {
			return sorted[i].ID < sorted[j].ID // stable tie-break
		}
		return si > sj
	})
	if poolSize < len(sorted) {
		sorted = sorted[:poolSize]
	}
	rng := rand.New(rand.NewSource(dateSeed(date))) //nolint:gosec // deterministic daily shuffle, not security-sensitive
	rng.Shuffle(len(sorted), func(i, j int) { sorted[i], sorted[j] = sorted[j], sorted[i] })
	if shortlistSize < len(sorted) {
		sorted = sorted[:shortlistSize]
	}
	return sorted
}

// formatShortlist renders candidates for the prompt, keyed by DB ID so the model
// returns IDs (never titles).
func formatShortlist(cands []candidate) string {
	var b strings.Builder
	for _, c := range cands {
		watched := "unwatched"
		if c.ViewCount > 0 {
			watched = "watched"
		}
		// Metacritic is omitted rather than sent as "unknown", so the model does
		// not read a gap in coverage as a poor review.
		metacritic := ""
		if c.Metascore != nil {
			metacritic = fmt.Sprintf(" — Metacritic: %d", *c.Metascore)
		}
		fmt.Fprintf(&b, "[id=%d] %s (%d) — Rating: %.1f%s — Genres: %s — %s\n",
			c.ID, c.Title, c.Year, c.Rating, metacritic, strings.Join(c.Genres, ", "), watched)
	}
	return b.String()
}

// loadCandidates loads eligible movies and TV shows, excluding titles recommended
// in the last 30 days. TV is restricted to unwatched shows.
func (r *Recommender) loadCandidates(ctx context.Context, date time.Time) (movies, tvshows []candidate, err error) {
	excludeMovies, excludeTV, err := r.recentlyRecommendedIDs(ctx, date, 30)
	if err != nil {
		return nil, nil, err
	}

	aff, err := r.genreAffinity(ctx)
	if err != nil {
		return nil, nil, err
	}
	affinityFor := func(genres []string) float64 {
		best := 0.0
		for _, g := range genres {
			if v := aff[g]; v > best {
				best = v
			}
		}
		return best
	}

	watchlistMovies, watchlistTV, err := r.signalIDSet(ctx, models.SignalKindWatchlist)
	if err != nil {
		return nil, nil, err
	}
	watchedMovies, watchedTV, err := r.signalIDSet(ctx, models.SignalKindWatched)
	if err != nil {
		return nil, nil, err
	}

	metaMovies, metaTV, err := r.metascoreMaps(ctx)
	if err != nil {
		return nil, nil, err
	}
	metascoreFor := func(scores map[uint]int, id uint) *int {
		if v, ok := scores[id]; ok {
			return &v
		}
		return nil
	}

	var dbMovies []models.Movie
	if err := r.db.WithContext(ctx).Find(&dbMovies).Error; err != nil {
		return nil, nil, fmt.Errorf("load movies: %w", err)
	}
	for _, m := range dbMovies {
		if _, skip := excludeMovies[m.ID]; skip {
			continue
		}
		genres := splitGenres(m.Genre)
		vc := m.ViewCount
		if _, w := watchedMovies[m.ID]; w && vc == 0 {
			vc = 1 // treat Trakt-watched as watched
		}
		_, wl := watchlistMovies[m.ID]
		movies = append(movies, candidate{
			ID: m.ID, Type: models.TypeMovie, Title: m.Title, Year: m.Year,
			Rating: m.Rating, Genres: genres, PosterURL: m.PosterURL,
			Runtime: m.Runtime, ViewCount: vc, TMDbID: m.TMDbID,
			Affinity: affinityFor(genres), Watchlisted: wl,
			Metascore: metascoreFor(metaMovies, m.ID),
		})
	}

	var dbShows []models.TVShow
	if err := r.db.WithContext(ctx).Where("view_count = 0").Find(&dbShows).Error; err != nil {
		return nil, nil, fmt.Errorf("load tv shows: %w", err)
	}
	for _, s := range dbShows {
		if _, skip := excludeTV[s.ID]; skip {
			continue
		}
		if _, watched := watchedTV[s.ID]; watched {
			continue // watched elsewhere; not a fresh TV pick
		}
		genres := splitGenres(s.Genre)
		_, wl := watchlistTV[s.ID]
		tvshows = append(tvshows, candidate{
			ID: s.ID, Type: models.TypeTVShow, Title: s.Title, Year: s.Year,
			Rating: s.Rating, Genres: genres, PosterURL: s.PosterURL,
			Runtime: s.Seasons, ViewCount: s.ViewCount, TMDbID: s.TMDbID,
			Affinity: affinityFor(genres), Watchlisted: wl,
			Metascore: metascoreFor(metaTV, s.ID),
		})
	}
	return movies, tvshows, nil
}

// recentlyRecommendedIDs returns Movie/TVShow IDs recommended within the last `days` days.
func (r *Recommender) recentlyRecommendedIDs(ctx context.Context, date time.Time, days int) (map[uint]struct{}, map[uint]struct{}, error) {
	cutoff := date.AddDate(0, 0, -days)
	var recs []models.Recommendation
	if err := r.db.WithContext(ctx).
		Where(`"date" >= ? AND "date" <= ?`, cutoff, date).
		Find(&recs).Error; err != nil {
		return nil, nil, fmt.Errorf("load recent recommendations: %w", err)
	}
	m := make(map[uint]struct{})
	tv := make(map[uint]struct{})
	for _, rec := range recs {
		if rec.MovieID != nil {
			m[*rec.MovieID] = struct{}{}
		}
		if rec.TVShowID != nil {
			tv[*rec.TVShowID] = struct{}{}
		}
	}
	return m, tv, nil
}

// signalIDSet returns the Movie and TVShow IDs that have a signal of the given kind.
func (r *Recommender) signalIDSet(ctx context.Context, kind string) (map[uint]struct{}, map[uint]struct{}, error) {
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).Where("kind = ?", kind).Find(&sigs).Error; err != nil {
		return nil, nil, fmt.Errorf("load %s signals: %w", kind, err)
	}
	m := make(map[uint]struct{})
	tv := make(map[uint]struct{})
	for _, s := range sigs {
		if s.MovieID != nil {
			m[*s.MovieID] = struct{}{}
		}
		if s.TVShowID != nil {
			tv[*s.TVShowID] = struct{}{}
		}
	}
	return m, tv, nil
}

// splitGenres parses the comma-joined genre column into a slice.
func splitGenres(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
