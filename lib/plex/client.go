package plex

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"log/slog"

	"github.com/LukeHagar/plexgo"
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
	contentTypeMovie  = "movie"
	contentTypeShow   = "show"
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

// GetLibrary returns the Library API instance for accessing Plex library operations.
func (c *Client) GetLibrary() *plexgo.Library {
	return c.api.Library
}

// GetAllLibraries retrieves all libraries from the Plex server.
// It returns detailed information about each library, including its type, title, and configuration.
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
	Type       operations.GetLibraryItemsLibraryType
	Year       *int
	Rating     *float64
	Summary    string
	Thumb      *string
	Art        *string
	Duration   *int
	AddedAt    int64
	UpdatedAt  *int64
	ViewCount  *int
	Genre      []operations.GetLibraryItemsGenre
	LeafCount  *int
	ChildCount *int
}

// GetPlexItems retrieves items from a specific Plex library.
// It supports pagination and filtering for unwatched items only.
func (c *Client) GetPlexItems(ctx context.Context, libraryKey string, unwatchedOnly bool) ([]PlexItem, error) {
	// Convert library key to integer
	sectionKey, err := strconv.Atoi(libraryKey)
	if err != nil {
		return nil, fmt.Errorf("invalid library key: %w", err)
	}

	// Set up pagination parameters
	containerSize := 50 // Reduced for better reliability
	containerStart := 0

	// Set up common parameters
	includeGuids1 := operations.IncludeGuids(1)
	includeMeta1 := operations.GetLibraryItemsQueryParamIncludeMeta(1)

	// Get library type first
	libraries, err := c.GetAllLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get libraries: %w", err)
	}

	var libraryType operations.GetLibraryItemsQueryParamType
	var librarySection string
	for _, lib := range libraries.Object.MediaContainer.Directory {
		if lib.Key == libraryKey {
			librarySection = lib.Type
			switch lib.Type {
			case contentTypeMovie:
				libraryType = operations.GetLibraryItemsQueryParamType(1)
			case contentTypeShow:
				libraryType = operations.GetLibraryItemsQueryParamType(2)
			case "artist":
				libraryType = operations.GetLibraryItemsQueryParamType(8)
			default:
				return nil, fmt.Errorf("unsupported library type: %s", lib.Type)
			}
		}
	}

	var allItems []PlexItem
	for {
		// Make request to Plex API with reliable parameters
		request := operations.GetLibraryItemsRequest{
			SectionKey:          sectionKey,
			Type:                libraryType,
			IncludeGuids:        &includeGuids1,
			IncludeMeta:         &includeMeta1,
			XPlexContainerSize:  &containerSize,
			XPlexContainerStart: &containerStart,
			Tag:                 operations.Tag("all"),
		}

		c.logger.Debug("Making request to Plex API",
			slog.Any("request", request),
			slog.Int("container_size", containerSize),
			slog.Int("container_start", containerStart),
			slog.String("library_type", librarySection),
			slog.String("section_key", libraryKey))

		resp, err := c.api.Library.GetLibraryItems(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("failed to get items from library: %w", err)
		}

		c.logger.Debug("Got response from Plex API",
			slog.Int("total_size", int(resp.Object.MediaContainer.TotalSize)),
			slog.Int("size", int(resp.Object.MediaContainer.Size)),
			slog.Int("metadata_count", len(resp.Object.MediaContainer.Metadata)),
			slog.String("title1", resp.Object.MediaContainer.Title1),
			slog.String("title2", resp.Object.MediaContainer.Title2),
			slog.String("identifier", resp.Object.MediaContainer.Identifier),
			slog.String("library_section_id", strconv.FormatInt(resp.Object.MediaContainer.LibrarySectionID, 10)),
			slog.String("library_section_title", resp.Object.MediaContainer.LibrarySectionTitle),
			slog.String("library_section_uuid", resp.Object.MediaContainer.LibrarySectionUUID),
			slog.Bool("allow_sync", resp.Object.MediaContainer.AllowSync),
			slog.String("content", resp.Object.MediaContainer.Content),
			slog.String("view_group", resp.Object.MediaContainer.ViewGroup),
			slog.Any("metadata_fields", func() []map[string]any {
				var fields []map[string]any
				for _, item := range resp.Object.MediaContainer.Metadata {
					fields = append(fields, map[string]any{
						"title":       item.Title,
						"type":        item.Type,
						"year":        item.Year,
						"rating":      item.Rating,
						"summary":     item.Summary,
						"view_count":  item.ViewCount,
						"genre":       item.Genre,
						"leaf_count":  item.LeafCount,
						"child_count": item.ChildCount,
					})
				}
				return fields
			}()))

		// Convert response to slice of PlexItem
		for _, item := range resp.Object.MediaContainer.Metadata {
			// Skip watched items if unwatchedOnly is true
			if unwatchedOnly && item.ViewCount != nil && *item.ViewCount > 0 {
				continue
			}

			allItems = append(allItems, PlexItem{
				RatingKey:  item.RatingKey,
				Key:        item.Key,
				Title:      item.Title,
				Type:       item.Type,
				Year:       item.Year,
				Rating:     item.Rating,
				Summary:    item.Summary,
				Thumb:      item.Thumb,
				Art:        item.Art,
				Duration:   item.Duration,
				AddedAt:    item.AddedAt,
				UpdatedAt:  item.UpdatedAt,
				ViewCount:  item.ViewCount,
				Genre:      item.Genre,
				LeafCount:  item.LeafCount,
				ChildCount: item.ChildCount,
			})
		}

		// Check if we've retrieved all items
		if len(resp.Object.MediaContainer.Metadata) == 0 ||
			containerStart+len(resp.Object.MediaContainer.Metadata) >= int(resp.Object.MediaContainer.TotalSize) {
			break
		}

		// Move to next page
		containerStart += containerSize
	}

	return allItems, nil
}

