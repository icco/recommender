package plex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

// Client represents a Plex API client that handles communication with a Plex server.
// It provides methods for retrieving library information and media items.
type Client struct {
	api       *plexgo.PlexAPI
	plexURL   string
	logger    *slog.Logger
	db        *gorm.DB
	plexToken string
	tmdb      *tmdb.Client
}

const (
	fallbackPosterURL = "https://via.placeholder.com/500x750?text=No+Poster+Available"
)

// NewClient creates a new Plex client with the provided configuration.
// It initializes the Plex API client with the given URL and authentication token.
func NewClient(plexURL, plexToken string, logger *slog.Logger, db *gorm.DB, tmdbClient *tmdb.Client) *Client {
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
		tmdb:      tmdbClient,
	}
}

// GetAPI returns the underlying Plex API instance for direct access to Plex API methods.
func (c *Client) GetAPI() *plexgo.PlexAPI {
	return c.api
}

// GetURL returns the Plex server URL used by this client.
func (c *Client) GetURL() string {
	return c.plexURL
}

// resolvePosterURL returns an absolute URL for HTML img src. Plex often returns relative thumb paths.
func (c *Client) resolvePosterURL(thumb string) string {
	if thumb == "" {
		return fallbackPosterURL
	}
	if strings.HasPrefix(thumb, "http://") || strings.HasPrefix(thumb, "https://") {
		return thumb
	}
	base := strings.TrimRight(c.plexURL, "/")
	if strings.HasPrefix(thumb, "/") {
		return base + thumb
	}
	return base + "/" + thumb
}

// GetLibrary returns the Library API instance for accessing Plex library operations.
func (c *Client) GetLibrary() *plexgo.Library {
	return c.api.Library
}

// GetAllLibraries retrieves all libraries from the Plex server.
// It returns detailed information about each library, including its type, title, and configuration.
func (c *Client) GetAllLibraries(ctx context.Context) (*operations.GetSectionsResponse, error) {
	c.logger.Debug("Fetching libraries from Plex", slog.String("url", c.plexURL))

	// Avoid plexgo JSON decode: some PMS versions return numeric 0/1 for fields typed as bool.
	resp, err := c.fetchLibrarySectionsViaJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get libraries: %w", err)
	}

	if resp.Object == nil || resp.Object.MediaContainer == nil {
		return nil, fmt.Errorf("invalid response from Plex API")
	}

	// Log available libraries
	var libraryInfo []map[string]any
	for _, lib := range resp.Object.MediaContainer.Directory {
		libraryInfo = append(libraryInfo, map[string]any{
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
		slog.Any("media_container", map[string]any{
			"title1":     resp.Object.MediaContainer.Title1,
			"allow_sync": resp.Object.MediaContainer.AllowSync,
		}))

	return resp, nil
}

// PlexItem represents a media item from Plex
type PlexItem struct {
	RatingKey  string
	Key        string
	Title      string
	Type       string
	Year       *int
	Rating     *float64
	Summary    string
	Thumb      *string
	Art        *string
	Duration   *int
	AddedAt    int64
	UpdatedAt  *int64
	ViewCount  *int
	Genre      []components.Tag
	LeafCount  *int
	ChildCount *int
}

// GetPlexItems retrieves items from a specific Plex library.
// It supports pagination and filtering for unwatched items only.
func (c *Client) GetPlexItems(ctx context.Context, libraryKey string, unwatchedOnly bool) ([]PlexItem, error) {
	c.logger.Debug("Getting library details from Plex API",
		slog.String("section_key", libraryKey))

	// Same tolerant JSON path as GetAllLibraries (plexgo fails on 0/1 bool fields).
	rawItems, err := c.fetchLibraryItemsViaJSON(ctx, libraryKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get library details: %w", err)
	}

	c.logger.Debug("Got library details from Plex API",
		slog.Int("directory_count", len(rawItems)))

	var allItems []PlexItem
	for _, item := range rawItems {
		if unwatchedOnly && item.ViewCount != nil && *item.ViewCount > 0 {
			continue
		}
		allItems = append(allItems, item)
	}

	return allItems, nil
}

// GetUnwatchedMovies retrieves all unwatched movies from Plex libraries.
// It converts the Plex items into Recommendation models for use in the recommendation system.
func (c *Client) GetUnwatchedMovies(ctx context.Context, libraries []components.LibrarySection) ([]models.Recommendation, error) {
	var movies []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != components.MediaTypeStringMovie {
			continue
		}

		key := ""
		if lib.Key != nil {
			key = *lib.Key
		}

		items, err := c.GetPlexItems(ctx, key, true)
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
			if len(item.Genre) > 0 {
				genre = item.Genre[0].Tag
			}

			duration := 0
			if item.Duration != nil {
				duration = *item.Duration / 60000 // Convert milliseconds to minutes
			}

			thumb := ""
			if item.Thumb != nil {
				thumb = *item.Thumb
			}
			posterURL := c.resolvePosterURL(thumb)

			movies = append(movies, models.Recommendation{
				Title:     item.Title,
				Type:      string(components.MediaTypeStringMovie),
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: posterURL,
				Runtime:   duration,
			})
		}
	}
	return movies, nil
}

