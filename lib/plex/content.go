package plex

import (
	"context"
	"fmt"
	"strings"

	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
)

// GetUnwatchedMovies gets unwatched movies from Plex
func (c *Client) GetUnwatchedMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Movie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.getPlexItems(ctx, movieLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedMovies []models.Movie
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			movie := models.Movie{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Runtime:   getIntValue(item.Duration) / 60000,
				PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
				Source:    "plex",
			}
			unwatchedMovies = append(unwatchedMovies, movie)
		}
	}

	return unwatchedMovies, nil
}

// GetUnwatchedAnime gets unwatched anime from Plex
func (c *Client) GetUnwatchedAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Anime, error) {
	animeLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := c.getPlexItems(ctx, animeLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedAnime []models.Anime
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			anime := models.Anime{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Episodes:  getIntValue(item.LeafCount),
				PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
				Source:    "plex",
			}
			unwatchedAnime = append(unwatchedAnime, anime)
		}
	}

	return unwatchedAnime, nil
}

// GetUnwatchedTVShows gets unwatched TV shows from Plex
func (c *Client) GetUnwatchedTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.TVShow, error) {
	tvLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return !strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := c.getPlexItems(ctx, tvLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedTVShows []models.TVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			tvShow := models.TVShow{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Seasons:   getIntValue(item.ChildCount),
				PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
				Source:    "plex",
			}
			unwatchedTVShows = append(unwatchedTVShows, tvShow)
		}
	}

	return unwatchedTVShows, nil
}

// GetAllMovies gets all movies from Plex
func (c *Client) GetAllMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexMovie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.getPlexItems(ctx, movieLibraryKey)
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
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Runtime:   getIntValue(item.Duration) / 60000,
			PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
			Watched:   watched,
		}
		movies = append(movies, movie)
	}

	return movies, nil
}

// GetAllAnime gets all anime from Plex
func (c *Client) GetAllAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexAnime, error) {
	animeLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := c.getPlexItems(ctx, animeLibraryKey)
	if err != nil {
		return nil, err
	}

	var anime []models.PlexAnime
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
		}

		animeItem := models.PlexAnime{
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Episodes:  getIntValue(item.LeafCount),
			PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
			Watched:   watched,
		}
		anime = append(anime, animeItem)
	}

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

	items, err := c.getPlexItems(ctx, tvLibraryKey)
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
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Seasons:   getIntValue(item.ChildCount),
			PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
			Watched:   watched,
		}
		tvShows = append(tvShows, tvShow)
	}

	return tvShows, nil
}
