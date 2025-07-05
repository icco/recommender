package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/icco/recommender/lib/db"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DebugRecommender is a simplified version that bypasses TMDb API calls
type DebugRecommender struct {
	db     *gorm.DB
	logger *slog.Logger
	openai *openai.Client
}

// RecommendationContext contains the context used for generating recommendations
type RecommendationContext struct {
	Content                 string
	Preferences             string
	PreviousRecommendations string
}

func main() {
	// Set up detailed logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	logger := slog.Default()
	logger.Info("Starting debug recommendation generation")

	// Check required environment variables
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		logger.Error("OPENAI_API_KEY environment variable is required")
		os.Exit(1)
	}

	// Get database path from environment or use default
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "recommender.db"
	}

	// Connect to database
	logger.Info("Connecting to database", slog.String("path", dbPath))
	gormDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: db.NewGormLogger(logger),
	})
	if err != nil {
		logger.Error("Failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}

	// Run migrations to ensure database is up to date
	logger.Info("Running database migrations")
	if err := db.RunMigrations(gormDB, logger); err != nil {
		logger.Error("Failed to run migrations", slog.Any("error", err))
		os.Exit(1)
	}

	// Create debug recommender instance
	debugRecommender, err := NewDebugRecommender(gormDB, logger, openaiAPIKey)
	if err != nil {
		logger.Error("Failed to create debug recommender", slog.Any("error", err))
		os.Exit(1)
	}

	// Check database content
	ctx := context.Background()
	if err := debugRecommender.CheckDatabaseContent(ctx); err != nil {
		logger.Error("Failed to check database content", slog.Any("error", err))
		os.Exit(1)
	}

	// Generate recommendations for today
	today := time.Now().Truncate(24 * time.Hour)
	logger.Info("Generating recommendations for today", slog.Time("date", today))

	if err := debugRecommender.GenerateRecommendations(ctx, today); err != nil {
		logger.Error("Failed to generate recommendations", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("Debug recommendation generation completed successfully")
}

// NewDebugRecommender creates a new debug recommender instance
func NewDebugRecommender(db *gorm.DB, logger *slog.Logger, openaiAPIKey string) (*DebugRecommender, error) {
	// Create HTTP client with timeout for OpenAI
	httpClient := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
		},
	}

	// Create OpenAI client
	config := openai.DefaultConfig(openaiAPIKey)
	config.HTTPClient = httpClient
	openaiClient := openai.NewClientWithConfig(config)

	return &DebugRecommender{
		db:     db,
		logger: logger,
		openai: openaiClient,
	}, nil
}

// CheckDatabaseContent checks what content is available in the database
func (dr *DebugRecommender) CheckDatabaseContent(ctx context.Context) error {
	dr.logger.Info("Checking database content")

	// Count movies
	var movieCount int64
	if err := dr.db.WithContext(ctx).Model(&models.Movie{}).Count(&movieCount).Error; err != nil {
		return fmt.Errorf("failed to count movies: %w", err)
	}

	// Count TV shows
	var tvShowCount int64
	if err := dr.db.WithContext(ctx).Model(&models.TVShow{}).Count(&tvShowCount).Error; err != nil {
		return fmt.Errorf("failed to count TV shows: %w", err)
	}

	// Count existing recommendations
	var recommendationCount int64
	if err := dr.db.WithContext(ctx).Model(&models.Recommendation{}).Count(&recommendationCount).Error; err != nil {
		return fmt.Errorf("failed to count recommendations: %w", err)
	}

	dr.logger.Info("Database content summary",
		slog.Int64("movies", movieCount),
		slog.Int64("tv_shows", tvShowCount),
		slog.Int64("existing_recommendations", recommendationCount))

	if movieCount == 0 && tvShowCount == 0 {
		return fmt.Errorf("no cached content found in database - please run cache update first")
	}

	// Show sample content
	dr.logSampleContent(ctx)

	return nil
}

// logSampleContent logs a sample of available content for debugging
func (dr *DebugRecommender) logSampleContent(ctx context.Context) {
	// Get sample movies
	var sampleMovies []models.Movie
	if err := dr.db.WithContext(ctx).Limit(5).Find(&sampleMovies).Error; err == nil {
		dr.logger.Info("Sample movies found", slog.Int("count", len(sampleMovies)))
		for _, movie := range sampleMovies {
			dr.logger.Debug("Sample movie",
				slog.String("title", movie.Title),
				slog.Int("year", movie.Year),
				slog.Float64("rating", movie.Rating),
				slog.String("genre", movie.Genre))
		}
	}

	// Get sample TV shows
	var sampleTVShows []models.TVShow
	if err := dr.db.WithContext(ctx).Limit(5).Find(&sampleTVShows).Error; err == nil {
		dr.logger.Info("Sample TV shows found", slog.Int("count", len(sampleTVShows)))
		for _, show := range sampleTVShows {
			dr.logger.Debug("Sample TV show",
				slog.String("title", show.Title),
				slog.Int("year", show.Year),
				slog.Float64("rating", show.Rating),
				slog.String("genre", show.Genre))
		}
	}
}

// GenerateRecommendations generates recommendations using only cached data (no TMDb API calls)
func (dr *DebugRecommender) GenerateRecommendations(ctx context.Context, date time.Time) error {
	dr.logger.Info("Starting recommendation generation (TMDb API bypassed)")

	// Check if recommendations already exist
	var existingCount int64
	if err := dr.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ?", date).
		Count(&existingCount).Error; err != nil {
		return fmt.Errorf("failed to check existing recommendations: %w", err)
	}

	if existingCount > 0 {
		dr.logger.Info("Recommendations already exist for date", 
			slog.Time("date", date),
			slog.Int64("count", existingCount))
		return nil
	}

	// Get cached movies (no TMDb API calls)
	var movies []models.Movie
	if err := dr.db.WithContext(ctx).Find(&movies).Error; err != nil {
		return fmt.Errorf("failed to get cached movies: %w", err)
	}
	dr.logger.Info("Found cached movies", slog.Int("count", len(movies)))

	// Get cached TV shows (no TMDb API calls)
	var tvShows []models.TVShow
	if err := dr.db.WithContext(ctx).Find(&tvShows).Error; err != nil {
		return fmt.Errorf("failed to get cached TV shows: %w", err)
	}
	dr.logger.Info("Found cached TV shows", slog.Int("count", len(tvShows)))

	// Get previous recommendations for context
	prevDate := date.AddDate(0, 0, -1)
	var prevRecs []models.Recommendation
	if err := dr.db.WithContext(ctx).
		Where("date = ?", prevDate).
		Limit(10). // Limit to prevent token issues
		Find(&prevRecs).Error; err != nil {
		dr.logger.Info("No previous recommendations found", slog.Any("error", err))
	}

	// Convert cached data to recommendation format (bypassing TMDb entirely)
	var allContent []models.Recommendation
	
	// Process movies - use cached data only
	for _, movie := range movies {
		// Use existing poster URL and TMDb ID if available, otherwise use defaults
		posterURL := movie.PosterURL
		tmdbID := 0
		if movie.TMDbID != nil {
			tmdbID = *movie.TMDbID
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

	// Process TV shows - use cached data only
	for _, tvShow := range tvShows {
		// Use existing poster URL and TMDb ID if available, otherwise use defaults
		posterURL := tvShow.PosterURL
		tmdbID := 0
		if tvShow.TMDbID != nil {
			tmdbID = *tvShow.TMDbID
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

	dr.logger.Info("Prepared content for OpenAI",
		slog.Int("total_items", len(allContent)),
		slog.Int("movies", len(movies)),
		slog.Int("tv_shows", len(tvShows)))

	// Prepare content for OpenAI
	content := RecommendationContext{
		Content: dr.formatContent(allContent),
		Preferences: "User enjoys a mix of genres including action, drama, comedy, and sci-fi. " +
			"Prefers content with high ratings (above 7.5) and appreciates both popular and lesser-known titles.",
		PreviousRecommendations: dr.formatContent(prevRecs),
	}

	// Log content being sent to OpenAI for debugging
	dr.logger.Debug("Content formatted for OpenAI",
		slog.Int("content_length", len(content.Content)),
		slog.Int("prev_rec_length", len(content.PreviousRecommendations)))

	// Load and execute prompt templates
	systemPrompt, err := dr.loadPromptTemplate("system_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load system prompt: %w", err)
	}

	recommendationPrompt, err := dr.loadPromptTemplate("recommendation_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load recommendation prompt: %w", err)
	}

	var systemMsg, userMsg strings.Builder
	if err := systemPrompt.Execute(&systemMsg, nil); err != nil {
		return fmt.Errorf("failed to execute system prompt: %w", err)
	}
	if err := recommendationPrompt.Execute(&userMsg, content); err != nil {
		return fmt.Errorf("failed to execute recommendation prompt: %w", err)
	}

	dr.logger.Info("Sending request to OpenAI",
		slog.Int("system_msg_length", len(systemMsg.String())),
		slog.Int("user_msg_length", len(userMsg.String())))

	// Make OpenAI API call
	resp, err := dr.openai.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
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

	dr.logger.Info("Received response from OpenAI",
		slog.Int("response_length", len(resp.Choices[0].Message.Content)))

	// Parse OpenAI response
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
		dr.logger.Error("Failed to parse OpenAI response", 
			slog.String("raw_response", resp.Choices[0].Message.Content),
			slog.Any("error", err))
		return fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	dr.logger.Info("Parsed OpenAI response",
		slog.Int("movies", len(recResponse.Movies)),
		slog.Int("tvshows", len(recResponse.TVShows)))

	// Log the recommendations for debugging
	for i, movie := range recResponse.Movies {
		dr.logger.Debug("Movie recommendation",
			slog.Int("index", i),
			slog.String("title", movie.Title),
			slog.String("type", movie.Type),
			slog.Int("tmdb_id", movie.TMDbID),
			slog.String("explanation", movie.Explanation))
	}

	for i, show := range recResponse.TVShows {
		dr.logger.Debug("TV show recommendation",
			slog.Int("index", i),
			slog.String("title", show.Title),
			slog.Int("tmdb_id", show.TMDbID),
			slog.String("explanation", show.Explanation))
	}

	// Process and save recommendations
	return dr.processAndSaveRecommendations(ctx, date, recResponse, allContent)
}

// processAndSaveRecommendations processes the OpenAI response and saves recommendations
func (dr *DebugRecommender) processAndSaveRecommendations(ctx context.Context, date time.Time, response RecommendationResponse, allContent []models.Recommendation) error {
	// Create a map for quick lookup
	contentMap := make(map[string]models.Recommendation)
	for _, content := range allContent {
		contentMap[content.Title] = content
	}

	var recommendations []models.Recommendation
	seenTitles := make(map[string]bool)

	// Process movie recommendations
	for _, item := range response.Movies {
		if seenTitles[item.Title] {
			dr.logger.Warn("Duplicate movie title found", slog.String("title", item.Title))
			continue
		}

		if content, exists := contentMap[item.Title]; exists {
			content.Date = date
			if item.TMDbID > 0 {
				content.TMDbID = item.TMDbID
			}
			recommendations = append(recommendations, content)
			seenTitles[item.Title] = true
			dr.logger.Debug("Added movie recommendation", 
				slog.String("title", item.Title),
				slog.String("type", item.Type))
		} else {
			dr.logger.Warn("Movie not found in available content", slog.String("title", item.Title))
		}
	}

	// Process TV show recommendations
	for _, item := range response.TVShows {
		if seenTitles[item.Title] {
			dr.logger.Warn("Duplicate TV show title found", slog.String("title", item.Title))
			continue
		}

		if content, exists := contentMap[item.Title]; exists {
			content.Date = date
			if item.TMDbID > 0 {
				content.TMDbID = item.TMDbID
			}
			recommendations = append(recommendations, content)
			seenTitles[item.Title] = true
			dr.logger.Debug("Added TV show recommendation", slog.String("title", item.Title))
		} else {
			dr.logger.Warn("TV show not found in available content", slog.String("title", item.Title))
		}
	}

	if len(recommendations) == 0 {
		return fmt.Errorf("no valid recommendations to save")
	}

	dr.logger.Info("Saving recommendations to database", slog.Int("count", len(recommendations)))

	// Save recommendations in a transaction
	if err := dr.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, rec := range recommendations {
			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to save recommendation %s: %w", rec.Title, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save recommendations: %w", err)
	}

	dr.logger.Info("Successfully saved recommendations",
		slog.Int("total_saved", len(recommendations)),
		slog.Time("date", date))

	// Show final summary
	movieCount := 0
	tvShowCount := 0
	for _, rec := range recommendations {
		if rec.Type == "movie" {
			movieCount++
		} else if rec.Type == "tvshow" {
			tvShowCount++
		}
	}

	dr.logger.Info("Recommendation generation completed",
		slog.Int("total_recommendations", len(recommendations)),
		slog.Int("movies", movieCount),
		slog.Int("tv_shows", tvShowCount))

	return nil
}

// formatContent formats recommendations for OpenAI prompts
func (dr *DebugRecommender) formatContent(items []models.Recommendation) string {
	var content strings.Builder
	// Limit to prevent token issues
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

// loadPromptTemplate loads and parses a prompt template
func (dr *DebugRecommender) loadPromptTemplate(filename string) (*template.Template, error) {
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