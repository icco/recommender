package plex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

type Client struct {
	api       *plexgo.PlexAPI
	plexURL   string
	logger    *slog.Logger
	db        *gorm.DB
	plexToken string
}

func NewClient(plexURL, plexToken string, logger *slog.Logger, db *gorm.DB) *Client {
	plex := plexgo.New(
		plexgo.WithSecurity(plexToken),
		plexgo.WithServerURL(plexURL),
	)

	return &Client{
		api:       plex,
		plexURL:   plexURL,
		logger:    logger,
		db:        db,
		plexToken: plexToken,
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
	c.logger.Debug("Fetching libraries from Plex", slog.String("url", c.plexURL))

	resp, err := c.api.Library.GetAllLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get libraries: %w", err)
	}

	if resp.Object == nil {
		return nil, fmt.Errorf("invalid response from Plex API")
	}

	// Log available libraries
	var libraryInfo []map[string]interface{}
	for _, lib := range resp.Object.MediaContainer.Directory {
		libraryInfo = append(libraryInfo, map[string]interface{}{
			"key":      lib.Key,
			"type":     lib.Type,
			"title":    lib.Title,
			"agent":    lib.Agent,
			"scanner":  lib.Scanner,
			"language": lib.Language,
			"uuid":     lib.UUID,
		})
	}

	c.logger.Debug("Got libraries from Plex",
		slog.Int("count", len(resp.Object.MediaContainer.Directory)),
		slog.Any("libraries", libraryInfo),
		slog.Any("media_container", map[string]interface{}{
			"title1":     resp.Object.MediaContainer.Title1,
			"allow_sync": resp.Object.MediaContainer.AllowSync,
		}))

	return resp, nil
}

// getPlexLibraryKey finds the library key for a given type and title condition
func getPlexLibraryKey(libraries []operations.GetAllLibrariesDirectory, libType string, titleCondition func(string) bool) (string, error) {
	for _, lib := range libraries {
		if lib.Type == libType && (titleCondition == nil || titleCondition(lib.Title)) {
			return lib.Key, nil
		}
	}
	return "", fmt.Errorf("no matching library found for type %s", libType)
}

// GetPlexItems gets items from a Plex library
func (c *Client) GetPlexItems(ctx context.Context, libraryKey string) (*operations.GetLibraryItemsResponse, error) {
	sectionKey, err := strconv.Atoi(libraryKey)
	if err != nil {
		return nil, fmt.Errorf("invalid library key: %w", err)
	}

	c.logger.Debug("Getting items from library",
		slog.String("library_key", libraryKey),
		slog.Int("section_key", sectionKey),
		slog.String("url", c.plexURL))

	// Set up pagination parameters
	containerSize := int(50)
	containerStart := int(0)

	// Try different parameter combinations
	includeGuids1 := operations.IncludeGuids(1)
	includeMeta1 := operations.GetLibraryItemsQueryParamIncludeMeta(1)

	// Make a single request with the most reliable parameters
	req := operations.GetLibraryItemsRequest{
		SectionKey:          sectionKey,
		Type:                operations.GetLibraryItemsQueryParamType(0), // Use type 0 for all items
		IncludeGuids:        &includeGuids1,
		IncludeMeta:         &includeMeta1,
		XPlexContainerSize:  &containerSize,
		XPlexContainerStart: &containerStart,
	}

	c.logger.Debug("Making request to Plex API",
		slog.Any("request", req),
		slog.Int("container_size", containerSize),
		slog.Int("container_start", containerStart))

	resp, err := c.api.Library.GetLibraryItems(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get library items: %w", err)
	}

	if resp.Object == nil || resp.Object.MediaContainer == nil {
		return nil, fmt.Errorf("invalid response from Plex API")
	}

	// Log more detailed information about the response
	c.logger.Debug("Got response from Plex API",
		slog.Int("total_size", resp.Object.MediaContainer.TotalSize),
		slog.Int("size", resp.Object.MediaContainer.Size),
		slog.Int("metadata_count", len(resp.Object.MediaContainer.Metadata)),
		slog.String("title1", resp.Object.MediaContainer.Title1),
		slog.String("title2", resp.Object.MediaContainer.Title2),
		slog.String("identifier", resp.Object.MediaContainer.Identifier),
		slog.String("library_section_id", strconv.FormatInt(resp.Object.MediaContainer.LibrarySectionID, 10)),
		slog.String("library_section_title", resp.Object.MediaContainer.LibrarySectionTitle),
		slog.String("library_section_uuid", resp.Object.MediaContainer.LibrarySectionUUID),
		slog.Bool("allow_sync", resp.Object.MediaContainer.AllowSync),
		slog.String("content", resp.Object.MediaContainer.Content),
		slog.String("view_group", resp.Object.MediaContainer.ViewGroup))

	// If we got a response with items, return it
	if len(resp.Object.MediaContainer.Metadata) > 0 {
		return resp, nil
	}

	// If we got a response with a total size but no items, try the next page
	if resp.Object.MediaContainer.TotalSize > 0 {
		c.logger.Debug("Got total size but no items on current page, trying next page",
			slog.Int("total_size", resp.Object.MediaContainer.TotalSize),
			slog.Int("next_start", containerStart+containerSize))
		containerStart += containerSize

		// Make another request with the next page
		req.XPlexContainerStart = &containerStart
		resp, err = c.api.Library.GetLibraryItems(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to get next page: %w", err)
		}

		if len(resp.Object.MediaContainer.Metadata) > 0 {
			return resp, nil
		}
	}

	return nil, fmt.Errorf("no items found in library")
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
				BaseMedia: models.BaseMedia{
					Title:     item.Title,
					Year:      getIntValue(item.Year),
					Rating:    getFloatValue(item.Rating),
					Genre:     getGenres(item.Genre),
					PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
					Source:    "plex",
				},
				Runtime: getIntValue(item.Duration) / 60000,
			}
			unwatchedMovies = append(unwatchedMovies, movie)
		}
	}

	return unwatchedMovies, nil
}

