package recommend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"log/slog"

	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"gorm.io/gorm"
)

type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
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

func New(db *gorm.DB, plex *plex.Client, logger *slog.Logger) (*Recommender, error) {
	openaiClient := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	return &Recommender{
		db:     db,
		plex:   plex,
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
		content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s - Runtime: %d\n",
			item.Title, item.Year, item.Rating, item.Genre, item.Runtime))
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

	// Get Plex libraries
	res, err := r.plex.GetAPI().Library.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	// Get unwatched content from Plex
	r.logger.Debug("Fetching unwatched movies")
	unwatchedMovies, err := r.plex.GetUnwatchedMovies(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get unwatched movies: %w", err)
	}
	r.logger.Debug("Found unwatched movies", slog.Int("count", len(unwatchedMovies)))

	r.logger.Debug("Fetching unwatched anime")
	unwatchedAnime, err := r.plex.GetUnwatchedAnime(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get unwatched anime: %w", err)
	}
	r.logger.Debug("Found unwatched anime", slog.Int("count", len(unwatchedAnime)))

	r.logger.Debug("Fetching unwatched TV shows")
	unwatchedTVShows, err := r.plex.GetUnwatchedTVShows(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return fmt.Errorf("failed to get unwatched TV shows: %w", err)
	}
	r.logger.Debug("Found unwatched TV shows", slog.Int("count", len(unwatchedTVShows)))

	// Get previous recommendations for context
	prevDate := date.AddDate(0, 0, -1)
	prevRecs, err := r.GetRecommendationsForDate(ctx, prevDate)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get previous recommendations: %w", err)
	}

	// Prepare content for OpenAI
	content := RecommendationContext{
		Content: r.formatContent(append(append(unwatchedMovies, unwatchedAnime...), unwatchedTVShows...)),
		Preferences: "User enjoys a mix of genres including action, drama, comedy, and sci-fi. " +
			"Prefers content with high ratings (above 7.5) and appreciates both popular and lesser-known titles.",
		PreviousRecommendations: r.formatContent(prevRecs),
	}

	// Load prompt templates
	systemPrompt, err := r.loadPromptTemplate("prompts/system_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load system prompt: %w", err)
	}

	recommendationPrompt, err := r.loadPromptTemplate("prompts/recommendation_openai.txt")
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
		},
	)
	if err != nil {
		return fmt.Errorf("failed to get OpenAI completion: %w", err)
	}

	// Parse OpenAI response and match with available content
	recommendations := make([]models.Recommendation, 0)
	allContent := append(append(unwatchedMovies, unwatchedAnime...), unwatchedTVShows...)

	// Extract titles from OpenAI response
	lines := strings.Split(resp.Choices[0].Message.Content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Try to match the title from the line
		for _, content := range allContent {
			if strings.Contains(strings.ToLower(line), strings.ToLower(content.Title)) {
				content.Date = date
				recommendations = append(recommendations, content)
				break
			}
		}
	}

	// Save recommendations to database in a transaction
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, rec := range recommendations {
			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to save recommendation: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save recommendations in transaction: %w", err)
	}

	r.logger.Debug("Successfully generated recommendations",
		slog.Int("total_count", len(recommendations)))

	if len(recommendations) == 0 {
		return fmt.Errorf("no recommendations found")
	}

	return nil
}
