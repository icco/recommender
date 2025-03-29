package models

import (
	"time"
)

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
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Runtime   int
	PosterURL string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Anime struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Episodes  int
	PosterURL string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TVShow struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Seasons   int
	PosterURL string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PlexCache struct {
	ID        uint `gorm:"primarykey"`
	UpdatedAt time.Time
	Movies    []PlexMovie  `gorm:"many2many:plex_cache_movies;"`
	Anime     []PlexAnime  `gorm:"many2many:plex_cache_anime;"`
	TVShows   []PlexTVShow `gorm:"many2many:plex_cache_tvshows;"`
}

type PlexMovie struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Runtime   int
	PosterURL string
	Watched   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (m PlexMovie) IsWatched() bool {
	return m.Watched
}

type PlexAnime struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Episodes  int
	PosterURL string
	Watched   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (a PlexAnime) IsWatched() bool {
	return a.Watched
}

type PlexTVShow struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"uniqueIndex"`
	Year      int
	Rating    float64
	Genre     string
	Seasons   int
	PosterURL string
	Watched   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (t PlexTVShow) IsWatched() bool {
	return t.Watched
}
