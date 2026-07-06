package recommend

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/icco/recommender/models"
)

// genreAffinity computes a normalized (0..1) taste weight per genre from watched
// and highly-rated Plex titles. Watched titles and higher ratings weigh more.
func (r *Recommender) genreAffinity(ctx context.Context) (map[string]float64, error) {
	raw := make(map[string]float64)
	movieGenres := make(map[uint][]string)
	tvGenres := make(map[uint][]string)

	accumulate := func(genres []string, rating float64, viewCount int) {
		for _, g := range genres {
			w := rating / 10.0
			if viewCount > 0 {
				w += 1.0
			}
			raw[g] += w
		}
	}

	var movies []models.Movie
	if err := r.db.WithContext(ctx).Find(&movies).Error; err != nil {
		return nil, fmt.Errorf("affinity movies: %w", err)
	}
	for _, m := range movies {
		g := splitGenres(m.Genre)
		movieGenres[m.ID] = g
		accumulate(g, m.Rating, m.ViewCount)
	}
	var shows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&shows).Error; err != nil {
		return nil, fmt.Errorf("affinity shows: %w", err)
	}
	for _, s := range shows {
		g := splitGenres(s.Genre)
		tvGenres[s.ID] = g
		accumulate(g, s.Rating, s.ViewCount)
	}

	// Fold in external rated/score signals: a high signal lifts its title's genres.
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).
		Where("kind IN ?", []string{models.SignalKindRated, models.SignalKindScore}).
		Find(&sigs).Error; err != nil {
		return nil, fmt.Errorf("affinity signals: %w", err)
	}
	for _, sig := range sigs {
		var genres []string
		switch {
		case sig.MovieID != nil:
			genres = movieGenres[*sig.MovieID]
		case sig.TVShowID != nil:
			genres = tvGenres[*sig.TVShowID]
		}
		for _, g := range genres {
			raw[g] += sig.Value / 10.0
		}
	}

	peak := 0.0
	for _, v := range raw {
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return map[string]float64{}, nil
	}
	out := make(map[string]float64, len(raw))
	for g, v := range raw {
		out[g] = v / peak
	}
	return out, nil
}

// tasteProfile renders the top genres as a short prompt fragment.
func (r *Recommender) tasteProfile(ctx context.Context) (string, error) {
	aff, err := r.genreAffinity(ctx)
	if err != nil {
		return "", err
	}
	if len(aff) == 0 {
		return "", nil
	}
	type gv struct {
		g string
		v float64
	}
	var gvs []gv
	for g, v := range aff {
		gvs = append(gvs, gv{g, v})
	}
	sort.Slice(gvs, func(i, j int) bool {
		if gvs[i].v == gvs[j].v {
			return gvs[i].g < gvs[j].g
		}
		return gvs[i].v > gvs[j].v
	})
	n := 5
	if len(gvs) < n {
		n = len(gvs)
	}
	tops := make([]string, 0, n)
	for _, x := range gvs[:n] {
		tops = append(tops, x.g)
	}
	return "Favorite genres, most to least: " + strings.Join(tops, ", ") + ".", nil
}

// lovedTitles summarizes up to 5 highly-rated (Value >= 8) owned titles from
// external signals, for prompt context. Empty when there are none.
func (r *Recommender) lovedTitles(ctx context.Context) (string, error) {
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).
		Where("kind IN ? AND value >= ?", []string{models.SignalKindRated, models.SignalKindScore}, 8.0).
		Order("value DESC").Limit(20).Find(&sigs).Error; err != nil {
		return "", fmt.Errorf("loved signals: %w", err)
	}
	seen := make(map[string]struct{})
	var titles []string
	for _, sig := range sigs {
		var title string
		switch {
		case sig.MovieID != nil:
			var m models.Movie
			if err := r.db.WithContext(ctx).First(&m, *sig.MovieID).Error; err == nil {
				title = m.Title
			}
		case sig.TVShowID != nil:
			var s models.TVShow
			if err := r.db.WithContext(ctx).First(&s, *sig.TVShowID).Error; err == nil {
				title = s.Title
			}
		}
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
		if len(titles) == 5 {
			break
		}
	}
	if len(titles) == 0 {
		return "", nil
	}
	return "Recently loved: " + strings.Join(titles, ", ") + ".", nil
}
