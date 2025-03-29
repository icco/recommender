package plex

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"log/slog"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
)

type Client struct {
	api     *plexgo.PlexAPI
	plexURL string
	logger  *slog.Logger
}

func NewClient(plexURL, plexToken string, logger *slog.Logger) *Client {
	plex := plexgo.New(
		plexgo.WithSecurity(plexToken),
		plexgo.WithServerURL(plexURL),
	)

	return &Client{
		api:     plex,
		plexURL: plexURL,
		logger:  logger,
	}
}

// GetAPI returns the underlying Plex API instance
func (c *Client) GetAPI() *plexgo.PlexAPI {
	return c.api
}

// GetURL returns the Plex server URL
func (c *Client) GetURL() string {
	return c.plexURL
}

// GetLibrary returns the Library API
func (c *Client) GetLibrary() *plexgo.Library {
	return c.api.Library
}

// GetAllLibraries gets all libraries from Plex
func (c *Client) GetAllLibraries(ctx context.Context) (*operations.GetAllLibrariesResponse, error) {
	return c.api.Library.GetAllLibraries(ctx)
}

// getPlexLibraryKey finds the library key for a given type and title condition
func getPlexLibraryKey(libraries []operations.GetAllLibrariesDirectory, libType string, titleCondition func(string) bool) (string, error) {
	for _, lib := range libraries {
		if lib.Type == libType && (titleCondition == nil || titleCondition(lib.Title)) {
			return lib.Key, nil
		}
	}
	return "", fmt.Errorf("no matching library found")
}

// GetPlexItems gets items from a Plex library
func (c *Client) GetPlexItems(ctx context.Context, libraryKey string) (*operations.GetLibraryItemsResponse, error) {
	sectionKey, err := strconv.Atoi(libraryKey)
	if err != nil {
		return nil, fmt.Errorf("invalid library key: %v", err)
	}

	return c.api.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		SectionKey: sectionKey,
		Tag:        "all",
	})
}

// Helper functions for Plex data extraction
func getIntValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func getFloatValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func getStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func getGenres(genres []operations.GetLibraryItemsGenre) string {
	var genreStrings []string
	for _, g := range genres {
		if g.Tag != nil {
			genreStrings = append(genreStrings, *g.Tag)
		}
	}
	return strings.Join(genreStrings, ", ")
}

// GetUnwatchedMovies gets unwatched movies from Plex
func (c *Client) GetUnwatchedMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Movie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.GetPlexItems(ctx, movieLibraryKey)
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

	items, err := c.GetPlexItems(ctx, animeLibraryKey)
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

	items, err := c.GetPlexItems(ctx, tvLibraryKey)
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
