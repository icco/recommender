package types

import "time"

// StatsData represents statistics about the recommendations database.
type StatsData struct {
	TotalRecommendations        int64
	TotalMovies                 int64
	TotalAnime                  int64
	TotalTVShows                int64
	FirstDate                   time.Time
	LastDate                    time.Time
	AverageDailyRecommendations float64
	GenreDistribution           []struct {
		Genre string
		Count int64
	}
}
