// Package models defines the GORM-mapped types persisted to the SQLite
// database (movies, TV shows, recommendations) plus the small set of shared
// type-name constants used to discriminate rows.
package models

import (
	"time"
)

// Recommendation type values used in Recommendation.Type and SQL `type` filters.
const (
	TypeMovie  = "movie"
	TypeTVShow = "tvshow"
)

// Movie represents a movie from Plex
type Movie struct {
	ID            uint       `gorm:"primarykey"`
	PlexRatingKey string     `gorm:"type:varchar(64);uniqueIndex:idx_movies_plex_rating_key"` // Plex metadata ratingKey (stable per library item)
	Title         string     `gorm:"type:varchar(500);not null;index:idx_movies_title"`       // Title of the movie
	Year          int        `gorm:"not null;index:idx_movies_year"`                          // Release year (not unique: Plex can have same title+year for different items)
	Rating        float64    `gorm:"index:idx_movies_rating"`                                 // Rating (e.g., from IMDB)
	Genre         string     `gorm:"type:varchar(255);index:idx_movies_genre"`                // Genre(s)
	PosterURL     string     `gorm:"type:varchar(1000)"`                                      // URL to the poster image
	Runtime       int        `gorm:"default:0"`                                               // Runtime in minutes
	TMDbID        *int       `gorm:"uniqueIndex:idx_movies_tmdb_id"`                          // The Movie Database ID (nullable)
	IMDbID        string     `gorm:"type:varchar(32);index:idx_movies_imdb_id"`               // Plex GUID imdb://
	TVDbID        string     `gorm:"type:varchar(32)"`                                        // Plex GUID tvdb://
	EnrichedAt    *time.Time `gorm:"index:idx_movies_enriched_at"`                            // last TMDb enrichment; nil = never
	ViewCount     int        `gorm:"default:0;index:idx_movies_view_count"`                   // Plex view count (0 = unwatched)
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:MovieID"`
}

// TVShow represents a TV show from Plex
type TVShow struct {
	ID            uint       `gorm:"primarykey"`
	PlexRatingKey string     `gorm:"type:varchar(64);uniqueIndex:idx_tvshows_plex_rating_key"` // Plex metadata ratingKey (stable per library item)
	Title         string     `gorm:"type:varchar(500);not null;index:idx_tvshows_title"`       // Title of the show
	Year          int        `gorm:"not null;index:idx_tvshows_year"`                          // Release year
	Rating        float64    `gorm:"index:idx_tvshows_rating"`                                 // Rating (e.g., from IMDB)
	Genre         string     `gorm:"type:varchar(255);index:idx_tvshows_genre"`                // Genre(s)
	PosterURL     string     `gorm:"type:varchar(1000)"`                                       // URL to the poster image
	Seasons       int        `gorm:"default:0"`                                                // Number of seasons
	TMDbID        *int       `gorm:"uniqueIndex:idx_tvshows_tmdb_id"`                          // The Movie Database ID (nullable)
	IMDbID        string     `gorm:"type:varchar(32);index:idx_tvshows_imdb_id"`               // Plex GUID imdb://
	TVDbID        string     `gorm:"type:varchar(32)"`                                         // Plex GUID tvdb://
	EnrichedAt    *time.Time `gorm:"index:idx_tvshows_enriched_at"`                            // last TMDb enrichment; nil = never
	ViewCount     int        `gorm:"default:0;index:idx_tvshows_view_count"`                   // Plex view count (0 = unwatched)
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:TVShowID"`
}

