package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	cache  map[string]*models.Recommendation
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
// It initializes the OpenAI client and sets up the recommendation cache.
func New(db *gorm.DB, plex *plex.Client, tmdb *tmdb.Client, logger *slog.Logger) (*Recommender, error) {
	openaiClient := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	return &Recommender{
		db:     db,
		plex:   plex,
		tmdb:   tmdb,
		logger: logger,
		openai: openaiClient,
		cache:  make(map[string]*models.Recommendation),
	}, nil
}

// GetRecommendationsForDate retrieves all recommendations for a specific date
func (r *Recommender) GetRecommendationsForDate(ctx context.Context, date time.Time) ([]models.Recommendation, error) {
	var recommendations []models.Recommendation
	if err := r.db.WithContext(ctx).Where("date = ?", date).Find(&recommendations).Error; err != nil {
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

// CheckRecommendationsExist checks if recommendations already exist for a specific date
func (r *Recommender) CheckRecommendationsExist(ctx context.Context, date time.Time) (bool, error) {
	var movieCount, tvShowCount int64

	// Count movies
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "movie").
		Count(&movieCount).Error; err != nil {
		return false, fmt.Errorf("failed to check existing movie recommendations: %w", err)
	}

	// Count TV shows
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "tvshow").
		Count(&tvShowCount).Error; err != nil {
		return false, fmt.Errorf("failed to check existing TV show recommendations: %w", err)
	}

	// According to README:
	// - 4 movies (1 funny, 1 action/drama, 1 rewatched, 1 additional)
	// - 3 TV shows
	return movieCount >= 4 && tvShowCount >= 3, nil
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
		if movie.TMDbID > 0 {
			result, err := r.tmdb.SearchMovie(ctx, movie.Title, movie.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				// Update the movie's TMDbID if it's not set
				if movie.TMDbID == 0 {
					movie.TMDbID = result.Results[0].ID
					if err := r.db.Save(&movie).Error; err != nil {
						r.logger.Error("Failed to update movie TMDbID", "error", err, "title", movie.Title)
					}
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
			Source:    movie.Source,
			MovieID:   &movie.ID,
			TMDbID:    movie.TMDbID,
		})
	}

	for _, tvShow := range unwatchedTVShows {
		// Get TMDB poster URL if available
		posterURL := tvShow.PosterURL
		if tvShow.TMDbID > 0 {
			result, err := r.tmdb.SearchTVShow(ctx, tvShow.Title, tvShow.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				// Update the TV show's TMDbID if it's not set
				if tvShow.TMDbID == 0 {
					tvShow.TMDbID = result.Results[0].ID
					if err := r.db.Save(&tvShow).Error; err != nil {
						r.logger.Error("Failed to update TV show TMDbID", "error", err, "title", tvShow.Title)
					}
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
			Source:    tvShow.Source,
			TVShowID:  &tvShow.ID,
			TMDbID:    tvShow.TMDbID,
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

	resp, err := r.openai.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
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
		},
	)
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

	// First, process movies according to specific requirements
	for _, rec := range recommendations {
		if rec.Type == "movie" {
			// Check if we need this type of movie
			if strings.Contains(strings.ToLower(rec.Genre), "comedy") && !movieTypes["funny"] && rec.Source == "plex" {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["funny"] = true
			} else if (strings.Contains(strings.ToLower(rec.Genre), "action") ||
				strings.Contains(strings.ToLower(rec.Genre), "drama")) &&
				!movieTypes["action_drama"] && rec.Source == "plex" {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["action_drama"] = true
			} else if rec.Source != "plex" && !movieTypes["rewatched"] { // Movies not from Plex are ones we've seen before
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["rewatched"] = true
			} else if !movieTypes["additional"] { // Add one more movie if we haven't reached the limit
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["additional"] = true
			}
		}
	}

	// Then process TV shows (3 unwatched)
	for _, rec := range recommendations {
		if rec.Type == "tvshow" && typeCounts["tvshow"] < 3 && rec.Source == "plex" {
			filteredRecommendations = append(filteredRecommendations, rec)
			typeCounts["tvshow"]++
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

	// Save recommendations to database in a transaction
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, rec := range filteredRecommendations {
			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to save recommendation: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save recommendations in transaction: %w", err)
	}

	r.logger.Debug("Successfully generated recommendations",
		slog.Int("total_count", len(filteredRecommendations)))

	if len(filteredRecommendations) == 0 {
		return fmt.Errorf("no recommendations found")
	}

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
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Where("source = ?", "plex").Count(&stats.TotalCachedMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Where("source = ?", "plex").Count(&stats.TotalCachedTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached TV shows: %w", err)
	}

	// Get last cache update time from the most recent movie or TV show update
	var lastMovieUpdate, lastTVShowUpdate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Where("source = ?", "plex").Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastMovieUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last movie update: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Where("source = ?", "plex").Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastTVShowUpdate).Error; err != nil {
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
