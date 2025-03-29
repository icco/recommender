package recommender

import (
	"context"
	"fmt"
	"os"
	"strings"

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

func New(db *gorm.DB, plex *plex.Client, logger *slog.Logger) *Recommender {
	return &Recommender{
		db:     db,
		plex:   plex,
		logger: logger,
	}
}

func (r *Recommender) GenerateRecommendations(ctx context.Context, rec *models.Recommendation) error {
	r.logger.Debug("Starting recommendation generation")

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

	// Use OpenAI to generate recommendations
	r.logger.Debug("Generating recommendations with OpenAI")
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	// Prepare content for OpenAI
	var content string
	content += "Movies:\n"
	for _, m := range unwatchedMovies {
		content += fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", m.Title, m.Year, m.Rating, m.Genre)
	}
	content += "\nAnime:\n"
	for _, a := range unwatchedAnime {
		content += fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", a.Title, a.Year, a.Rating, a.Genre)
	}
	content += "\nTV Shows:\n"
	for _, t := range unwatchedTVShows {
		content += fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", t.Title, t.Year, t.Rating, t.Genre)
	}

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: "You are a media recommendation expert. Based on the provided unwatched content, select the most interesting and diverse recommendations. Consider ratings, genres, and overall appeal. Select up to 3 items from each category.",
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: content,
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
