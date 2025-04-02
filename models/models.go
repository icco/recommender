package models

import (
	"time"
)

// Movie represents a movie from Plex
type Movie struct {
	ID        uint    `gorm:"primarykey"`
	Title     string  // Title of the movie
	Year      int     // Release year
	Rating    float64 // Rating (e.g., from IMDB)
	Genre     string  // Genre(s)
	PosterURL string  // URL to the poster image
	Runtime   int     // Runtime in minutes
	Source    string  // Source of the content (e.g., "plex")
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TVShow represents a TV show from Plex
type TVShow struct {
	ID        uint    `gorm:"primarykey"`
	Title     string  // Title of the show
	Year      int     // Release year
	Rating    float64 // Rating (e.g., from IMDB)
	Genre     string  // Genre(s)
	PosterURL string  // URL to the poster image
	Seasons   int     // Number of seasons
	Source    string  // Source of the content (e.g., "plex")
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Recommendation represents a single recommendation item with its metadata.
type Recommendation struct {
	ID        uint      `gorm:"primarykey"`
	Date      time.Time `gorm:"index"` // The date this recommendation was generated
	Title     string    // Title of the content
	Type      string    // "movie", "anime", or "tvshow"
	Year      int       // Release year
	Rating    float64   // Rating (e.g., from IMDB, Anilist)
	Genre     string    // Genre(s)
	PosterURL string    // URL to the poster image
	Runtime   int       // Runtime in minutes (for movies) or episodes (for anime) or seasons (for TV shows)
	Source    string    // Source of the content (e.g., "plex", "anilist")
	MovieID   *uint     // Reference to Movie if Type is "movie"
	TVShowID  *uint     // Reference to TVShow if Type is "tvshow"
	CreatedAt time.Time
	UpdatedAt time.Time
}
