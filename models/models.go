package models

import (
	"time"
)

// MediaItem represents the common interface for all media types.
// It defines the basic properties that all media items must implement.
type MediaItem interface {
	GetID() uint
	GetTitle() string
	GetYear() int
	GetRating() float64
	GetGenre() string
	GetPosterURL() string
	GetSource() string
}

// BaseMedia contains common fields for all media types.
// It implements the MediaItem interface and provides the base structure
// for movies, anime, and TV shows.
type BaseMedia struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	PosterURL string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetID returns the unique identifier of the media item.
func (b BaseMedia) GetID() uint { return b.ID }

// GetTitle returns the title of the media item.
func (b BaseMedia) GetTitle() string { return b.Title }

// GetYear returns the release year of the media item.
func (b BaseMedia) GetYear() int { return b.Year }

// GetRating returns the rating of the media item.
func (b BaseMedia) GetRating() float64 { return b.Rating }

// GetGenre returns the genre of the media item.
func (b BaseMedia) GetGenre() string { return b.Genre }

// GetPosterURL returns the URL of the media item's poster image.
func (b BaseMedia) GetPosterURL() string { return b.PosterURL }

// GetSource returns the source of the media item (e.g., "plex", "anilist").
func (b BaseMedia) GetSource() string { return b.Source }

// Recommendation represents a daily recommendation containing movies, anime, and TV shows.
type Recommendation struct {
	ID        uint      `gorm:"primarykey"`
	Date      time.Time `gorm:"uniqueIndex"`
	Movies    []Movie   `gorm:"many2many:recommendation_movies;"`
	Anime     []Anime   `gorm:"many2many:recommendation_anime;"`
	TVShows   []TVShow  `gorm:"many2many:recommendation_tvshows;"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Movie represents a film with its runtime.
type Movie struct {
	BaseMedia
	Runtime int
}

// GetTitle returns the title of the movie.
func (m Movie) GetTitle() string {
	return m.Title
}

// Anime represents an animated series with its episode count.
type Anime struct {
	BaseMedia
	Episodes int
}

// GetTitle returns the title of the anime.
func (a Anime) GetTitle() string {
	return a.Title
}

// TVShow represents a television series with its season count.
type TVShow struct {
	BaseMedia
	Seasons int
}

// GetTitle returns the title of the TV show.
func (t TVShow) GetTitle() string {
	return t.Title
}

// PlexCache represents a cache of media items from Plex.
type PlexCache struct {
	ID        uint `gorm:"primarykey"`
	UpdatedAt time.Time
	Movies    []PlexMovie  `gorm:"many2many:plex_cache_movies;"`
	Anime     []PlexAnime  `gorm:"many2many:plex_cache_anime;"`
	TVShows   []PlexTVShow `gorm:"many2many:plex_cache_tvshows;"`
}

// PlexMovie represents a movie from Plex with its watch status.
type PlexMovie struct {
	BaseMedia
	Runtime int
	Watched bool
}

// IsWatched returns whether the movie has been watched.
func (m PlexMovie) IsWatched() bool {
	return m.Watched
}

// PlexAnime represents an anime from Plex with its watch status.
type PlexAnime struct {
	BaseMedia
	Episodes int
	Watched  bool
}

// IsWatched returns whether the anime has been watched.
func (a PlexAnime) IsWatched() bool {
	return a.Watched
}

// PlexTVShow represents a TV show from Plex with its watch status.
type PlexTVShow struct {
	BaseMedia
	Seasons int
	Watched bool
}

// IsWatched returns whether the TV show has been watched.
func (t PlexTVShow) IsWatched() bool {
	return t.Watched
}

// UserPreference represents user preferences for content recommendations.
type UserPreference struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// Favorite genres across all media types
	FavoriteGenres []string `gorm:"type:text[]"`
	// Preferred themes and topics
	Themes []string `gorm:"type:text[]"`
	// Preferred mood (e.g., light-hearted, serious, thought-provoking)
	Moods []string `gorm:"type:text[]"`
	// Preferred content length (short, medium, long)
	ContentLengths []string `gorm:"type:text[]"`
	// Preferred decades or time periods
	TimePeriods []string `gorm:"type:text[]"`
	// Preferred languages
	Languages []string `gorm:"type:text[]"`
	// Preferred content sources
	Sources []string `gorm:"type:text[]"`
}

// UserRating represents a user's rating and review of a media item.
type UserRating struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// Reference to the content (movie, anime, or TV show)
	ContentType string // "movie", "anime", or "tvshow"
	ContentID   uint
	// Rating (1-5)
	Rating int
	// Review text
	Review string
	// Date watched
	WatchedAt time.Time
	// Tags for themes, genres, etc.
	Tags []string `gorm:"type:text[]"`
	// Similar content IDs (for RAG system)
	SimilarContent []uint `gorm:"type:integer[]"`
}
