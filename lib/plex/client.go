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

// Client represents a Plex API client that handles communication with a Plex server.
// It provides methods for retrieving library information and media items.
type Client struct {
	api       *plexgo.PlexAPI
	plexURL   string
	logger    *slog.Logger
	db        *gorm.DB
	plexToken string
}

const (
	contentTypeMovie = "movie"
	contentTypeShow  = "show"
)

// NewClient creates a new Plex client with the provided configuration.
// It initializes the Plex API client with the given URL and authentication token.
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

// getPlexLibraryKey is intentionally kept for future use.
// It will be used to retrieve library keys based on type and title conditions.
func getPlexLibraryKey(libraries []operations.GetAllLibrariesDirectory, libType string, titleCondition func(string) bool) (string, error) {
	for _, lib := range libraries {
		if lib.Type == libType && (titleCondition == nil || titleCondition(lib.Title)) {
			return lib.Key, nil
		}
	}
	return "", fmt.Errorf("no matching library found for type %s", libType)
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
			slog.Any("metadata_fields", func() []map[string]interface{} {
				var fields []map[string]interface{}
				for _, item := range resp.Object.MediaContainer.Metadata {
					fields = append(fields, map[string]interface{}{
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

// getIntValue is intentionally kept for future use.
// It provides a safe way to get integer values from pointers.
func getIntValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// getFloatValue is intentionally kept for future use.
// It provides a safe way to get float values from pointers.
func getFloatValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// getStringValue is intentionally kept for future use.
// It provides a safe way to get string values from pointers.
func getStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// getGenres is intentionally kept for future use.
// It will be used to format genre information from Plex items.
func getGenres(genres []operations.GetLibraryItemsGenre) string {
	var genreStrings []string
	for _, g := range genres {
		if g.Tag != nil {
			genreStrings = append(genreStrings, *g.Tag)
		}
	}
	return strings.Join(genreStrings, ", ")
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

			thumb := ""
			if item.Thumb != nil {
				thumb = fmt.Sprintf("%s%s", c.plexURL, *item.Thumb)
			}

			movies = append(movies, models.Recommendation{
				Title:     item.Title,
				Type:      contentTypeMovie,
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

// GetUnwatchedAnime retrieves all unwatched anime from Plex libraries.
// It identifies anime by checking for the "anime" genre and converts them into Recommendation models.
func (c *Client) GetUnwatchedAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Recommendation, error) {
	var anime []models.Recommendation
	for _, lib := range libraries {
		if lib.Type != contentTypeShow {
			continue
		}

		items, err := c.GetPlexItems(ctx, lib.Key, true)
		if err != nil {
			return nil, fmt.Errorf("failed to get library items: %w", err)
		}

		for _, item := range items {
			// Check if the show has the anime genre
			isAnime := false
			for _, genre := range item.Genre {
				if genre.Tag != nil && strings.EqualFold(*genre.Tag, "anime") {
					isAnime = true
					break
				}
			}

			if !isAnime {
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

			episodes := 0
			if item.LeafCount != nil {
				episodes = *item.LeafCount
			}

			thumb := ""
			if item.Thumb != nil {
				thumb = fmt.Sprintf("%s%s", c.plexURL, *item.Thumb)
			}

			anime = append(anime, models.Recommendation{
				Title:     item.Title,
				Type:      contentTypeShow,
				Year:      year,
				Rating:    rating,
				Genre:     genre,
				PosterURL: thumb,
				Runtime:   episodes,
				Source:    "plex",
			})
		}
	}
	return anime, nil
}

// GetUnwatchedTVShows retrieves all unwatched TV shows from Plex libraries.
// It excludes anime shows and converts the items into Recommendation models.
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
				thumb = fmt.Sprintf("%s%s", c.plexURL, *item.Thumb)
			}

			shows = append(shows, models.Recommendation{
				Title:     item.Title,
				Type:      contentTypeShow,
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

	// Get all content
	movies, err := c.GetUnwatchedMovies(ctx, libraries.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get movies: %w", err)
	}
	c.logger.Debug("Fetched movies from Plex", slog.Int("count", len(movies)))

	tvShows, err := c.GetUnwatchedTVShows(ctx, libraries.Object.MediaContainer.Directory)
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

	// Ensure the tables exist
	if err := tx.WithContext(ctx).AutoMigrate(&models.Movie{}, &models.TVShow{}); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to ensure tables exist: %w", err)
	}

	// Clear existing cache entries
	if err := tx.WithContext(ctx).Where("source = ?", "plex").Delete(&models.Movie{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear existing movies: %w", err)
	}
	if err := tx.WithContext(ctx).Where("source = ?", "plex").Delete(&models.TVShow{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to clear existing TV shows: %w", err)
	}

	// Process movies one at a time
	if len(movies) > 0 {
		c.logger.Debug("Processing movies", slog.Int("total", len(movies)))
		for i, movie := range movies {
			// Log progress every 100 items
			if i%100 == 0 {
				c.logger.Debug("Processing movies progress",
					slog.Int("processed", i),
					slog.Int("total", len(movies)),
					slog.Float64("percent", float64(i)/float64(len(movies))*100))
			}

			// Convert to Movie model
			dbMovie := models.Movie{
				Title:     movie.Title,
				Year:      movie.Year,
				Rating:    movie.Rating,
				Genre:     movie.Genre,
				PosterURL: movie.PosterURL,
				Runtime:   movie.Runtime,
				Source:    movie.Source,
			}

			// Save the movie
			if err := tx.WithContext(ctx).Create(&dbMovie).Error; err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to save movie %d: %w", i, err)
			}
		}
		c.logger.Debug("Successfully processed movies", slog.Int("count", len(movies)))
	}

	// Process TV shows one at a time
	if len(tvShows) > 0 {
		c.logger.Debug("Processing TV shows", slog.Int("total", len(tvShows)))
		for i, tvShow := range tvShows {
			// Log progress every 100 items
			if i%100 == 0 {
				c.logger.Debug("Processing TV shows progress",
					slog.Int("processed", i),
					slog.Int("total", len(tvShows)),
					slog.Float64("percent", float64(i)/float64(len(tvShows))*100))
			}

			// Convert to TVShow model
			dbTVShow := models.TVShow{
				Title:     tvShow.Title,
				Year:      tvShow.Year,
				Rating:    tvShow.Rating,
				Genre:     tvShow.Genre,
				PosterURL: tvShow.PosterURL,
				Seasons:   tvShow.Runtime, // Runtime field contains seasons for TV shows
				Source:    tvShow.Source,
			}

			// Save the TV show
			if err := tx.WithContext(ctx).Create(&dbTVShow).Error; err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to save TV show %d: %w", i, err)
			}
		}
		c.logger.Debug("Successfully processed TV shows", slog.Int("count", len(tvShows)))
	}

	// Commit the transaction
	if err := tx.WithContext(ctx).Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	c.logger.Debug("Successfully committed transaction")

	c.logger.Info("Cache updated successfully",
		slog.Int("movies", len(movies)),
		slog.Int("tv_shows", len(tvShows)),
	)

	return nil
}
