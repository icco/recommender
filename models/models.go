package models

import (
	"time"
)

// Movie represents a movie from Plex
type Movie struct {
	ID        uint    `gorm:"primarykey"`
	Title     string  `gorm:"type:varchar(500);not null;index:idx_movies_title;uniqueIndex:idx_movies_title_year"` // Title of the movie
	Year      int     `gorm:"not null;index:idx_movies_year;uniqueIndex:idx_movies_title_year"`                    // Release year
	Rating    float64 `gorm:"index:idx_movies_rating"`                           // Rating (e.g., from IMDB)
	Genre     string  `gorm:"type:varchar(255);index:idx_movies_genre"`          // Genre(s)
	PosterURL string  `gorm:"type:varchar(1000)"`                               // URL to the poster image
	Runtime   int     `gorm:"default:0"`                                        // Runtime in minutes
	TMDbID    *int    `gorm:"uniqueIndex:idx_movies_tmdb_id"`                   // The Movie Database ID (nullable)
	CreatedAt time.Time
	UpdatedAt time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:MovieID"`
}

// TVShow represents a TV show from Plex
type TVShow struct {
	ID        uint    `gorm:"primarykey"`
	Title     string  `gorm:"type:varchar(500);not null;index:idx_tvshows_title;uniqueIndex:idx_tvshows_title_year"` // Title of the show
	Year      int     `gorm:"not null;index:idx_tvshows_year;uniqueIndex:idx_tvshows_title_year"`                    // Release year
	Rating    float64 `gorm:"index:idx_tvshows_rating"`                           // Rating (e.g., from IMDB)
	Genre     string  `gorm:"type:varchar(255);index:idx_tvshows_genre"`          // Genre(s)
	PosterURL string  `gorm:"type:varchar(1000)"`                                // URL to the poster image
	Seasons   int     `gorm:"default:0"`                                          // Number of seasons
	TMDbID    *int    `gorm:"uniqueIndex:idx_tvshows_tmdb_id"`                    // The Movie Database ID (nullable)
	CreatedAt time.Time
	UpdatedAt time.Time

	// Relationships
	Recommendations []Recommendation `gorm:"foreignKey:TVShowID"`
}

// Recommendation represents a single recommendation item with its metadata.
type Recommendation struct {
	ID        uint      `gorm:"primarykey"`
	Date      time.Time `gorm:"not null;index:idx_recommendations_date;uniqueIndex:idx_recommendations_date_title"`          // The date this recommendation was generated
	Title     string    `gorm:"type:varchar(500);not null;index:idx_recommendations_title;uniqueIndex:idx_recommendations_date_title"` // Title of the content
	Type      string    `gorm:"type:varchar(20);not null;index:idx_recommendations_type;check:type IN ('movie', 'tvshow')"` // "movie" or "tvshow"
	Year      int       `gorm:"not null;index:idx_recommendations_year"`          // Release year
	Rating    float64   `gorm:"index:idx_recommendations_rating"`                 // Rating (e.g., from IMDB)
	Genre     string    `gorm:"type:varchar(255);index:idx_recommendations_genre"` // Genre(s)
	PosterURL string    `gorm:"type:varchar(1000)"`                               // URL to the poster image
	Runtime   int       `gorm:"default:0"`                                        // Runtime in minutes (for movies) or seasons (for TV shows)
	MovieID   *uint     `gorm:"index:idx_recommendations_movie_id;constraint:OnDelete:CASCADE"` // Reference to Movie if Type is "movie"
	TVShowID  *uint     `gorm:"index:idx_recommendations_tvshow_id;constraint:OnDelete:CASCADE"` // Reference to TVShow if Type is "tvshow"
	TMDbID    int       `gorm:"not null;index:idx_recommendations_tmdb_id"`       // The Movie Database ID
	CreatedAt time.Time
	UpdatedAt time.Time

	// Relationships
	Movie  *Movie  `gorm:"foreignKey:MovieID"`
	TVShow *TVShow `gorm:"foreignKey:TVShowID"`
}
