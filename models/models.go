package models

import (
	"time"
)

// MediaItem represents the common interface for all media types
type MediaItem interface {
	GetID() uint
	GetTitle() string
	GetYear() int
	GetRating() float64
	GetGenre() string
	GetPosterURL() string
	GetSource() string
}

// BaseMedia contains common fields for all media types
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

func (b BaseMedia) GetID() uint          { return b.ID }
func (b BaseMedia) GetTitle() string     { return b.Title }
func (b BaseMedia) GetYear() int         { return b.Year }
func (b BaseMedia) GetRating() float64   { return b.Rating }
func (b BaseMedia) GetGenre() string     { return b.Genre }
func (b BaseMedia) GetPosterURL() string { return b.PosterURL }
func (b BaseMedia) GetSource() string    { return b.Source }

type Recommendation struct {
	ID        uint      `gorm:"primarykey"`
	Date      time.Time `gorm:"uniqueIndex"`
	Movies    []Movie   `gorm:"many2many:recommendation_movies;"`
	Anime     []Anime   `gorm:"many2many:recommendation_anime;"`
	TVShows   []TVShow  `gorm:"many2many:recommendation_tvshows;"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Movie struct {
	BaseMedia
	Runtime int
}

func (m Movie) GetTitle() string {
	return m.Title
}

type Anime struct {
	BaseMedia
	Episodes int
}

func (a Anime) GetTitle() string {
	return a.Title
}

type TVShow struct {
	BaseMedia
	Seasons int
}

func (t TVShow) GetTitle() string {
	return t.Title
}

type PlexCache struct {
	ID        uint `gorm:"primarykey"`
	UpdatedAt time.Time
	Movies    []PlexMovie  `gorm:"many2many:plex_cache_movies;"`
	Anime     []PlexAnime  `gorm:"many2many:plex_cache_anime;"`
	TVShows   []PlexTVShow `gorm:"many2many:plex_cache_tvshows;"`
}

type PlexMovie struct {
	BaseMedia
	Runtime int
	Watched bool
}

func (m PlexMovie) IsWatched() bool {
	return m.Watched
}

type PlexAnime struct {
	BaseMedia
	Episodes int
	Watched  bool
}

func (a PlexAnime) IsWatched() bool {
	return a.Watched
}

type PlexTVShow struct {
	BaseMedia
	Seasons int
	Watched bool
}

func (t PlexTVShow) IsWatched() bool {
	return t.Watched
}

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
