package plex

import (
	"context"
	"fmt"
	"strings"

	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
)

// GetAllMovies gets all movies from Plex
func (c *Client) GetAllMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexMovie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.GetPlexItems(ctx, movieLibraryKey, false)
	if err != nil {
		return nil, err
	}

	var movies []models.PlexMovie
	for _, item := range items {
		movie := models.PlexMovie{
			BaseMedia: models.BaseMedia{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
				Source:    "plex",
			},
			Runtime: getIntValue(item.Duration) / 60000,
			Watched: item.ViewCount != nil && *item.ViewCount > 0,
		}
		movies = append(movies, movie)
	}

	return movies, nil
}

// GetAllTVShows gets all TV shows and anime from Plex
func (c *Client) GetAllTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexTVShow, error) {
	// First try to find a library with "anime" in the title
	animeLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return strings.Contains(strings.ToLower(title), "anime")
	})

	// If no dedicated anime library found, try to find any TV library
	if err != nil {
		c.logger.Debug("No dedicated anime library found, searching in TV libraries")
		tvLibraryKey, err := getPlexLibraryKey(libraries, "show", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to find any TV library: %w", err)
		}
		animeLibraryKey = tvLibraryKey
	}

	items, err := c.GetPlexItems(ctx, animeLibraryKey, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get items from library: %w", err)
	}

	var tvShows []models.PlexTVShow
	for _, item := range items {
		// Check if the show has the anime genre
		isAnime := false
		for _, genre := range item.Genre {
			if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
				isAnime = true
				break
			}
		}

		tvShow := models.PlexTVShow{
			BaseMedia: models.BaseMedia{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
				Source:    "plex",
			},
			Seasons: getIntValue(item.ChildCount),
			Watched: item.ViewCount != nil && *item.ViewCount > 0,
			IsAnime: isAnime,
		}
		tvShows = append(tvShows, tvShow)
	}

	return tvShows, nil
}
