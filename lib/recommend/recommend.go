package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"log/slog"

	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"gorm.io/gorm"
)

// StatsData represents statistics about the recommendations database.
type StatsData struct {
	TotalRecommendations        int64
	TotalMovies                 int64
	TotalTVShows                int64
	FirstDate                   time.Time
	LastDate                    time.Time
	AverageDailyRecommendations float64
	GenreDistribution           []struct {
		Genre string
		Count int64
	}
	TotalCachedMovies  int64
	TotalCachedTVShows int64
	LastCacheUpdate    time.Time
}

// Recommender handles the generation and retrieval of content recommendations.
// It uses OpenAI to generate recommendations based on unwatched content from Plex
// and metadata from TMDb.
type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	tmdb   *tmdb.Client
	logger *slog.Logger
	openai *openai.Client
	cache  map[string]*CacheEntry
}

// CacheEntry represents a cached recommendation with metadata
type CacheEntry struct {
	Recommendation *models.Recommendation
	Timestamp      time.Time
	TTL            time.Duration
}

// RecommendationContext contains the context used for generating recommendations.
// It includes the available content, user preferences, and previous recommendations.
type RecommendationContext struct {
	Content                 string
	Preferences             string
	PreviousRecommendations string
}

// UnwatchedContent represents the unwatched content available for recommendations,
// organized by content type (movies and TV shows).
type UnwatchedContent struct {
	Movies  []models.Recommendation
	TVShows []models.Recommendation
}

// New creates a new Recommender instance with the provided dependencies.
// It initializes the OpenAI client with proper timeout and retry configuration.
func New(db *gorm.DB, plex *plex.Client, tmdb *tmdb.Client, logger *slog.Logger) (*Recommender, error) {
	// Create HTTP client with timeout for OpenAI
	httpClient := &http.Client{
		Timeout: 120 * time.Second, // Longer timeout for OpenAI API
		Transport: &http.Transport{
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
		},
	}

	// Create OpenAI client with configuration
	config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	config.HTTPClient = httpClient

	openaiClient := openai.NewClientWithConfig(config)

	r := &Recommender{
		db:     db,
		plex:   plex,
		tmdb:   tmdb,
		logger: logger,
		openai: openaiClient,
		cache:  make(map[string]*CacheEntry),
	}

	// Start cache cleanup goroutine
	go r.startCacheCleanup()

	return r, nil
}

// GetRecommendationsForDate retrieves all recommendations for a specific date
func (r *Recommender) GetRecommendationsForDate(ctx context.Context, date time.Time) ([]models.Recommendation, error) {
	var recommendations []models.Recommendation
	// Use date() function to match only the date part, ignoring time and timezone
	dateStr := date.Format("2006-01-02")
	if err := r.db.WithContext(ctx).Where("date(date) = ?", dateStr).Find(&recommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get recommendations: %w", err)
	}
	return recommendations, nil
}

// GetRecommendationDates retrieves a paginated list of dates with recommendations
func (r *Recommender) GetRecommendationDates(ctx context.Context, page, pageSize int) ([]time.Time, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get total count: %w", err)
	}

	var dates []time.Time
	if err := r.db.WithContext(ctx).
		Model(&models.Recommendation{}).
		Order("date DESC").
		Offset((page-1)*pageSize).
		Limit(pageSize).
		Pluck("date", &dates).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get dates: %w", err)
	}

	return dates, total, nil
}