// GetUnwatchedAnime gets unwatched anime from Plex
func (c *Client) GetUnwatchedAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Anime, error) {
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

	var unwatchedAnime []models.Anime
	for _, item := range items.Object.MediaContainer.Metadata {
		// Check if the show has the anime genre
		isAnime := false
		for _, genre := range item.Genre {
			if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
				isAnime = true
				break
			}
		}

		if isAnime && item.ViewCount != nil && *item.ViewCount == 0 {
			anime := models.Anime{
				BaseMedia: models.BaseMedia{
					Title:     item.Title,
					Year:      getIntValue(item.Year),
					Rating:    getFloatValue(item.Rating),
					Genre:     getGenres(item.Genre),
					PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
					Source:    "plex",
				},
				Episodes: getIntValue(item.LeafCount),
			}
			unwatchedAnime = append(unwatchedAnime, anime)
		}
	}

	c.logger.Debug("Found unwatched anime",
		slog.Int("count", len(unwatchedAnime)),
		slog.String("library_key", animeLibraryKey))

	return unwatchedAnime, nil
}

// GetUnwatchedTVShows gets unwatched TV shows from Plex
func (c *Client) GetUnwatchedTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.TVShow, error) {
	// First get the TV library
	tvLibraryKey, err := getPlexLibraryKey(libraries, "show", nil)
	if err != nil {
		return nil, err
	}

	items, err := c.GetPlexItems(ctx, tvLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedTVShows []models.TVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		// Skip shows with the anime genre
		isAnime := false
		for _, genre := range item.Genre {
			if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
				isAnime = true
				break
			}
		}

		if !isAnime && item.ViewCount != nil && *item.ViewCount == 0 {
			tvShow := models.TVShow{
				BaseMedia: models.BaseMedia{
					Title:     item.Title,
					Year:      getIntValue(item.Year),
					Rating:    getFloatValue(item.Rating),
					Genre:     getGenres(item.Genre),
					PosterURL: fmt.Sprintf("%s%s", c.plexURL, getStringValue(item.Thumb)),
					Source:    "plex",
				},
				Seasons: getIntValue(item.ChildCount),
			}
			unwatchedTVShows = append(unwatchedTVShows, tvShow)
		}
	}

	return unwatchedTVShows, nil
}

