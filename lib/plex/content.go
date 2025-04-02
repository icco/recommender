package plex

import (
	"context"
	"fmt"
	"strings"

	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
)

// GetAllMovies gets all movies from Plex
func (c *Client) GetAllMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Recommendation, error) {
	var movies []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != "movie" {
			continue
		}

		items, err := c.GetPlexItems(ctx, lib.Key, false)
		if err != nil {
			return nil, fmt.Errorf("failed to get library items: %w", err)
		}

		for _, item := range items {
			year := 0
			if item.Year != nil {
				year = *item.Year
			}

			rating := 0.0
			if item.Rating != nil {
				rating = *item.Rating
			}

			genre := ""
			if len(item.Genre) > 0 && item.Genre[0].Tag != nil {
				genre = *item.Genre[0].Tag
			}

			duration := 0
			if item.Duration != nil {
				duration = *item.Duration / 60000 // Convert milliseconds to minutes
			}

			thumb := ""
			if item.Thumb != nil {
				// Search for the movie in TMDB to get the poster URL
				result, err := c.tmdb.SearchMovie(ctx, item.Title, year)
				if err == nil && len(result.Results) > 0 {
					thumb = c.tmdb.GetPosterURL(result.Results[0].PosterPath)
				}
			}

			movies = append(movies, models.Recommendation{
				Title:     item.Title,
				Type:      "movie",
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: thumb,
				Runtime:   duration,
				Source:    "plex",
			})
		}
	}
	return movies, nil
}

// GetAllTVShows gets all TV shows from Plex
func (c *Client) GetAllTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Recommendation, error) {
	var shows []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != "show" {
			continue
		}

		items, err := c.GetPlexItems(ctx, lib.Key, false)
		if err != nil {
			return nil, fmt.Errorf("failed to get library items: %w", err)
		}

		for _, item := range items {
			// Skip shows with the anime genre
			isAnime := false
			for _, genre := range item.Genre {
				if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
					isAnime = true
					break
				}
			}

			if isAnime {
				continue
			}

			year := 0
			if item.Year != nil {
				year = *item.Year
			}

			rating := 0.0
			if item.Rating != nil {
				rating = *item.Rating
			}

			genre := ""
			if len(item.Genre) > 0 && item.Genre[0].Tag != nil {
				genre = *item.Genre[0].Tag
			}

			seasons := 0
			if item.ChildCount != nil {
				seasons = *item.ChildCount
			}

			thumb := ""
			if item.Thumb != nil {
				// Search for the TV show in TMDB to get the poster URL
				result, err := c.tmdb.SearchTVShow(ctx, item.Title, year)
				if err == nil && len(result.Results) > 0 {
					thumb = c.tmdb.GetPosterURL(result.Results[0].PosterPath)
				}
			}

			shows = append(shows, models.Recommendation{
				Title:     item.Title,
				Type:      "tvshow",
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: thumb,
				Runtime:   seasons,
				Source:    "plex",
			})
		}
	}
	return shows, nil
}