// GetUnwatchedTVShows retrieves all unwatched TV shows from Plex libraries.
// It converts the items into Recommendation models.
func (c *Client) GetUnwatchedTVShows(ctx context.Context, libraries []components.LibrarySection) ([]models.Recommendation, error) {
	var shows []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != components.MediaTypeStringTvShow {
			continue
		}

		key := ""
		if lib.Key != nil {
			key = *lib.Key
		}

		items, err := c.GetPlexItems(ctx, key, true)
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
			if len(item.Genre) > 0 {
				genre = item.Genre[0].Tag
			}

			seasons := 0
			if item.ChildCount != nil {
				seasons = *item.ChildCount
			}

			thumb := ""
			if item.Thumb != nil {
				thumb = *item.Thumb
			}
			posterURL := c.resolvePosterURL(thumb)

			shows = append(shows, models.Recommendation{
				Title:     item.Title,
				Type:      string(components.MediaTypeStringTvShow),
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: posterURL,
				Runtime:   seasons,
			})
		}
	}
	return shows, nil
}

// UpdateCache updates the Plex cache by fetching all libraries and their items.
// It clears existing cache entries and populates them with the latest data from Plex.
func (c *Client) UpdateCache(ctx context.Context) error {
	c.logger.Info("Starting cache update")

	// Create a new context with a timeout of 15 minutes (for large libraries)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	// Get all libraries
	c.logger.Info("Fetching all libraries")
	libraries, err := c.GetAllLibraries(ctx)
	if err != nil {
		c.logger.Error("Failed to get libraries", slog.Any("error", err))
		return fmt.Errorf("failed to get libraries: %w", err)
	}
	c.logger.Info("Successfully fetched libraries", slog.Int("count", len(libraries.Object.MediaContainer.Directory)))

	// Get all content from each library
	var allMovies []PlexItem
	var allTVShows []PlexItem

	for _, lib := range libraries.Object.MediaContainer.Directory {
		key := ""
		if lib.Key != nil {
			key = *lib.Key
		}

		items, err := c.GetPlexItems(ctx, key, false) // false means get all content, not just unwatched
		if err != nil {
			title := ""
			if lib.Title != nil {
				title = *lib.Title
			}
			c.logger.Error("Failed to get items from library",
				slog.String("library", title),
				slog.Any("error", err))
			continue
		}

		for _, item := range items {
			switch item.Type {
			case string(components.MediaTypeStringMovie):
				allMovies = append(allMovies, item)
			case string(components.MediaTypeStringTvShow):
				allTVShows = append(allTVShows, item)
			}
		}
	}

	c.logger.Info("Successfully fetched movies", slog.Int("count", len(allMovies)))
	c.logger.Info("Successfully fetched TV shows", slog.Int("count", len(allTVShows)))

	// Ensure the tables exist first (outside transaction)
	if err := c.db.WithContext(ctx).AutoMigrate(&models.Movie{}, &models.TVShow{}); err != nil {
		return fmt.Errorf("failed to ensure tables exist: %w", err)
	}

	// Clear existing cache entries in a separate transaction
	if err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1=1").Delete(&models.Movie{}).Error; err != nil {
			return fmt.Errorf("failed to clear existing movies: %w", err)
		}
		if err := tx.Where("1=1").Delete(&models.TVShow{}).Error; err != nil {
			return fmt.Errorf("failed to clear existing TV shows: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to clear existing cache: %w", err)
	}

	// Process movies in batches
	const batchSize = 50
	for i := 0; i < len(allMovies); i += batchSize {
		end := i + batchSize
		if end > len(allMovies) {
			end = len(allMovies)
		}
		
		batch := allMovies[i:end]
		if err := c.processMovieBatch(ctx, batch); err != nil {
			return fmt.Errorf("failed to process movie batch %d-%d: %w", i, end, err)
		}
	}

	// Process TV shows in batches
	for i := 0; i < len(allTVShows); i += batchSize {
		end := i + batchSize
		if end > len(allTVShows) {
			end = len(allTVShows)
		}
		
		batch := allTVShows[i:end]
		if err := c.processTVShowBatch(ctx, batch); err != nil {
			return fmt.Errorf("failed to process TV show batch %d-%d: %w", i, end, err)
		}
	}

	c.logger.Info("Successfully updated cache")
	return nil
}

