package recommend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

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
	Movies  []models.Movie
	Anime   []models.Anime
	TVShows []models.TVShow
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

func (r *Recommender) getUserPreferences(ctx context.Context) (*models.UserPreference, error) {
	var prefs models.UserPreference
	if err := r.db.WithContext(ctx).First(&prefs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Create default preferences if none exist
			prefs = models.UserPreference{}
			if err := prefs.SetFavoriteGenres([]string{"Action", "Drama", "Comedy"}); err != nil {
				return nil, fmt.Errorf("failed to set favorite genres: %w", err)
			}
			if err := prefs.SetThemes([]string{"Character Development", "Storytelling"}); err != nil {
				return nil, fmt.Errorf("failed to set themes: %w", err)
			}
			if err := prefs.SetMoods([]string{"Thought-provoking", "Entertaining"}); err != nil {
				return nil, fmt.Errorf("failed to set moods: %w", err)
			}
			if err := prefs.SetContentLengths([]string{"Medium"}); err != nil {
				return nil, fmt.Errorf("failed to set content lengths: %w", err)
			}
			if err := prefs.SetTimePeriods([]string{"Modern", "Classic"}); err != nil {
				return nil, fmt.Errorf("failed to set time periods: %w", err)
			}
			if err := prefs.SetLanguages([]string{"English", "Japanese"}); err != nil {
				return nil, fmt.Errorf("failed to set languages: %w", err)
			}
			if err := prefs.SetSources([]string{"Plex", "Anilist"}); err != nil {
				return nil, fmt.Errorf("failed to set sources: %w", err)
			}
			if err := r.db.WithContext(ctx).Create(&prefs).Error; err != nil {
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
	if err := r.db.WithContext(ctx).Order("watched_at desc").Limit(10).Find(&ratings).Error; err != nil {
		return nil, fmt.Errorf("failed to get recent ratings: %w", err)
	}
	return ratings, nil
}

func (r *Recommender) formatContent(items interface{}) string {
	var content strings.Builder
	switch v := items.(type) {
	case []models.Movie:
		content.WriteString("Movies:\n")
		for _, m := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", m.Title, m.Year, m.Rating, m.Genre))
		}
	case []models.Anime:
		content.WriteString("Anime:\n")
		for _, a := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", a.Title, a.Year, a.Rating, a.Genre))
		}
	case []models.TVShow:
		content.WriteString("TV Shows:\n")
		for _, t := range v {
			content.WriteString(fmt.Sprintf("- %s (%d) - Rating: %.1f - Genre: %s\n", t.Title, t.Year, t.Rating, t.Genre))
		}
	}
	return content.String()
}

func (r *Recommender) GenerateRecommendations(ctx context.Context, rec *models.Recommendation) error {
	r.logger.Debug("Starting recommendation generation")

	// Check cache first
	cacheKey := rec.Date.Format("2006-01-02")
	if cached, exists := r.cache[cacheKey]; exists {
		*rec = *cached
		r.logger.Info("Using cached recommendations", slog.String("date", cacheKey))
		return nil
	}

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

	// Load OpenAI prompt templates
	openaiSystemTmpl, err := r.loadPromptTemplate("system_openai.txt")
	if err != nil {
		return err
	}

	openaiRecommendationTmpl, err := r.loadPromptTemplate("recommendation_openai.txt")
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

	// Generate OpenAI prompts
	var openaiSystemPrompt strings.Builder
	if err := openaiSystemTmpl.Execute(&openaiSystemPrompt, nil); err != nil {
		return fmt.Errorf("failed to generate OpenAI system prompt: %w", err)
	}

	var openaiRecommendationPrompt strings.Builder
	if err := openaiRecommendationTmpl.Execute(&openaiRecommendationPrompt, ctxData); err != nil {
		return fmt.Errorf("failed to generate OpenAI recommendation prompt: %w", err)
	}

	// Prepare unwatched content
	unwatched := UnwatchedContent{
		Movies:  unwatchedMovies,
		Anime:   unwatchedAnime,
		TVShows: unwatchedTVShows,
	}

	// Generate recommendations from OpenAI
	openaiRecs, err := r.generateOpenAIRecommendations(ctx, openaiSystemPrompt.String(), openaiRecommendationPrompt.String(), unwatched)
	if err != nil {
		return fmt.Errorf("failed to get OpenAI recommendations: %w", err)
	}

	// Use OpenAI recommendations directly
	rec.Movies = openaiRecs.Movies
	rec.Anime = openaiRecs.Anime
	rec.TVShows = openaiRecs.TVShows

	r.logger.Debug("Successfully generated recommendations",
		slog.Int("movies_count", len(rec.Movies)),
		slog.Int("anime_count", len(rec.Anime)),
		slog.Int("tvshows_count", len(rec.TVShows)))

	if len(rec.Movies) == 0 && len(rec.Anime) == 0 && len(rec.TVShows) == 0 {
		return fmt.Errorf("no recommendations found")
	}

	// Save the recommendation to the database
	if err := r.db.Create(rec).Error; err != nil {
		return fmt.Errorf("failed to save recommendation: %w", err)
	}

	// Cache the recommendations
	r.cache[cacheKey] = rec
	return nil
}

func (r *Recommender) generateOpenAIRecommendations(ctx context.Context, systemPrompt, userPrompt string, unwatched UnwatchedContent) (*models.Recommendation, error) {
	resp, err := r.openai.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: userPrompt},
			},
			Temperature: 0.7,
			MaxTokens:   1000,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get OpenAI completion: %w", err)
	}

	// Parse OpenAI response and match with our content
	recommendations := resp.Choices[0].Message.Content
	r.logger.Debug("OpenAI response", slog.String("recommendations", recommendations))

	movies := matchRecommendations(unwatched.Movies, recommendations, "Movies")
	anime := matchRecommendations(unwatched.Anime, recommendations, "Anime")
	tvshows := matchRecommendations(unwatched.TVShows, recommendations, "TV Shows")

	r.logger.Debug("Matched recommendations",
		slog.Int("movies_matched", len(movies)),
		slog.Int("anime_matched", len(anime)),
		slog.Int("tvshows_matched", len(tvshows)))

	return &models.Recommendation{
		Movies:  movies,
		Anime:   anime,
		TVShows: tvshows,
	}, nil
}

func (r *Recommender) formatPreviousRecommendations(ratings []models.UserRating) string {
	if len(ratings) == 0 {
		return "No previous recommendations"
	}

	var content strings.Builder
	content.WriteString("Recent Ratings:\n")

	// Only include the 5 most recent ratings
	for i := 0; i < len(ratings) && i < 5; i++ {
		rating := ratings[i]
		content.WriteString(fmt.Sprintf("- %s (%s) - Rating: %d/5\n",
			rating.ContentType,
			rating.WatchedAt.Format("2006-01-02"),
			rating.Rating))
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
			// Extract title before any parentheses or additional information
			if idx := strings.Index(title, "("); idx != -1 {
				title = strings.TrimSpace(title[:idx])
			}
			if idx := strings.Index(title, " - "); idx != -1 {
				title = strings.TrimSpace(title[:idx])
			}
			// Try to match the title
			for _, item := range items {
				itemTitle := item.GetTitle()
				// Try exact match first
				if strings.EqualFold(itemTitle, title) {
					matched = append(matched, item)
					break
				}
				// Try partial match if exact match fails
				if strings.Contains(strings.ToLower(itemTitle), strings.ToLower(title)) {
					matched = append(matched, item)
					break
				}
			}
		}
	}

	return matched
}