// CheckRecommendationsComplete checks if recommendations are complete for a specific date
// Returns true if we have at least some recommendations (flexible approach)
func (r *Recommender) CheckRecommendationsComplete(ctx context.Context, date time.Time) (bool, error) {
	var movieCount, tvShowCount int64

	// Count movies for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "movie").
		Count(&movieCount).Error; err != nil {
		return false, fmt.Errorf("failed to count movies: %w", err)
	}

	// Count TV shows for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "tvshow").
		Count(&tvShowCount).Error; err != nil {
		return false, fmt.Errorf("failed to count TV shows: %w", err)
	}

	// Check what content is available in the cache to determine completeness criteria
	var cachedMoviesCount, cachedTVShowsCount int64
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&cachedMoviesCount).Error; err != nil {
		return false, fmt.Errorf("failed to count cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&cachedTVShowsCount).Error; err != nil {
		return false, fmt.Errorf("failed to count cached TV shows: %w", err)
	}

	// If we have both types of content cached, require both types of recommendations
	if cachedMoviesCount > 0 && cachedTVShowsCount > 0 {
		return movieCount > 0 && tvShowCount > 0, nil
	}
	
	// If only movies are cached, only require movie recommendations
	if cachedMoviesCount > 0 && cachedTVShowsCount == 0 {
		return movieCount > 0, nil
	}
	
	// If only TV shows are cached, only require TV show recommendations
	if cachedMoviesCount == 0 && cachedTVShowsCount > 0 {
		return tvShowCount > 0, nil
	}
	
	// If no content is cached, we can't generate recommendations
	return false, nil
}

// CheckRecommendationsExist checks if recommendations already exist for a specific date
func (r *Recommender) CheckRecommendationsExist(ctx context.Context, date time.Time) (bool, error) {
	var count int64

	// Count total recommendations for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ?", date).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check existing recommendations: %w", err)
	}

	// If we have any recommendations, check if they're complete
	if count > 0 {
		complete, err := r.CheckRecommendationsComplete(ctx, date)
		if err != nil {
			return false, fmt.Errorf("failed to check recommendation completeness: %w", err)
		}
		return complete, nil
	}

	// Return false if we have no recommendations
	return false, nil
}

// loadPromptTemplate loads and parses a prompt template from the embedded filesystem.
// It returns a template that can be executed with the provided data.
func (r *Recommender) loadPromptTemplate(filename string) (*template.Template, error) {
	content, err := prompts.FS.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read prompt file %s: %w", filename, err)
	}

	tmpl, err := template.New(filename).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template %s: %w", filename, err)
	}

	return tmpl, nil
}

// formatContent formats a slice of recommendations into a human-readable string.
// Each item is formatted with its title, year, rating, genre, runtime, and TMDb ID.
// It limits the number of items to prevent token limit issues.
func (r *Recommender) formatContent(items []models.Recommendation) string {
	var content strings.Builder
	// Limit to 50 items per type to prevent token limit issues
	limit := 50
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s - Runtime: %d - TMDb ID: %d\n",
			item.Title, item.Year, item.Rating, item.Genre, item.Runtime, item.TMDbID))
	}
	return content.String()
}

// limitPreviousRecommendations limits the number of previous recommendations to prevent token limit issues
func (r *Recommender) limitPreviousRecommendations(recs []models.Recommendation) []models.Recommendation {
	// Only keep the last 10 recommendations
	if len(recs) > 10 {
		return recs[len(recs)-10:]
	}
	return recs
}

