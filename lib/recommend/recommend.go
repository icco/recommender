package recommend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	"log/slog"

	"cloud.google.com/go/vertexai/genai"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"google.golang.org/api/option"
	"gorm.io/gorm"
)

type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	logger *slog.Logger
	openai *openai.Client
	gemini *genai.Client
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

	ctx := context.Background()
	geminiClient, err := genai.NewClient(ctx, "recommender", "us-central1", option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &Recommender{
		db:     db,
		plex:   plex,
		logger: logger,
		openai: openaiClient,
		gemini: geminiClient,
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
	if err := r.db.First(&prefs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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

	// Load model-specific prompt templates
	openaiSystemTmpl, err := r.loadPromptTemplate("system_openai.txt")
	if err != nil {
		return err
	}

	openaiRecommendationTmpl, err := r.loadPromptTemplate("recommendation_openai.txt")
	if err != nil {
		return err
	}

	geminiSystemTmpl, err := r.loadPromptTemplate("system_gemini.txt")
	if err != nil {
		return err
	}

	geminiRecommendationTmpl, err := r.loadPromptTemplate("recommendation_gemini.txt")
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

	// Generate Gemini prompts
	var geminiSystemPrompt strings.Builder
	if err := geminiSystemTmpl.Execute(&geminiSystemPrompt, nil); err != nil {
		return fmt.Errorf("failed to generate Gemini system prompt: %w", err)
	}

	var geminiRecommendationPrompt strings.Builder
	if err := geminiRecommendationTmpl.Execute(&geminiRecommendationPrompt, ctxData); err != nil {
		return fmt.Errorf("failed to generate Gemini recommendation prompt: %w", err)
	}

	// Prepare unwatched content
	unwatched := UnwatchedContent{
		Movies:  unwatchedMovies,
		Anime:   unwatchedAnime,
		TVShows: unwatchedTVShows,
	}

	// Generate recommendations from both OpenAI and Gemini
	openaiRecs, err := r.generateOpenAIRecommendations(ctx, openaiSystemPrompt.String(), openaiRecommendationPrompt.String(), unwatched)
	if err != nil {
		return fmt.Errorf("failed to get OpenAI recommendations: %w", err)
	}

	geminiRecs, err := r.generateGeminiRecommendations(ctx, geminiSystemPrompt.String(), geminiRecommendationPrompt.String(), unwatched)
	if err != nil {
		return fmt.Errorf("failed to get Gemini recommendations: %w", err)
	}

	// Combine and deduplicate recommendations
	allMovies := make(map[string]models.Movie)
	allAnime := make(map[string]models.Anime)
	allTVShows := make(map[string]models.TVShow)

	// Add OpenAI recommendations
	for _, m := range openaiRecs.Movies {
		allMovies[m.Title] = m
	}
	for _, a := range openaiRecs.Anime {
		allAnime[a.Title] = a
	}
	for _, t := range openaiRecs.TVShows {
		allTVShows[t.Title] = t
	}

	// Add Gemini recommendations
	for _, m := range geminiRecs.Movies {
		allMovies[m.Title] = m
	}
	for _, a := range geminiRecs.Anime {
		allAnime[a.Title] = a
	}
	for _, t := range geminiRecs.TVShows {
		allTVShows[t.Title] = t
	}

	// Convert maps back to slices
	rec.Movies = make([]models.Movie, 0, len(allMovies))
	for _, m := range allMovies {
		rec.Movies = append(rec.Movies, m)
	}

	rec.Anime = make([]models.Anime, 0, len(allAnime))
	for _, a := range allAnime {
		rec.Anime = append(rec.Anime, a)
	}

	rec.TVShows = make([]models.TVShow, 0, len(allTVShows))
	for _, t := range allTVShows {
		rec.TVShows = append(rec.TVShows, t)
	}

	r.logger.Debug("Successfully combined recommendations",
		slog.Int("movies_count", len(rec.Movies)),
		slog.Int("anime_count", len(rec.Anime)),
		slog.Int("tvshows_count", len(rec.TVShows)))

	// Check if we found any recommendations
	if len(rec.Movies) == 0 && len(rec.Anime) == 0 && len(rec.TVShows) == 0 {
		return fmt.Errorf("no recommendations found from either model")
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
	return &models.Recommendation{
		Movies:  matchRecommendations(unwatched.Movies, recommendations, "Movies"),
		Anime:   matchRecommendations(unwatched.Anime, recommendations, "Anime"),
		TVShows: matchRecommendations(unwatched.TVShows, recommendations, "TV Shows"),
	}, nil
}

func (r *Recommender) generateGeminiRecommendations(ctx context.Context, systemPrompt, userPrompt string, unwatched UnwatchedContent) (*models.Recommendation, error) {
	model := r.gemini.GenerativeModel("gemini-pro")
	resp, err := model.GenerateContent(ctx, genai.Text(systemPrompt+"\n\n"+userPrompt))
	if err != nil {
		return nil, fmt.Errorf("failed to get Gemini completion: %w", err)
	}

	// Parse Gemini response and match with our content
	recommendations := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	return &models.Recommendation{
		Movies:  matchRecommendations(unwatched.Movies, recommendations, "Movies"),
		Anime:   matchRecommendations(unwatched.Anime, recommendations, "Anime"),
		TVShows: matchRecommendations(unwatched.TVShows, recommendations, "TV Shows"),
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