// processMovieBatch processes a batch of movies in a single transaction
func (c *Client) processMovieBatch(ctx context.Context, movies []PlexItem) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range movies {
			year := 0
			if item.Year != nil {
				year = *item.Year
			}

			rating := 0.0
			if item.Rating != nil {
				rating = *item.Rating
			}

			genre := ""
			if len(item.Genre) > 0 {
				genre = item.Genre[0].Tag
			}

			runtime := 0
			if item.Duration != nil {
				runtime = *item.Duration / 60000 // Convert milliseconds to minutes
			}

			viewCount := 0
			if item.ViewCount != nil {
				viewCount = *item.ViewCount
			}

			thumb := ""
			if item.Thumb != nil {
				thumb = *item.Thumb
			}
			posterURL := c.resolvePosterURL(thumb)

			movie := models.Movie{
				Title:     item.Title,
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: posterURL,
				Runtime:   runtime,
				TMDbID:    nil,
				ViewCount: viewCount,
			}

			if err := tx.Create(&movie).Error; err != nil {
				return fmt.Errorf("failed to create movie: %w", err)
			}
		}
		return nil
	})
}

// processTVShowBatch processes a batch of TV shows in a single transaction
func (c *Client) processTVShowBatch(ctx context.Context, shows []PlexItem) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range shows {
			year := 0
			if item.Year != nil {
				year = *item.Year
			}

			rating := 0.0
			if item.Rating != nil {
				rating = *item.Rating
			}

			genre := ""
			if len(item.Genre) > 0 {
				genre = item.Genre[0].Tag
			}

			seasons := 0
			if item.ChildCount != nil {
				seasons = *item.ChildCount
			}

			viewCount := 0
			if item.ViewCount != nil {
				viewCount = *item.ViewCount
			}

			thumb := ""
			if item.Thumb != nil {
				thumb = *item.Thumb
			}
			posterURL := c.resolvePosterURL(thumb)

			tvShow := models.TVShow{
				Title:     item.Title,
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: posterURL,
				Seasons:   seasons,
				TMDbID:    nil,
				ViewCount: viewCount,
			}

			if err := tx.Create(&tvShow).Error; err != nil {
				return fmt.Errorf("failed to create TV show: %w", err)
			}
		}
		return nil
	})
}