// Recommendation represents a single recommendation item with its metadata.
type Recommendation struct {
	ID          uint      `gorm:"primarykey"`
	Date        time.Time `gorm:"not null;index:idx_recommendations_date;uniqueIndex:idx_recommendations_date_title"`                    // The date this recommendation was generated
	Title       string    `gorm:"type:varchar(500);not null;index:idx_recommendations_title;uniqueIndex:idx_recommendations_date_title"` // Title of the content
	Type        string    `gorm:"type:varchar(20);not null;index:idx_recommendations_type;check:type IN ('movie', 'tvshow')"`            // "movie" or "tvshow"
	Year        int       `gorm:"not null;index:idx_recommendations_year"`                                                               // Release year
	Rating      float64   `gorm:"index:idx_recommendations_rating"`                                                                      // Rating (e.g., from IMDB)
	Genre       string    `gorm:"type:varchar(255);index:idx_recommendations_genre"`                                                     // Genre(s)
	PosterURL   string    `gorm:"type:varchar(1000)"`                                                                                    // URL to the poster image
	Explanation string    `gorm:"type:varchar(1000)"`                                                                                    // model's one-line reason for this pick
	Runtime     int       `gorm:"default:0"`                                                                                             // Runtime in minutes (for movies) or seasons (for TV shows)
	MovieID     *uint     `gorm:"index:idx_recommendations_movie_id;constraint:OnDelete:CASCADE"`                                        // Reference to Movie if Type is "movie"
	TVShowID    *uint     `gorm:"index:idx_recommendations_tvshow_id;constraint:OnDelete:CASCADE"`                                       // Reference to TVShow if Type is "tvshow"
	TMDbID      int       `gorm:"not null;index:idx_recommendations_tmdb_id"`                                                            // The Movie Database ID
	ViewCount   int       `gorm:"-"`                                                                                                     // Plex views when building prompts only (not stored)
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Relationships
	Movie  *Movie  `gorm:"foreignKey:MovieID"`
	TVShow *TVShow `gorm:"foreignKey:TVShowID"`
}

// Run status values for GenerationRun.Status.
const (
	RunStatusOK    = "ok"
	RunStatusError = "error"
)

// Signal source + kind values for ExternalSignal.
const (
	SourcePlex          = "plex"
	SourceTrakt         = "trakt"
	SourceAniList       = "anilist"
	SignalKindWatched   = "watched"
	SignalKindRated     = "rated"
	SignalKindScore     = "score"
	SignalKindWatchlist = "watchlist"
)

// GenerationRun records one recommendation-generation attempt for a day.
type GenerationRun struct {
	ID          uint      `gorm:"primarykey"`
	Date        time.Time `gorm:"not null;index:idx_generation_runs_date"` // UTC midnight of the target day
	Status      string    `gorm:"type:varchar(20);not null"`               // "ok" or "error"
	MovieCount  int       `gorm:"default:0"`
	TVShowCount int       `gorm:"default:0"`
	Model       string    `gorm:"type:varchar(64)"`
	DurationMS  int64     `gorm:"default:0"`
	Error       string    `gorm:"type:varchar(1000)"`
	CreatedAt   time.Time
}

// ExternalSignal is a per-title or per-user signal from a source (Plex, Trakt, …)
// used to personalize scoring. Recommendations remain Plex-owned; signals only rank.
type ExternalSignal struct {
	ID          uint    `gorm:"primarykey"`
	Source      string  `gorm:"type:varchar(32);not null;uniqueIndex:idx_signal_unique"`
	ExternalRef string  `gorm:"type:varchar(128);uniqueIndex:idx_signal_unique"` // e.g. imdb id or "genre:Comedy"
	Kind        string  `gorm:"type:varchar(20);not null;uniqueIndex:idx_signal_unique"`
	MovieID     *uint   `gorm:"index"`
	TVShowID    *uint   `gorm:"index"`
	Value       float64 `gorm:"default:0"`
	UpdatedAt   time.Time
}

// OAuthToken stores an OAuth token set for an external source (e.g. Trakt).
type OAuthToken struct {
	ID           uint   `gorm:"primarykey"`
	Source       string `gorm:"type:varchar(32);not null;uniqueIndex:idx_oauth_source"`
	AccessToken  string `gorm:"type:varchar(512)"`
	RefreshToken string `gorm:"type:varchar(512)"`
	ExpiresAt    time.Time
	UpdatedAt    time.Time
}
