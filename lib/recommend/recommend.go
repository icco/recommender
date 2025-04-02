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

type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	tmdb   *tmdb.Client
	logger *slog.Logger
	openai *openai.Client
	cache  map[string]*models.Recommendation
}

type RecommendationContext struct {
	Content                 string
	Preferences             string
	PreviousRecommendations string
}

type UnwatchedContent struct {
	Movies  []models.Recommendation
	Anime   []models.Recommendation
	TVShows []models.Recommendation
}

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

// CheckRecommendationsExist checks if recommendations exist for a specific date
func (r *Recommender) CheckRecommendationsExist(ctx context.Context, date time.Time) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("date = ?", date).Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check recommendations: %w", err)
	}
	return count > 0, nil
}

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

func (r *Recommender) formatContent(items []models.Recommendation) string {
	var content strings.Builder
	for _, item := range items {
		content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s - Runtime: %d - TMDb ID: %d\n",
			item.Title, item.Year, item.Rating, item.Genre, item.Runtime, item.TMDbID))
	}
	return content.String()
}

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

	// Convert movies and TV shows to recommendations for OpenAI
	var allContent []models.Recommendation
	for _, movie := range unwatchedMovies {
		allContent = append(allContent, models.Recommendation{
			Title:     movie.Title,
			Type:      "movie",
			Year:      movie.Year,
			Rating:    movie.Rating,
			Genre:     movie.Genre,
			PosterURL: movie.PosterURL,
			Runtime:   movie.Runtime,
			Source:    movie.Source,
			MovieID:   &movie.ID,
			TMDbID:    movie.TMDbID,
		})
	}

	for _, tvShow := range unwatchedTVShows {
		allContent = append(allContent, models.Recommendation{
			Title:     tvShow.Title,
			Type:      "tvshow",
			Year:      tvShow.Year,
			Rating:    tvShow.Rating,
			Genre:     tvShow.Genre,
			PosterURL: tvShow.PosterURL,
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
		Anime   []RecommendationItem `json:"anime"`
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
	processItems(recResponse.Anime, "anime")
	processItems(recResponse.TVShows, "tvshow")

	// Enforce limits based on content type
	typeCounts := make(map[string]int)
	filteredRecommendations := make([]models.Recommendation, 0)

	for _, rec := range recommendations {
		switch rec.Type {
		case "movie":
			if typeCounts["movie"] < 3 { // 1 funny + 1 action/drama + 1 rewatchable
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
			}
		case "anime":
			if typeCounts["anime"] < 3 {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["anime"]++
			}
		case "tvshow":
			if typeCounts["tvshow"] < 3 {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["tvshow"]++
			}
		}
	}

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