// GenerateRecommendations generates new recommendations for the specified date.
// It uses OpenAI to analyze unwatched content and previous recommendations,
// then stores the generated recommendations in the database.
func (r *Recommender) GenerateRecommendations(ctx context.Context, date time.Time) error {
	r.logger.Debug("Starting recommendation generation")

	// Check if recommendations already exist
	exists, err := r.CheckRecommendationsExist(ctx, date)
	if err != nil {
		return fmt.Errorf("failed to check existing recommendations: %w", err)
	}
	if exists {
		r.logger.Info("Recommendations already exist for date", slog.Time("date", date))
		return nil
	}

	// Get unwatched content from database
	var unwatchedMovies []models.Movie
	if err := r.db.WithContext(ctx).Find(&unwatchedMovies).Error; err != nil {
		return fmt.Errorf("failed to get unwatched movies: %w", err)
	}
	r.logger.Debug("Found unwatched movies", slog.Int("count", len(unwatchedMovies)))

	var unwatchedTVShows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&unwatchedTVShows).Error; err != nil {
		return fmt.Errorf("failed to get unwatched TV shows: %w", err)
	}
	r.logger.Debug("Found unwatched TV shows", slog.Int("count", len(unwatchedTVShows)))

	// Get previous recommendations for context
	prevDate := date.AddDate(0, 0, -1)
	prevRecs, err := r.GetRecommendationsForDate(ctx, prevDate)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get previous recommendations: %w", err)
	}

	// Limit previous recommendations
	prevRecs = r.limitPreviousRecommendations(prevRecs)

	// Convert movies and TV shows to recommendations for OpenAI
	var allContent []models.Recommendation
	for _, movie := range unwatchedMovies {
		// Get TMDB poster URL if available
		posterURL := movie.PosterURL
		tmdbID := 0
		if movie.TMDbID != nil && *movie.TMDbID > 0 {
			tmdbID = *movie.TMDbID
			result, err := r.tmdb.SearchMovie(ctx, movie.Title, movie.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				// Update the movie's TMDbID if it's not set
				if movie.TMDbID == nil {
					newTMDbID := result.Results[0].ID
					movie.TMDbID = &newTMDbID
					if err := r.db.WithContext(ctx).Save(&movie).Error; err != nil {
						r.logger.Error("Failed to update movie TMDbID", "error", err, "title", movie.Title)
					}
				}
			} else if err != nil {
				r.logger.Error("Failed to search TMDb for movie", "error", err, "title", movie.Title)
			}
		} else {
			// Try to fetch TMDb data if we don't have it
			result, err := r.tmdb.SearchMovie(ctx, movie.Title, movie.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				tmdbID = result.Results[0].ID
				newTMDbID := result.Results[0].ID
				movie.TMDbID = &newTMDbID
				if err := r.db.WithContext(ctx).Save(&movie).Error; err != nil {
					r.logger.Error("Failed to update movie TMDbID", "error", err, "title", movie.Title)
				}
			} else if err != nil {
				r.logger.Error("Failed to search TMDb for movie", "error", err, "title", movie.Title)
			}
		}

		allContent = append(allContent, models.Recommendation{
			Title:     movie.Title,
			Type:      "movie",
			Year:      movie.Year,
			Rating:    movie.Rating,
			Genre:     movie.Genre,
			PosterURL: posterURL,
			Runtime:   movie.Runtime,
			MovieID:   &movie.ID,
			TMDbID:    tmdbID,
		})
	}

	for _, tvShow := range unwatchedTVShows {
		// Get TMDB poster URL if available
		posterURL := tvShow.PosterURL
		tmdbID := 0
		if tvShow.TMDbID != nil && *tvShow.TMDbID > 0 {
			tmdbID = *tvShow.TMDbID
			result, err := r.tmdb.SearchTVShow(ctx, tvShow.Title, tvShow.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				// Update the TV show's TMDbID if it's not set
				if tvShow.TMDbID == nil {
					newTMDbID := result.Results[0].ID
					tvShow.TMDbID = &newTMDbID
					if err := r.db.WithContext(ctx).Save(&tvShow).Error; err != nil {
						r.logger.Error("Failed to update TV show TMDbID", "error", err, "title", tvShow.Title)
					}
				}
			} else if err != nil {
				r.logger.Error("Failed to search TMDb for TV show", "error", err, "title", tvShow.Title)
			}
		} else {
			// Try to fetch TMDb data if we don't have it
			result, err := r.tmdb.SearchTVShow(ctx, tvShow.Title, tvShow.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				tmdbID = result.Results[0].ID
				newTMDbID := result.Results[0].ID
				tvShow.TMDbID = &newTMDbID
				if err := r.db.WithContext(ctx).Save(&tvShow).Error; err != nil {
					r.logger.Error("Failed to update TV show TMDbID", "error", err, "title", tvShow.Title)
				}
			} else if err != nil {
				r.logger.Error("Failed to search TMDb for TV show", "error", err, "title", tvShow.Title)
			}
		}

		allContent = append(allContent, models.Recommendation{
			Title:     tvShow.Title,
			Type:      "tvshow",
			Year:      tvShow.Year,
			Rating:    tvShow.Rating,
			Genre:     tvShow.Genre,
			PosterURL: posterURL,
			Runtime:   tvShow.Seasons,
			TVShowID:  &tvShow.ID,
			TMDbID:    tmdbID,
		})
	}

	// Prepare content for OpenAI
	content := RecommendationContext{
		Content: r.formatContent(allContent),
		Preferences: "User enjoys a mix of genres including action, drama, comedy, and sci-fi. " +
			"Prefers content with high ratings (above 7.5) and appreciates both popular and lesser-known titles.",
		PreviousRecommendations: r.formatContent(prevRecs),
	}

	// Log available content for debugging
	r.logger.Debug("Available content for recommendations",
		slog.Int("total_items", len(allContent)),
		slog.Int("movies", len(unwatchedMovies)),
		slog.Int("tv_shows", len(unwatchedTVShows)),
		slog.String("content", content.Content))

	// Load prompt templates
	systemPrompt, err := r.loadPromptTemplate("system_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load system prompt: %w", err)
	}

	recommendationPrompt, err := r.loadPromptTemplate("recommendation_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load recommendation prompt: %w", err)
	}

	// Generate recommendations using OpenAI
	var systemMsg, userMsg strings.Builder
	if err := systemPrompt.Execute(&systemMsg, nil); err != nil {
		return fmt.Errorf("failed to execute system prompt: %w", err)
	}
	if err := recommendationPrompt.Execute(&userMsg, content); err != nil {
		return fmt.Errorf("failed to execute recommendation prompt: %w", err)
	}

	// Retry OpenAI API call with exponential backoff
	resp, err := r.retryOpenAIRequest(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT4oMini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemMsg.String(),
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: userMsg.String(),
			},
		},
		Temperature: 0.7,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to get OpenAI completion: %w", err)
	}

	// Parse OpenAI JSON response
	type RecommendationItem struct {
		Title       string `json:"title"`
		Type        string `json:"type,omitempty"`
		TMDbID      int    `json:"tmdb_id"`
		Explanation string `json:"explanation"`
	}

	type RecommendationResponse struct {
		Movies  []RecommendationItem `json:"movies"`
		TVShows []RecommendationItem `json:"tvshows"`
	}

	var recResponse RecommendationResponse
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &recResponse); err != nil {
		return fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	// Create a map of all available content for quick lookup
	contentMap := make(map[string]models.Recommendation)
	for _, content := range allContent {
		contentMap[content.Title] = content
	}

	// Process recommendations
	recommendations := make([]models.Recommendation, 0)
	seenTitles := make(map[string]bool)

	// Helper function to process recommendation items
	processItems := func(items []RecommendationItem, contentType string) {
		for _, item := range items {
			if seenTitles[item.Title] {
				continue
			}

			if content, exists := contentMap[item.Title]; exists {
				content.Date = date
				content.TMDbID = item.TMDbID
				recommendations = append(recommendations, content)
				seenTitles[item.Title] = true
			}
		}
	}

	// Process each type of recommendation
	processItems(recResponse.Movies, "movie")
	processItems(recResponse.TVShows, "tvshow")

	// Enforce limits based on content type and requirements
	typeCounts := make(map[string]int)
	movieTypes := make(map[string]bool) // Track specific movie types (funny, action/drama, rewatched, additional)
	filteredRecommendations := make([]models.Recommendation, 0)

	// Check what content types are available to determine recommendation targets
	var availableMovies, availableTVShows int64
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&availableMovies).Error; err != nil {
		return fmt.Errorf("failed to count available movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&availableTVShows).Error; err != nil {
		return fmt.Errorf("failed to count available TV shows: %w", err)
	}

	// Determine target counts based on available content
	targetMovieCount := 4  // Standard target
	targetTVShowCount := 3 // Standard target
	
	// If no TV shows are available, increase movie recommendations to compensate
	if availableTVShows == 0 && availableMovies > 0 {
		targetMovieCount = 7 // Increase to 7 movies when no TV shows available
		targetTVShowCount = 0
		r.logger.Info("No TV shows available, generating movie-only recommendations",
			slog.Int("target_movies", targetMovieCount))
	}

	// Process movies according to requirements
	for _, rec := range recommendations {
		if rec.Type == "movie" && typeCounts["movie"] < targetMovieCount {
			// Try to get diverse genres, but be flexible if content is limited
			if strings.Contains(strings.ToLower(rec.Genre), "comedy") && !movieTypes["funny"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["funny"] = true
			} else if (strings.Contains(strings.ToLower(rec.Genre), "action") ||
				strings.Contains(strings.ToLower(rec.Genre), "drama")) &&
				!movieTypes["action_drama"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["action_drama"] = true
			} else if !movieTypes["rewatched"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["rewatched"] = true
			} else if typeCounts["movie"] < targetMovieCount { // Add additional movies up to target
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
			}
		}
	}

	// Process TV shows if available
	if availableTVShows > 0 {
		for _, rec := range recommendations {
			if rec.Type == "tvshow" && typeCounts["tvshow"] < targetTVShowCount {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["tvshow"]++
			}
		}
	}

	// Log the counts for debugging
	r.logger.Debug("Recommendation counts",
		slog.Int("movies", typeCounts["movie"]),
		slog.Int("tvshows", typeCounts["tvshow"]),
		slog.Bool("funny_movie", movieTypes["funny"]),
		slog.Bool("action_drama_movie", movieTypes["action_drama"]),
		slog.Bool("rewatched_movie", movieTypes["rewatched"]),
		slog.Bool("additional_movie", movieTypes["additional"]))

	// If we have no new recommendations to store, return an error
	if len(filteredRecommendations) == 0 {
		r.logger.Warn("No new recommendations to store")
		return fmt.Errorf("no new recommendations to store")
	}

	// Save recommendations to database in a transaction with duplicate checking
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// First, check if any recommendations already exist for this date
		var existingCount int64
		if err := tx.Model(&models.Recommendation{}).Where("date = ?", date).Count(&existingCount).Error; err != nil {
			return fmt.Errorf("failed to check existing recommendations in transaction: %w", err)
		}
		
		if existingCount > 0 {
			return fmt.Errorf("recommendations already exist for date %s (found %d existing)", date.Format("2006-01-02"), existingCount)
		}
		
		// Create recommendations with duplicate checking
		for _, rec := range filteredRecommendations {
			// Check for duplicate title on the same date
			var duplicate models.Recommendation
			err := tx.Where("title = ? AND date = ?", rec.Title, rec.Date).First(&duplicate).Error
			if err == nil {
				r.logger.Warn("Skipping duplicate recommendation", 
					slog.String("title", rec.Title),
					slog.Time("date", rec.Date))
				continue
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("failed to check for duplicate recommendation: %w", err)
			}
			
			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to save recommendation: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save recommendations in transaction: %w", err)
	}

	r.logger.Debug("Successfully stored recommendations",
		slog.Int("total_count", len(filteredRecommendations)),
		slog.Int("movies", typeCounts["movie"]),
		slog.Int("tvshows", typeCounts["tvshow"]))

	return nil
}