// GetUnwatchedMovies retrieves all unwatched movies from Plex libraries.
// It converts the Plex items into Recommendation models for use in the recommendation system.
func (c *Client) GetUnwatchedMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Recommendation, error) {
	var movies []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != contentTypeMovie {
			continue
		}

		items, err := c.GetPlexItems(ctx, lib.Key, true)
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

			// Try to get TMDb poster
			posterURL := fallbackPosterURL
			if year > 0 {
				result, err := c.tmdb.SearchMovie(ctx, item.Title, year)
				if err != nil {
					c.logger.Warn("Failed to search TMDb for movie",
						slog.String("title", item.Title),
						slog.Int("year", year),
						slog.Any("error", err))
				} else if len(result.Results) > 0 {
					// Use the first result's poster if available
					if result.Results[0].PosterPath != "" {
						tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
						if _, err := url.Parse(tmdbPosterURL); err != nil {
							c.logger.Warn("Invalid TMDb poster URL",
								slog.String("url", tmdbPosterURL),
								slog.Any("error", err))
						} else {
							posterURL = tmdbPosterURL
						}
					}
				}
			}

			movies = append(movies, models.Recommendation{
				Title:     item.Title,
				Type:      contentTypeMovie,
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
func (c *Client) GetUnwatchedTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Recommendation, error) {
	var shows []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != contentTypeShow {
			continue
		}

		items, err := c.GetPlexItems(ctx, lib.Key, true)
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

			seasons := 0
			if item.ChildCount != nil {
				seasons = *item.ChildCount
			}

			// Try to get TMDb poster
			posterURL := fallbackPosterURL
			if year > 0 {
				result, err := c.tmdb.SearchTVShow(ctx, item.Title, year)
				if err != nil {
					// If search with year fails, try without year
					c.logger.Debug("Retrying TMDb search without year",
						slog.String("title", item.Title),
						slog.Int("original_year", year))

					result, err = c.tmdb.SearchTVShow(ctx, item.Title, 0)
					if err != nil {
						c.logger.Warn("Failed to search TMDb for TV show",
							slog.String("title", item.Title),
							slog.Any("error", err))
					} else if len(result.Results) > 0 {
						// Use the first result's poster if available
						if result.Results[0].PosterPath != "" {
							tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
							if _, err := url.Parse(tmdbPosterURL); err != nil {
								c.logger.Warn("Invalid TMDb poster URL",
									slog.String("url", tmdbPosterURL),
									slog.Any("error", err))
							} else {
								posterURL = tmdbPosterURL
							}
						}
					}
				} else if len(result.Results) > 0 {
					// Use the first result's poster if available
					if result.Results[0].PosterPath != "" {
						tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
						if _, err := url.Parse(tmdbPosterURL); err != nil {
							c.logger.Warn("Invalid TMDb poster URL",
								slog.String("url", tmdbPosterURL),
								slog.Any("error", err))
						} else {
							posterURL = tmdbPosterURL
						}
					}
				}
			}

			shows = append(shows, models.Recommendation{
				Title:     item.Title,
				Type:      contentTypeShow,
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

	// Create a new context with a timeout of 5 minutes
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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
		items, err := c.GetPlexItems(ctx, lib.Key, false) // false means get all content, not just unwatched
		if err != nil {
			c.logger.Error("Failed to get items from library",
				slog.String("library", lib.Title),
				slog.Any("error", err))
			continue
		}

		for _, item := range items {
			if item.Type == contentTypeMovie {
				allMovies = append(allMovies, item)
			} else if item.Type == contentTypeShow {
				allTVShows = append(allTVShows, item)
			}
		}
	}

	c.logger.Info("Successfully fetched movies", slog.Int("count", len(allMovies)))
	c.logger.Info("Successfully fetched TV shows", slog.Int("count", len(allTVShows)))

	// Start a transaction
	tx := c.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %w", tx.Error)
	}

	// Ensure the tables exist
	if err := tx.AutoMigrate(&models.Movie{}, &models.TVShow{}); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to ensure tables exist: %w", err)
	}

	// Clear existing cache entries
	if err := tx.Delete(&models.Movie{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear existing movies: %w", err)
	}
	if err := tx.Delete(&models.TVShow{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear existing TV shows: %w", err)
	}

	// Process movies one at a time
	for _, item := range allMovies {
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

		runtime := 0
		if item.Duration != nil {
			runtime = *item.Duration / 60000 // Convert milliseconds to minutes
		}

		// Try to get TMDb poster
		posterURL := fallbackPosterURL
		if year > 0 {
			result, err := c.tmdb.SearchMovie(ctx, item.Title, year)
			if err != nil {
				c.logger.Warn("Failed to search TMDb for movie",
					slog.String("title", item.Title),
					slog.Int("year", year),
					slog.Any("error", err))
			} else if len(result.Results) > 0 {
				// Use the first result's poster if available
				if result.Results[0].PosterPath != "" {
					tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
					if _, err := url.Parse(tmdbPosterURL); err != nil {
						c.logger.Warn("Invalid TMDb poster URL",
							slog.String("url", tmdbPosterURL),
							slog.Any("error", err))
					} else {
						posterURL = tmdbPosterURL
					}
				}
			}
		}

		// Create movie record
		movie := models.Movie{
			Title:     item.Title,
			Year:      year,
			Rating:    rating,
			Genre:     genre,
			PosterURL: posterURL,
			Runtime:   runtime,
			TMDbID:    0, // Will be updated later if needed
		}

		if err := tx.Create(&movie).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to create movie: %w", err)
		}
	}

	// Process TV shows one at a time
	for _, item := range allTVShows {
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

		// Try to get TMDb poster
		posterURL := fallbackPosterURL
		if year > 0 {
			result, err := c.tmdb.SearchTVShow(ctx, item.Title, year)
			if err != nil {
				// If search with year fails, try without year
				c.logger.Debug("Retrying TMDb search without year",
					slog.String("title", item.Title),
					slog.Int("original_year", year))

				result, err = c.tmdb.SearchTVShow(ctx, item.Title, 0)
				if err != nil {
					c.logger.Warn("Failed to search TMDb for TV show",
						slog.String("title", item.Title),
						slog.Any("error", err))
				} else if len(result.Results) > 0 {
					// Use the first result's poster if available
					if result.Results[0].PosterPath != "" {
						tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
						if _, err := url.Parse(tmdbPosterURL); err != nil {
							c.logger.Warn("Invalid TMDb poster URL",
								slog.String("url", tmdbPosterURL),
								slog.Any("error", err))
						} else {
							posterURL = tmdbPosterURL
						}
					}
				}
			} else if len(result.Results) > 0 {
				// Use the first result's poster if available
				if result.Results[0].PosterPath != "" {
					tmdbPosterURL := c.tmdb.GetPosterURL(result.Results[0].PosterPath)
					if _, err := url.Parse(tmdbPosterURL); err != nil {
						c.logger.Warn("Invalid TMDb poster URL",
							slog.String("url", tmdbPosterURL),
							slog.Any("error", err))
					} else {
						posterURL = tmdbPosterURL
					}
				}
			}
		}

		// Create TV show record
		tvShow := models.TVShow{
			Title:     item.Title,
			Year:      year,
			Rating:    rating,
			Genre:     genre,
			PosterURL: posterURL,
			Seasons:   seasons,
			TMDbID:    0, // Will be updated later if needed
		}

		if err := tx.Create(&tvShow).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to create TV show: %w", err)
		}
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	c.logger.Info("Successfully updated cache")
	return nil
}
