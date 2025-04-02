package recommend

import (
	"context"
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
	Content string
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

	// Check cache first
	cacheKey := date.Format("2006-01-02")
	if _, exists := r.cache[cacheKey]; exists {
		r.logger.Info("Using cached recommendations", slog.String("date", cacheKey))
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

	// Convert unwatched content to recommendations
	recommendations := make([]models.Recommendation, 0)

	// Add movies
	for _, m := range unwatchedMovies {
		recommendations = append(recommendations, models.Recommendation{
			Date:      date,
			Title:     m.Title,
			Type:      "movie",
			Year:      m.Year,
			Rating:    m.Rating,
			Genre:     m.Genre,
			PosterURL: m.PosterURL,
			Runtime:   m.Runtime,
			Source:    "plex",
		})
	}

	// Add anime
	for _, a := range unwatchedAnime {
		recommendations = append(recommendations, models.Recommendation{
			Date:      date,
			Title:     a.Title,
			Type:      "anime",
			Year:      a.Year,
			Rating:    a.Rating,
			Genre:     a.Genre,
			PosterURL: a.PosterURL,
			Runtime:   a.Episodes,
			Source:    "anilist",
		})
	}

	// Add TV shows
	for _, t := range unwatchedTVShows {
		recommendations = append(recommendations, models.Recommendation{
			Date:      date,
			Title:     t.Title,
			Type:      "tvshow",
			Year:      t.Year,
			Rating:    t.Rating,
			Genre:     t.Genre,
			PosterURL: t.PosterURL,
			Runtime:   t.Seasons,
			Source:    "plex",
		})
	}

	// Save recommendations to database
	for _, rec := range recommendations {
		if err := r.db.Create(&rec).Error; err != nil {
			return fmt.Errorf("failed to save recommendation: %w", err)
		}
	}

	// Cache the recommendations
	r.cache[cacheKey] = &recommendations[0] // Cache the first one as a placeholder

	r.logger.Debug("Successfully generated recommendations",
		slog.Int("total_count", len(recommendations)))

	if len(recommendations) == 0 {
		return fmt.Errorf("no recommendations found")
	}

	return nil
}
