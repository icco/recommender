package models

import (
	"time"
)

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
	CreatedAt time.Time
	UpdatedAt time.Time
}
