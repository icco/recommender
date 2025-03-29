package recommender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"log/slog"

	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"gorm.io/gorm"
)

type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	logger *slog.Logger
}

type RecommendationContext struct {
	Content                 string
	Preferences             string
	PreviousRecommendations string
}

func New(db *gorm.DB, plex *plex.Client, logger *slog.Logger) *Recommender {
	return &Recommender{
		db:     db,
		plex:   plex,
		logger: logger,
	}
}

func (r *Recommender) loadPromptTemplate(name string) (*template.Template, error) {
	content, err := os.ReadFile(filepath.Join("lib/recommender/prompts", name))
	if err != nil {
		return nil, fmt.Errorf("failed to read prompt template %s: %w", name, err)
	}
	return template.New(name).Parse(string(content))
}

func (r *Recommender) getUserPreferences(ctx context.Context) (*models.UserPreference, error) {
	var prefs models.UserPreference
	if err := r.db.First(&prefs).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// Create default preferences if none exist
			prefs = models.UserPreference{
				FavoriteGenres: []string{"Action", "Drama", "Comedy"},
				Themes:         []string{"Character Development", "Storytelling"},
				Moods:          []string{"Thought-provoking", "Entertaining"},
				ContentLengths: []string{"Medium"},
				TimePeriods:    []string{"Modern", "Classic"},
				Languages:      []string{"English", "Japanese"},
				Sources:        []string{"Plex", "Anilist"},
			}
			if err := r.db.Create(&prefs).Error; err != nil {
				return nil, fmt.Errorf("failed to create default preferences: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to get user preferences: %w", err)
		}
	}
	return &prefs, nil
}

func (r *Recommender) getRecentRatings(ctx context.Context) ([]models.UserRating, error) {
	var ratings []models.UserRating
	if err := r.db.Order("watched_at desc").Limit(10).Find(&ratings).Error; err != nil {
		return nil, fmt.Errorf("failed to get recent ratings: %w", err)
	}
	return ratings, nil
}

func (r *Recommender) formatContent(items interface{}) string {
	var content strings.Builder
	switch v := items.(type) {
	case []models.PlexMovie:
		content.WriteString("Movies:\n")
		for _, m := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", m.Title, m.Year, m.Rating, m.Genre))
		}
	case []models.PlexAnime:
		content.WriteString("Anime:\n")
		for _, a := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", a.Title, a.Year, a.Rating, a.Genre))
		}
	case []models.PlexTVShow:
		content.WriteString("TV Shows:\n")
		for _, t := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", t.Title, t.Year, t.Rating, t.Genre))
		}
	}
	return content.String()
}