// UpdateCache updates the Plex cache by fetching all libraries and their items
func (c *Client) UpdateCache(ctx context.Context) error {
	c.logger.Info("Starting cache update")

	// Create a new context with a timeout of 30 seconds
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Test connection and library access first
	c.logger.Info("Testing Plex connection and library access")
	if err := c.TestConnection(ctx); err != nil {
		c.logger.Error("Connection test failed", slog.Any("error", err))
		return fmt.Errorf("connection test failed: %w", err)
	}
	c.logger.Info("Connection test successful")

	// Get all libraries
	c.logger.Info("Fetching all libraries")
	libraries, err := c.GetAllLibraries(ctx)
	if err != nil {
		c.logger.Error("Failed to get libraries", slog.Any("error", err))
		return fmt.Errorf("failed to get libraries: %w", err)
	}
	c.logger.Info("Successfully fetched libraries", slog.Int("count", len(libraries.Object.MediaContainer.Directory)))

	// Get all content (including watched items)
	movies, err := c.GetAllMovies(ctx, libraries.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get movies: %w", err)
	}
	c.logger.Debug("Fetched movies from Plex", slog.Int("count", len(movies)))

	anime, err := c.GetAllAnime(ctx, libraries.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get anime: %w", err)
	}
	c.logger.Debug("Fetched anime from Plex", slog.Int("count", len(anime)))

	tvShows, err := c.GetAllTVShows(ctx, libraries.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get TV shows: %w", err)
	}
	c.logger.Debug("Fetched TV shows from Plex", slog.Int("count", len(tvShows)))

	// Start a transaction to ensure all operations succeed or none do
	tx := c.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// Clear existing cache entries and their associations
	if err := tx.Exec("DELETE FROM plex_cache_movies").Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear movie associations: %w", err)
	}
	if err := tx.Exec("DELETE FROM plex_cache_anime").Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear anime associations: %w", err)
	}
	if err := tx.Exec("DELETE FROM plex_cache_tvshows").Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear TV show associations: %w", err)
	}
	if err := tx.Where("1 = 1").Delete(&models.PlexCache{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear existing cache: %w", err)
	}

	// Save movies
	if len(movies) > 0 {
		c.logger.Debug("Saving movies to database", slog.Int("count", len(movies)))
		if err := tx.CreateInBatches(movies, 100).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to save movies: %w", err)
		}
		c.logger.Debug("Successfully saved movies")
	}

	// Save anime
	if len(anime) > 0 {
		c.logger.Debug("Saving anime to database", slog.Int("count", len(anime)))
		if err := tx.CreateInBatches(anime, 100).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to save anime: %w", err)
		}
		c.logger.Debug("Successfully saved anime")
	}

	// Save TV shows
	if len(tvShows) > 0 {
		c.logger.Debug("Saving TV shows to database", slog.Int("count", len(tvShows)))
		if err := tx.CreateInBatches(tvShows, 100).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to save TV shows: %w", err)
		}
		c.logger.Debug("Successfully saved TV shows")
	}

	// Create a new cache entry with associations
	cache := &models.PlexCache{
		UpdatedAt: time.Now(),
		Movies:    movies,
		Anime:     anime,
		TVShows:   tvShows,
	}

	// Save the cache entry with associations
	c.logger.Debug("Creating new cache entry with associations")
	if err := tx.Create(cache).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to save cache: %w", err)
	}
	c.logger.Debug("Successfully created cache entry", slog.Int("cache_id", int(cache.ID)))

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	c.logger.Debug("Successfully committed transaction")

	c.logger.Info("Cache updated successfully",
		slog.Int("movies", len(movies)),
		slog.Int("anime", len(anime)),
		slog.Int("tv_shows", len(tvShows)),
	)

	return nil
}

// TestConnection tests the Plex connection and token access
func (c *Client) TestConnection(ctx context.Context) error {
	// Test basic connection
	c.logger.Debug("Testing Plex connection", slog.String("url", c.plexURL))

	// Get libraries to test token
	libraries, err := c.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	// Test access to each library
	for _, lib := range libraries.Object.MediaContainer.Directory {
		c.logger.Debug("Testing library access",
			slog.String("title", lib.Title),
			slog.String("type", lib.Type),
			slog.String("key", lib.Key))

		items, err := c.GetPlexItems(ctx, lib.Key)
		if err != nil {
			return fmt.Errorf("failed to access library %s: %w", lib.Title, err)
		}

		c.logger.Debug("Library access successful",
			slog.String("title", lib.Title),
			slog.Int("item_count", len(items.Object.MediaContainer.Metadata)))
	}

	return nil
}