// GetStats retrieves statistics about the recommendations database.
// It returns counts of recommendations by type, date range, and genre distribution.
func (r *Recommender) GetStats(ctx context.Context) (*StatsData, error) {
	var stats StatsData

	// Get total recommendations
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Count(&stats.TotalRecommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get total recommendations: %w", err)
	}

	// Get counts by type
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", "movie").Count(&stats.TotalMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", "tvshow").Count(&stats.TotalTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total TV shows: %w", err)
	}

	// Get date range
	var firstDate, lastDate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date ASC").Limit(1).Pluck("date", &firstDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get first date: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date DESC").Limit(1).Pluck("date", &lastDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last date: %w", err)
		}
	}
	stats.FirstDate = firstDate
	stats.LastDate = lastDate

	// Calculate average daily recommendations
	if !firstDate.IsZero() && !lastDate.IsZero() {
		days := lastDate.Sub(firstDate).Hours() / 24
		if days > 0 {
			stats.AverageDailyRecommendations = float64(stats.TotalRecommendations) / days
		}
	}

	// Get genre distribution
	type genreCount struct {
		Genre string
		Count int64
	}
	var genreCounts []genreCount
	if err := r.db.WithContext(ctx).
		Model(&models.Recommendation{}).
		Select("genre, count(*) as count").
		Group("genre").
		Order("count DESC").
		Find(&genreCounts).Error; err != nil {
		return nil, fmt.Errorf("failed to get genre distribution: %w", err)
	}

	stats.GenreDistribution = make([]struct {
		Genre string
		Count int64
	}, len(genreCounts))
	for i, gc := range genreCounts {
		stats.GenreDistribution[i] = struct {
			Genre string
			Count int64
		}{
			Genre: gc.Genre,
			Count: gc.Count,
		}
	}

	// Get cache database statistics
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&stats.TotalCachedMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&stats.TotalCachedTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached TV shows: %w", err)
	}

	// Get last cache update time from the most recent movie or TV show update
	var lastMovieUpdate, lastTVShowUpdate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastMovieUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last movie update: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastTVShowUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last TV show update: %w", err)
		}
	}

	// Use the most recent update time
	if lastMovieUpdate.After(lastTVShowUpdate) {
		stats.LastCacheUpdate = lastMovieUpdate
	} else {
		stats.LastCacheUpdate = lastTVShowUpdate
	}

	return &stats, nil
}

