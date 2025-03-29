package models

import (
	"time"

	"gorm.io/gorm"
)

type Recommendation struct {
	gorm.Model
	Date    time.Time
	Movies  []Movie
	Anime   []Anime
	TVShows []TVShow
}

type Movie struct {
	gorm.Model
	RecommendationID uint
	Title            string
	Year             int
	Rating           float64
	Genre            string
	Runtime          int
	PosterURL        string
	Source           string // "plex", "letterboxd", etc.
	Seen             bool
	Type             string // "funny", "action", "drama", "seen"
}

type Anime struct {
	gorm.Model
	RecommendationID uint
	Title            string
	Year             int
	Rating           float64
	Genre            string
	Episodes         int
	PosterURL        string
	Source           string // "anilist", "plex", etc.
}

type TVShow struct {
	gorm.Model
	RecommendationID uint
	Title            string
	Year             int
	Rating           float64
	Genre            string
	Seasons          int
	PosterURL        string
	Source           string // "plex", "traktv", etc.
}
