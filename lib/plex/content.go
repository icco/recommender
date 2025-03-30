package plex

import (
	"context"
	"fmt"
	"strings"

	"log/slog"

	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
)

// GetAllMovies gets all movies from Plex
func (c *Client) GetAllMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexMovie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.GetPlexItems(ctx, movieLibraryKey)
	if err != nil {
		return nil, err
	}

	var movies []models.PlexMovie
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
		}

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
			Watched: watched,
		}
		movies = append(movies, movie)
	}

	return movies, nil
}

// GetAllAnime gets all anime from Plex
func (c *Client) GetAllAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexAnime, error) {
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

	items, err := c.GetPlexItems(ctx, animeLibraryKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get items from library: %w", err)
	}

	var anime []models.PlexAnime
	for _, item := range items.Object.MediaContainer.Metadata {
		// Check if the show has the anime genre
		isAnime := false
		for _, genre := range item.Genre {
			if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
				isAnime = true
				break
			}
		}

		if isAnime {
			watched := false
			if item.ViewCount != nil && *item.ViewCount > 0 {
				watched = true
			}

			animeItem := models.PlexAnime{
				BaseMedia: models.BaseMedia{
					Title:     item.Title,
					Year:      getIntValue(item.Year),
					Rating:    getFloatValue(item.Rating),
					Genre:     getGenres(item.Genre),
					PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
					Source:    "plex",
				},
				Episodes: getIntValue(item.LeafCount),
				Watched:  watched,
			}
			anime = append(anime, animeItem)
		}
	}

	c.logger.Debug("Found anime",
		slog.Int("count", len(anime)),
		slog.String("library_key", animeLibraryKey))

	return anime, nil
}

// GetAllTVShows gets all TV shows from Plex
func (c *Client) GetAllTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexTVShow, error) {
	tvLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return !strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := c.GetPlexItems(ctx, tvLibraryKey)
	if err != nil {
		return nil, err
	}

	var tvShows []models.PlexTVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
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
			Watched: watched,
		}
		tvShows = append(tvShows, tvShow)
	}

	return tvShows, nil
}