// startCacheCleanup starts a goroutine that periodically cleans up expired cache entries
func (r *Recommender) startCacheCleanup() {
	ticker := time.NewTicker(30 * time.Minute) // Clean every 30 minutes
	defer ticker.Stop()

	for range ticker.C {
		r.cleanupCache()
	}
}

// cleanupCache removes expired entries from the cache
func (r *Recommender) cleanupCache() {
	now := time.Now()
	var expiredKeys []string

	for key, entry := range r.cache {
		if now.Sub(entry.Timestamp) > entry.TTL {
			expiredKeys = append(expiredKeys, key)
		}
	}

	for _, key := range expiredKeys {
		delete(r.cache, key)
	}

	if len(expiredKeys) > 0 {
		r.logger.Debug("Cleaned up expired cache entries", 
			slog.Int("expired_count", len(expiredKeys)),
			slog.Int("remaining_count", len(r.cache)))
	}
}

// SetCache adds or updates a cache entry
func (r *Recommender) SetCache(key string, recommendation *models.Recommendation, ttl time.Duration) {
	r.cache[key] = &CacheEntry{
		Recommendation: recommendation,
		Timestamp:      time.Now(),
		TTL:            ttl,
	}
}

// GetCache retrieves a cache entry if it exists and is not expired
func (r *Recommender) GetCache(key string) (*models.Recommendation, bool) {
	entry, exists := r.cache[key]
	if !exists {
		return nil, false
	}

	// Check if entry is expired
	if time.Since(entry.Timestamp) > entry.TTL {
		delete(r.cache, key)
		return nil, false
	}

	return entry.Recommendation, true
}

// ClearCache removes all cache entries
func (r *Recommender) ClearCache() {
	r.cache = make(map[string]*CacheEntry)
	r.logger.Debug("Cache cleared")
}

// retryOpenAIRequest implements retry logic for OpenAI API calls with exponential backoff
func (r *Recommender) retryOpenAIRequest(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	var resp openai.ChatCompletionResponse
	maxRetries := 3
	baseDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := r.openai.CreateChatCompletion(ctx, req)
		if err == nil {
			return result, nil
		}

		// If this is the last attempt, return the error
		if attempt == maxRetries {
			r.logger.Error("OpenAI API max retries exceeded",
				slog.Int("attempts", attempt+1),
				slog.String("error", err.Error()))
			return resp, err
		}

		// Calculate delay with exponential backoff
		delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}

		r.logger.Warn("Retrying OpenAI API request",
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay),
			slog.String("error", err.Error()))

		// Wait before retrying
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	return resp, fmt.Errorf("unreachable code")
}