func (r *Recommender) GenerateRecommendations(ctx context.Context, rec *models.Recommendation) error {
	r.logger.Debug("Starting recommendation generation")

	// Get user preferences and recent ratings
	prefs, err := r.getUserPreferences(ctx)
	if err != nil {
		return err
	}

	ratings, err := r.getRecentRatings(ctx)
	if err != nil {
		return err
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
		return err
	}
	r.logger.Debug("Found unwatched movies", slog.Int("count", len(unwatchedMovies)))

	r.logger.Debug("Fetching unwatched anime")
	unwatchedAnime, err := r.plex.GetUnwatchedAnime(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	r.logger.Debug("Found unwatched anime", slog.Int("count", len(unwatchedAnime)))

	r.logger.Debug("Fetching unwatched TV shows")
	unwatchedTVShows, err := r.plex.GetUnwatchedTVShows(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	r.logger.Debug("Found unwatched TV shows", slog.Int("count", len(unwatchedTVShows)))

	// Load prompt templates
	systemTmpl, err := r.loadPromptTemplate("system.txt")
	if err != nil {
		return err
	}

	recommendationTmpl, err := r.loadPromptTemplate("recommendation.txt")
	if err != nil {
		return err
	}

	// Prepare context for templates
	ctxData := RecommendationContext{
		Content: fmt.Sprintf("%s\n%s\n%s",
			r.formatContent(unwatchedMovies),
			r.formatContent(unwatchedAnime),
			r.formatContent(unwatchedTVShows)),
		Preferences: fmt.Sprintf("Favorite Genres: %v\nThemes: %v\nMoods: %v\nContent Lengths: %v\nTime Periods: %v\nLanguages: %v\nSources: %v",
			prefs.FavoriteGenres, prefs.Themes, prefs.Moods, prefs.ContentLengths, prefs.TimePeriods, prefs.Languages, prefs.Sources),
		PreviousRecommendations: r.formatPreviousRecommendations(ratings),
	}

	// Generate system prompt
	var systemPrompt strings.Builder
	if err := systemTmpl.Execute(&systemPrompt, nil); err != nil {
		return fmt.Errorf("failed to generate system prompt: %w", err)
	}

	// Generate recommendation prompt
	var recommendationPrompt strings.Builder
	if err := recommendationTmpl.Execute(&recommendationPrompt, ctxData); err != nil {
		return fmt.Errorf("failed to generate recommendation prompt: %w", err)
	}

	// Use OpenAI to generate recommendations
	r.logger.Debug("Generating recommendations with OpenAI")
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt.String(),
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: recommendationPrompt.String(),
		},
	}

	req := openai.ChatCompletionRequest{
		Model:    openai.GPT4,
		Messages: messages,
	}

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to get OpenAI recommendations: %w", err)
	}

	// Parse OpenAI response and match with our content
	r.logger.Debug("Matching OpenAI recommendations with content")
	recommendations := resp.Choices[0].Message.Content
	rec.Movies = matchRecommendations(unwatchedMovies, recommendations, "Movies")
	rec.Anime = matchRecommendations(unwatchedAnime, recommendations, "Anime")
	rec.TVShows = matchRecommendations(unwatchedTVShows, recommendations, "TV Shows")

	r.logger.Debug("Successfully matched recommendations",
		slog.Int("movies_count", len(rec.Movies)),
		slog.Int("anime_count", len(rec.Anime)),
		slog.Int("tvshows_count", len(rec.TVShows)))

	// Check if we found any recommendations
	if len(rec.Movies) == 0 && len(rec.Anime) == 0 && len(rec.TVShows) == 0 {
		return fmt.Errorf("no recommendations found in OpenAI response")
	}

	// Save the recommendation to the database
	if err := r.db.Create(rec).Error; err != nil {
		return fmt.Errorf("failed to save recommendation: %w", err)
	}

	return nil
}

func (r *Recommender) formatPreviousRecommendations(ratings []models.UserRating) string {
	var content strings.Builder
	content.WriteString("Recent Ratings:\n")
	for _, rating := range ratings {
		content.WriteString(fmt.Sprintf("- %s (Rating: %d) - %s\n", rating.ContentType, rating.Rating, rating.Review))
		if len(rating.Tags) > 0 {
			content.WriteString(fmt.Sprintf("  Tags: %v\n", rating.Tags))
		}
	}
	return content.String()
}

// matchRecommendations matches OpenAI recommendations with content items
func matchRecommendations[T interface{ GetTitle() string }](items []T, recommendations string, category string) []T {
	var matched []T
	lines := strings.Split(recommendations, "\n")
	inCategory := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, category+":") {
			inCategory = true
			continue
		}
		if inCategory && line == "" {
			break
		}
		if inCategory && strings.HasPrefix(line, "-") {
			title := strings.TrimPrefix(line, "-")
			title = strings.TrimSpace(title)
			// Extract title before any parentheses
			if idx := strings.Index(title, "("); idx != -1 {
				title = strings.TrimSpace(title[:idx])
			}
			for _, item := range items {
				if strings.EqualFold(item.GetTitle(), title) {
					matched = append(matched, item)
					break
				}
			}
		}
	}

	return matched
}
