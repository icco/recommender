package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"log/slog"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// LibrarySectionInfo is the subset of Plex library section fields needed for cache updates.
// We decode via a minimal JSON shape so newer PMS builds that send 0/1 instead of JSON
// booleans for flags do not break unmarshaling (plexgo's full LibrarySection uses *bool).
type LibrarySectionInfo struct {
	Key      *string
	Title    *string
	Type     string
	Agent    *string
	Scanner  *string
	Language string
	UUID     string
}

// GetAllLibraries fetches library sections (GET /library/sections/all) with a minimal decoder.
func (c *Client) GetAllLibraries(ctx context.Context) ([]LibrarySectionInfo, error) {
	c.logger.Debug("Fetching libraries from Plex", slog.String("url", c.plexURL))

	base := strings.TrimRight(c.plexURL, "/")
	reqURL, err := url.JoinPath(base, "library", "sections", "all")
	if err != nil {
		return nil, fmt.Errorf("failed to build library sections URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.plexToken)
	req.Header.Set("User-Agent", "recommender")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get libraries: %w", err)
	}
	defer func() {
		if cerr := httpResp.Body.Close(); cerr != nil {
			c.logger.Debug("close Plex response body", slog.Any("error", cerr))
		}
	}()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Plex response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plex library sections: HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		MediaContainer *struct {
			Title1    *string `json:"title1"`
			Directory []struct {
				Key      *string `json:"key"`
				Title    *string `json:"title"`
				Type     string  `json:"type"`
				Agent    *string `json:"agent,omitempty"`
				Scanner  *string `json:"scanner,omitempty"`
				Language string  `json:"language"`
				UUID     string  `json:"uuid"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to get libraries: error unmarshaling json response body: %w", err)
	}
	if payload.MediaContainer == nil {
		return nil, fmt.Errorf("invalid response from Plex API")
	}

	libs := make([]LibrarySectionInfo, 0, len(payload.MediaContainer.Directory))
	var libraryInfo []map[string]any
	for _, d := range payload.MediaContainer.Directory {
		info := LibrarySectionInfo{
			Key: d.Key, Title: d.Title, Type: d.Type,
			Agent: d.Agent, Scanner: d.Scanner, Language: d.Language, UUID: d.UUID,
		}
		libs = append(libs, info)
		libraryInfo = append(libraryInfo, map[string]any{
			"key":      info.Key,
			"type":     info.Type,
			"title":    info.Title,
			"agent":    info.Agent,
			"scanner":  info.Scanner,
			"language": info.Language,
			"uuid":     info.UUID,
		})
	}

	c.logger.Debug("Got libraries from Plex",
		slog.Int("count", len(libs)),
		slog.Any("libraries", libraryInfo),
		slog.Any("media_container", map[string]any{
			"title1": payload.MediaContainer.Title1,
		}))

	return libs, nil
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

// GetPlexItems lists a section via plexgo Content.ListContent (GET …/library/sections/{id}/all)
// with container paging. When unwatchedOnly is true, watched items are dropped in memory.
func (c *Client) GetPlexItems(ctx context.Context, libraryKey string, unwatchedOnly bool) ([]PlexItem, error) {
	c.logger.Debug("Getting library details from Plex API",
		slog.String("section_key", libraryKey))

	rawItems, err := c.listSectionContentAll(ctx, libraryKey)
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

func chunkUints(ids []uint, size int) [][]uint {
	if len(ids) == 0 {
		return nil
	}
	var out [][]uint
	for i := 0; i < len(ids); i += size {
		j := i + size
		if j > len(ids) {
			j = len(ids)
		}
		out = append(out, ids[i:j])
	}
	return out
}

// removeMoviesNotInSnapshot deletes cache movies whose Plex ratingKey is not in present (and clears recommendation FKs).
func (c *Client) removeMoviesNotInSnapshot(ctx context.Context, present map[string]struct{}) error {
	const chunk = 400
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []models.Movie
		if err := tx.Select("id", "plex_rating_key").Find(&rows).Error; err != nil {
			return err
		}
		var stale []uint
		for _, m := range rows {
			if m.PlexRatingKey == "" {
				stale = append(stale, m.ID)
				continue
			}
			if _, ok := present[m.PlexRatingKey]; !ok {
				stale = append(stale, m.ID)
			}
		}
		for _, part := range chunkUints(stale, chunk) {
			if len(part) == 0 {
				continue
			}
			if err := tx.Exec("UPDATE recommendations SET movie_id = NULL WHERE movie_id IN ?", part).Error; err != nil {
				return fmt.Errorf("clear recommendation movie_id refs: %w", err)
			}
			if err := tx.Where("id IN ?", part).Delete(&models.Movie{}).Error; err != nil {
				return fmt.Errorf("delete stale movies: %w", err)
			}
		}
		return nil
	})
}

// removeTVShowsNotInSnapshot deletes cache TV rows whose Plex ratingKey is not in present (and clears recommendation FKs).
func (c *Client) removeTVShowsNotInSnapshot(ctx context.Context, present map[string]struct{}) error {
	const chunk = 400
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []models.TVShow
		if err := tx.Select("id", "plex_rating_key").Find(&rows).Error; err != nil {
			return err
		}
		var stale []uint
		for _, m := range rows {
			if m.PlexRatingKey == "" {
				stale = append(stale, m.ID)
				continue
			}
			if _, ok := present[m.PlexRatingKey]; !ok {
				stale = append(stale, m.ID)
			}
		}
		for _, part := range chunkUints(stale, chunk) {
			if len(part) == 0 {
				continue
			}
			if err := tx.Exec("UPDATE recommendations SET tv_show_id = NULL WHERE tv_show_id IN ?", part).Error; err != nil {
				return fmt.Errorf("clear recommendation tv_show_id refs: %w", err)
			}
			if err := tx.Where("id IN ?", part).Delete(&models.TVShow{}).Error; err != nil {
				return fmt.Errorf("delete stale TV shows: %w", err)
			}
		}
		return nil
	})
}

// UpdateCache updates the Plex cache by fetching all libraries and their items.
// Rows are upserted by Plex ratingKey; items no longer returned by Plex are removed.
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
	c.logger.Info("Successfully fetched libraries", slog.Int("count", len(libraries)))

	// Get all content from each library
	var allMovies []PlexItem
	var allTVShows []PlexItem
	var fetchErrCount int

	libs := libraries
	for _, lib := range libs {
		key := ""
		if lib.Key != nil {
			key = *lib.Key
		}

		items, err := c.GetPlexItems(ctx, key, false) // false means get all content, not just unwatched
		if err != nil {
			fetchErrCount++
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
			if item.RatingKey == "" {
				c.logger.Warn("Skipping Plex item without ratingKey",
					slog.String("title", item.Title),
					slog.String("type", item.Type))
				continue
			}
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

	if len(libs) == 0 {
		return fmt.Errorf("plex returned no libraries; cache not modified")
	}

	if len(allMovies)+len(allTVShows) == 0 {
		if fetchErrCount > 0 {
			return fmt.Errorf("no movie or TV items fetched from Plex (%d library errors logged above); cache not modified", fetchErrCount)
		}
		return fmt.Errorf("no movie or TV items in Plex libraries; cache not modified")
	}

	// Ensure the tables exist first (outside transaction)
	if err := c.db.WithContext(ctx).AutoMigrate(&models.Movie{}, &models.TVShow{}); err != nil {
		return fmt.Errorf("failed to ensure tables exist: %w", err)
	}

	movieKeys := make(map[string]struct{}, len(allMovies))
	for _, m := range allMovies {
		movieKeys[m.RatingKey] = struct{}{}
	}
	tvKeys := make(map[string]struct{}, len(allTVShows))
	for _, s := range allTVShows {
		tvKeys[s.RatingKey] = struct{}{}
	}

	const batchSize = 50
	for i := 0; i < len(allMovies); i += batchSize {
		end := i + batchSize
		if end > len(allMovies) {
			end = len(allMovies)
		}
		if err := c.upsertMovieBatch(ctx, allMovies[i:end]); err != nil {
			return fmt.Errorf("failed to upsert movie batch %d-%d: %w", i, end, err)
		}
	}

	for i := 0; i < len(allTVShows); i += batchSize {
		end := i + batchSize
		if end > len(allTVShows) {
			end = len(allTVShows)
		}
		if err := c.upsertTVShowBatch(ctx, allTVShows[i:end]); err != nil {
			return fmt.Errorf("failed to upsert TV show batch %d-%d: %w", i, end, err)
		}
	}

	if err := c.removeMoviesNotInSnapshot(ctx, movieKeys); err != nil {
		return fmt.Errorf("failed to prune stale movies: %w", err)
	}
	if err := c.removeTVShowsNotInSnapshot(ctx, tvKeys); err != nil {
		return fmt.Errorf("failed to prune stale TV shows: %w", err)
	}

	c.logger.Info("Successfully updated cache")
	return nil
}

// GORM names TMDbID as tm_db_id in SQLite (see schema).
var movieUpsertColumns = []string{
	"title", "year", "rating", "genre", "poster_url", "runtime", "tm_db_id", "view_count", "updated_at",
}

var tvUpsertColumns = []string{
	"title", "year", "rating", "genre", "poster_url", "seasons", "tm_db_id", "view_count", "updated_at",
}

// upsertMovieBatch upserts movies by plex_rating_key in a single transaction.
func (c *Client) upsertMovieBatch(ctx context.Context, movies []PlexItem) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
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
				PlexRatingKey: item.RatingKey,
				Title:         item.Title,
				Year:          year,
				Rating:        rating,
				Genre:         genre,
				PosterURL:     posterURL,
				Runtime:       runtime,
				TMDbID:        nil,
				ViewCount:     viewCount,
				UpdatedAt:     now,
			}

			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "plex_rating_key"}},
				DoUpdates: clause.AssignmentColumns(movieUpsertColumns),
			}).Create(&movie).Error; err != nil {
				return fmt.Errorf("failed to upsert movie %q: %w", item.Title, err)
			}
		}
		return nil
	})
}

// upsertTVShowBatch upserts TV shows by plex_rating_key in a single transaction.
func (c *Client) upsertTVShowBatch(ctx context.Context, shows []PlexItem) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
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
				PlexRatingKey: item.RatingKey,
				Title:         item.Title,
				Year:          year,
				Rating:        rating,
				Genre:         genre,
				PosterURL:     posterURL,
				Seasons:       seasons,
				TMDbID:        nil,
				ViewCount:     viewCount,
				UpdatedAt:     now,
			}

			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "plex_rating_key"}},
				DoUpdates: clause.AssignmentColumns(tvUpsertColumns),
			}).Create(&tvShow).Error; err != nil {
				return fmt.Errorf("failed to upsert TV show %q: %w", item.Title, err)
			}
		}
		return nil
	})
}
