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

	accumulate := func(genre string, rating float64, viewCount int) {
		for _, g := range splitGenres(genre) {
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
		accumulate(m.Genre, m.Rating, m.ViewCount)
	}
	var shows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&shows).Error; err != nil {
		return nil, fmt.Errorf("affinity shows: %w", err)
	}
	for _, s := range shows {
		accumulate(s.Genre, s.Rating, s.ViewCount)
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
