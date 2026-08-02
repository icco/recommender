// Package models defines the GORM-mapped types persisted to the Postgres
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
	TypeBook   = "book"
)

// Goodreads shelf values used in Book.Shelf.
const (
	ShelfRead   = "read"
	ShelfToRead = "to-read"
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
	Metascore     *int       `gorm:"index:idx_movies_metascore"`                              // Metacritic critic score 0-100 via OMDb; nil = none
	MetascoreAt   *time.Time `gorm:"index:idx_movies_metascore_at"`                           // last OMDb lookup; nil = never (a lookup with no score still stamps this)
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
	Metascore     *int       `gorm:"index:idx_tvshows_metascore"`                              // Metacritic critic score 0-100 via OMDb; nil = none. Often absent: Metacritic scores seasons, not series
	MetascoreAt   *time.Time `gorm:"index:idx_tvshows_metascore_at"`                           // last OMDb lookup; nil = never (a lookup with no score still stamps this)
	ViewCount     int        `gorm:"default:0;index:idx_tvshows_view_count"`                   // Plex view count (0 = unwatched)
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:TVShowID"`
}

// Book represents a book on one of the user's Goodreads shelves. ShelfToRead is
// the recommendable pool (the books analogue of an owned Plex library);
// ShelfRead carries the taste signal in UserRating. Shelf is refreshed on every
// sync, since a finished book moves between the two.
type Book struct {
	ID          uint    `gorm:"primarykey"`
	GoodreadsID string  `gorm:"type:varchar(32);not null;uniqueIndex:idx_books_goodreads_id"` // Goodreads book id from the shelf feed
	Title       string  `gorm:"type:varchar(500);not null;index:idx_books_title"`
	Author      string  `gorm:"type:varchar(255);index:idx_books_author"`
	Year        int     `gorm:"default:0;index:idx_books_year"`        // Publication year; 0 when the feed omits it (common on to-read)
	Rating      float64 `gorm:"index:idx_books_rating"`                // Goodreads community average, rescaled 0-10 to match Movie/TVShow
	UserRating  int     `gorm:"default:0;index:idx_books_user_rating"` // The user's own stars 1-5; 0 = unrated

	Shelf     string     `gorm:"type:varchar(32);not null;index:idx_books_shelf"` // "read" or "to-read"
	Genre     string     `gorm:"type:varchar(255);index:idx_books_genre"`         // Comma-joined user shelf names; usually empty, since most people don't shelve by genre
	CoverURL  string     `gorm:"type:varchar(1000)"`                              // Public i.gr-assets.com cover; needs no token, unlike Plex thumbs
	ISBN      string     `gorm:"type:varchar(32)"`
	Pages     int        `gorm:"default:0"` // Page count; 0 when unknown
	ReadAt    *time.Time `gorm:"index:idx_books_read_at"`
	AddedAt   *time.Time `gorm:"index:idx_books_added_at"`
	SyncedAt  time.Time  // Last time the shelf feed reported this book
	CreatedAt time.Time
	UpdatedAt time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:BookID"`
}

// Recommendation represents a single recommendation item with its metadata.
type Recommendation struct {
	ID uint `gorm:"primarykey"`
	// Keyed on (date, type, title), not (date, title): a book and a film share a
	// title often enough that the narrow key dropped one of them.
	Date          time.Time `gorm:"not null;index:idx_recommendations_date;uniqueIndex:idx_recommendations_date_type_title"`                                                            // The date this recommendation was generated
	Title         string    `gorm:"type:varchar(500);not null;index:idx_recommendations_title;uniqueIndex:idx_recommendations_date_type_title"`                                         // Title of the content
	Type          string    `gorm:"type:varchar(20);not null;index:idx_recommendations_type;uniqueIndex:idx_recommendations_date_type_title;check:type IN ('movie', 'tvshow', 'book')"` // "movie", "tvshow", or "book"
	Year          int       `gorm:"not null;index:idx_recommendations_year"`                                                                                                            // Release year
	Rating        float64   `gorm:"index:idx_recommendations_rating"`                                                                                                                   // Rating (e.g., from IMDB)
	Genre         string    `gorm:"type:varchar(255);index:idx_recommendations_genre"`                                                                                                  // Genre(s)
	PosterURL     string    `gorm:"type:varchar(1000)"`                                                                                                                                 // URL to the poster image
	Explanation   string    `gorm:"type:varchar(1000)"`                                                                                                                                 // model's one-line reason for this pick
	Metascore     *int      `gorm:"index:idx_recommendations_metascore"`                                                                                                                // Metacritic critic score 0-100 at generation time; nil = none
	MetacriticURL string    `gorm:"type:varchar(500)"`                                                                                                                                  // metacritic.com page for this title; "" when no Metascore
	Runtime       int       `gorm:"default:0"`                                                                                                                                          // Minutes (movies), seasons (TV shows), or pages (books)
	Author        string    `gorm:"type:varchar(255)"`                                                                                                                                  // Book author; "" for movies and TV shows
	SourceURL     string    `gorm:"type:varchar(500)"`                                                                                                                                  // Canonical page for the title (goodreads.com for books); "" when none
	MovieID       *uint     `gorm:"index:idx_recommendations_movie_id;constraint:OnDelete:CASCADE"`                                                                                     // Reference to Movie if Type is "movie"
	TVShowID      *uint     `gorm:"index:idx_recommendations_tvshow_id;constraint:OnDelete:CASCADE"`                                                                                    // Reference to TVShow if Type is "tvshow"
	BookID        *uint     `gorm:"index:idx_recommendations_book_id;constraint:OnDelete:CASCADE"`                                                                                      // Reference to Book if Type is "book"
	TMDbID        int       `gorm:"not null;index:idx_recommendations_tmdb_id"`                                                                                                         // The Movie Database ID
	ViewCount     int       `gorm:"-"`                                                                                                                                                  // Plex views when building prompts only (not stored)
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Relationships
	Movie  *Movie  `gorm:"foreignKey:MovieID"`
	TVShow *TVShow `gorm:"foreignKey:TVShowID"`
	Book   *Book   `gorm:"foreignKey:BookID"`
}

// HasMetascore reports whether Metacritic scored this title. Templates use this
// instead of a nil check so the score can be compared as a plain int.
func (r Recommendation) HasMetascore() bool { return r.Metascore != nil }

// MetascoreValue returns the Metacritic score, or 0 when there is none.
// Templates cannot dereference a *int, so display goes through this.
func (r Recommendation) MetascoreValue() int {
	if r.Metascore == nil {
		return 0
	}
	return *r.Metascore
}

// GoodreadsRating converts a book's Rating back to the native 5-star scale.
// Rating persists on the 0-10 scale so one scoring path serves all three tiers,
// but book cards show the number readers recognize.
func (r Recommendation) GoodreadsRating() float64 {
	return r.Rating / 2.0
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
	SourceOMDb          = "omdb"
	SourceGoodreads     = "goodreads"
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
	BookCount   int       `gorm:"default:0"`
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
