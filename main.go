package main

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type App struct {
	db      *gorm.DB
	plex    *plexgo.PlexAPI
	plexURL string
	router  *chi.Mux
	logger  *slog.Logger
}

func NewApp() (*App, error) {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "recommender.db"
	}

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Auto-migrate the schema
	err = db.AutoMigrate(
		&models.Recommendation{}, &models.Movie{}, &models.Anime{}, &models.TVShow{},
		&models.PlexCache{}, &models.PlexMovie{}, &models.PlexAnime{}, &models.PlexTVShow{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	plexURL := os.Getenv("PLEX_URL")
	if plexURL == "" {
		return nil, fmt.Errorf("PLEX_URL environment variable is required")
	}

	plexToken := os.Getenv("PLEX_TOKEN")
	if plexToken == "" {
		return nil, fmt.Errorf("PLEX_TOKEN environment variable is required")
	}

	plex := plexgo.New(
		plexgo.WithSecurity(plexToken),
		plexgo.WithServerURL(plexURL),
	)

	// Create JSON logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	app := &App{
		db:      db,
		plex:    plex,
		plexURL: plexURL,
		router:  chi.NewRouter(),
		logger:  logger,
	}

	app.setupRoutes()
	return app, nil
}

func (a *App) setupRoutes() {
	a.router.Use(middleware.Logger)
	a.router.Use(middleware.Recoverer)

	a.router.Get("/", a.handleHome)
	a.router.Get("/date/{date}", a.handleDate)
	a.router.Get("/cron", a.handleCron)
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	// Get today's recommendation
	var rec models.Recommendation
	today := time.Now().Truncate(24 * time.Hour)

	result := a.db.Where("date = ?", today).First(&rec)
	if result.Error != nil {
		a.logger.Info("No recommendation found for today", slog.String("date", today.Format("2006-01-02")))
		tmpl := template.Must(template.ParseFiles("templates/home.html"))
		if err := tmpl.Execute(w, models.Recommendation{}); err != nil {
			a.logger.Error("Failed to execute template", slog.Any("error", err))
			http.Error(w, fmt.Sprintf("Failed to render page: %v", err), http.StatusInternalServerError)
			return
		}
		return
	}

	// Load the full recommendation with all relations
	if err := a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID).Error; err != nil {
		a.logger.Error("Failed to load recommendation with relations",
			slog.Any("error", err),
			slog.Int("recommendation_id", int(rec.ID)))
		http.Error(w, fmt.Sprintf("Failed to load recommendation: %v", err), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	if err := tmpl.Execute(w, rec); err != nil {
		a.logger.Error("Failed to execute template", slog.Any("error", err))
		http.Error(w, fmt.Sprintf("Failed to render page: %v", err), http.StatusInternalServerError)
		return
	}
}

func (a *App) handleDate(w http.ResponseWriter, r *http.Request) {
	dateStr := chi.URLParam(r, "date")
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		a.logger.Error("Invalid date format", slog.Any("error", err), slog.String("date", dateStr))
		http.Error(w, "Invalid date format", http.StatusBadRequest)
		return
	}

	var rec models.Recommendation
	result := a.db.Where("date = ?", date).First(&rec)
	if result.Error != nil {
		a.logger.Info("Recommendation not found for date", slog.String("date", dateStr))
		http.Error(w, "Recommendation not found", http.StatusNotFound)
		return
	}

	if err := a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID).Error; err != nil {
		a.logger.Error("Failed to load recommendation with relations",
			slog.Any("error", err),
			slog.Int("recommendation_id", int(rec.ID)),
			slog.String("date", dateStr))
		http.Error(w, fmt.Sprintf("Failed to load recommendation: %v", err), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	if err := tmpl.Execute(w, rec); err != nil {
		a.logger.Error("Failed to execute template", slog.Any("error", err))
		http.Error(w, fmt.Sprintf("Failed to render page: %v", err), http.StatusInternalServerError)
		return
	}
}

func (a *App) handleCron(w http.ResponseWriter, r *http.Request) {
	a.logger.Info("Starting cron job")

	// First, update the Plex cache
	a.logger.Debug("Updating Plex cache")
	if err := a.updatePlexCache(r.Context()); err != nil {
		a.logger.Error("Failed to update Plex cache", slog.Any("error", err))
		http.Error(w, fmt.Sprintf("Failed to update Plex cache: %v", err), http.StatusInternalServerError)
		return
	}
	a.logger.Info("Successfully updated Plex cache")

	// Check if we already have a recommendation for today
	var existingRec models.Recommendation
	today := time.Now().Truncate(24 * time.Hour)

	result := a.db.Where("date = ?", today).First(&existingRec)
	if result.Error == nil {
		a.logger.Info("Recommendation already exists for today", slog.String("date", today.Format("2006-01-02")))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Recommendation already exists for today"))
		return
	}

	// Create new recommendation
	a.logger.Debug("Generating new recommendations", slog.String("date", today.Format("2006-01-02")))
	rec := models.Recommendation{Date: today}
	if err := a.generateRecommendations(r.Context(), &rec); err != nil {
		a.logger.Error("Failed to generate recommendations", slog.Any("error", err))
		http.Error(w, fmt.Sprintf("Failed to generate recommendations: %v", err), http.StatusInternalServerError)
		return
	}

	if err := a.db.Create(&rec).Error; err != nil {
		a.logger.Error("Failed to save recommendation",
			slog.Any("error", err),
			slog.Int("recommendation_id", int(rec.ID)))
		http.Error(w, fmt.Sprintf("Failed to save recommendation: %v", err), http.StatusInternalServerError)
		return
	}

	a.logger.Info("Successfully generated new recommendations",
		slog.Int("recommendation_id", int(rec.ID)),
		slog.String("date", today.Format("2006-01-02")))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Successfully generated new recommendations"))
}

func (a *App) generateRecommendations(ctx context.Context, rec *models.Recommendation) error {
	a.logger.Debug("Starting recommendation generation")

	// Get Plex libraries
	res, err := a.plex.Library.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	// Get unwatched content from Plex
	a.logger.Debug("Fetching unwatched movies")
	unwatchedMovies, err := a.getUnwatchedMovies(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	a.logger.Debug("Found unwatched movies", slog.Int("count", len(unwatchedMovies)))

	a.logger.Debug("Fetching unwatched anime")
	unwatchedAnime, err := a.getUnwatchedAnime(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	a.logger.Debug("Found unwatched anime", slog.Int("count", len(unwatchedAnime)))

	a.logger.Debug("Fetching unwatched TV shows")
	unwatchedTVShows, err := a.getUnwatchedTVShows(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	a.logger.Debug("Found unwatched TV shows", slog.Int("count", len(unwatchedTVShows)))

	// Use OpenAI to generate recommendations
	a.logger.Debug("Generating recommendations with OpenAI")
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
		Model:    openai.GPT4oMini20240718,
		Messages: messages,
	}

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to get OpenAI recommendations: %w", err)
	}

	// Parse OpenAI response and match with our content
	a.logger.Debug("Matching OpenAI recommendations with content")
	recommendations := resp.Choices[0].Message.Content
	rec.Movies = matchRecommendations(unwatchedMovies, recommendations, "Movies")
	rec.Anime = matchRecommendations(unwatchedAnime, recommendations, "Anime")
	rec.TVShows = matchRecommendations(unwatchedTVShows, recommendations, "TV Shows")

	a.logger.Debug("Successfully matched recommendations",
		slog.Int("movies_count", len(rec.Movies)),
		slog.Int("anime_count", len(rec.Anime)),
		slog.Int("tvshows_count", len(rec.TVShows)))

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

// getPlexLibraryKey finds the library key for a given type and title condition
func getPlexLibraryKey(libraries []operations.GetAllLibrariesDirectory, libType string, titleCondition func(string) bool) (string, error) {
	for _, lib := range libraries {
		if lib.Type == libType && (titleCondition == nil || titleCondition(lib.Title)) {
			return lib.Key, nil
		}
	}
	return "", fmt.Errorf("no matching library found")
}

// getPlexItems gets items from a Plex library
func (a *App) getPlexItems(ctx context.Context, libraryKey string) (*operations.GetLibraryItemsResponse, error) {
	sectionKey, err := strconv.Atoi(libraryKey)
	if err != nil {
		return nil, fmt.Errorf("invalid library key: %v", err)
	}

	return a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		SectionKey: sectionKey,
		Tag:        "all",
	})
}

// getUnwatchedMovies gets unwatched movies from Plex
func (a *App) getUnwatchedMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Movie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, movieLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedMovies []models.Movie
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			movie := models.Movie{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Runtime:   getIntValue(item.Duration) / 60000,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedMovies = append(unwatchedMovies, movie)
		}
	}

	return unwatchedMovies, nil
}

// getUnwatchedAnime gets unwatched anime from Plex
func (a *App) getUnwatchedAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.Anime, error) {
	animeLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, animeLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedAnime []models.Anime
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			anime := models.Anime{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Episodes:  getIntValue(item.LeafCount),
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedAnime = append(unwatchedAnime, anime)
		}
	}

	return unwatchedAnime, nil
}

// getUnwatchedTVShows gets unwatched TV shows from Plex
func (a *App) getUnwatchedTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.TVShow, error) {
	tvLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return !strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, tvLibraryKey)
	if err != nil {
		return nil, err
	}

	var unwatchedTVShows []models.TVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			tvShow := models.TVShow{
				Title:     item.Title,
				Year:      getIntValue(item.Year),
				Rating:    getFloatValue(item.Rating),
				Genre:     getGenres(item.Genre),
				Seasons:   getIntValue(item.ChildCount),
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedTVShows = append(unwatchedTVShows, tvShow)
		}
	}

	return unwatchedTVShows, nil
}

// Helper functions for Plex data extraction
func getIntValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func getFloatValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func getGenres(genres []operations.GetLibraryItemsGenre) string {
	var genreStrings []string
	for _, g := range genres {
		if g.Tag != nil {
			genreStrings = append(genreStrings, *g.Tag)
		}
	}
	return strings.Join(genreStrings, ", ")
}

func (a *App) updatePlexCache(ctx context.Context) error {
	a.logger.Debug("Starting Plex cache update")

	// Get Plex libraries
	res, err := a.plex.Library.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	// Create new cache entry
	cache := models.PlexCache{
		UpdatedAt: time.Now(),
	}

	// Get all content from Plex
	a.logger.Debug("Fetching all movies from Plex")
	movies, err := a.getAllMovies(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	cache.Movies = movies
	a.logger.Debug("Found movies",
		slog.Int("total_count", len(movies)),
		slog.Int("watched", countWatched(movies)))

	a.logger.Debug("Fetching all anime from Plex")
	anime, err := a.getAllAnime(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	cache.Anime = anime
	a.logger.Debug("Found anime",
		slog.Int("total_count", len(anime)),
		slog.Int("watched", countWatched(anime)))

	a.logger.Debug("Fetching all TV shows from Plex")
	tvShows, err := a.getAllTVShows(ctx, res.Object.MediaContainer.Directory)
	if err != nil {
		return err
	}
	cache.TVShows = tvShows
	a.logger.Debug("Found TV shows",
		slog.Int("total_count", len(tvShows)),
		slog.Int("watched", countWatched(tvShows)))

	// Save the cache
	a.logger.Debug("Saving Plex cache to database")
	if err := a.db.Create(&cache).Error; err != nil {
		return fmt.Errorf("failed to save Plex cache: %w", err)
	}

	a.logger.Info("Successfully updated Plex cache",
		slog.Int("cache_id", int(cache.ID)),
		slog.Int("movies_count", len(cache.Movies)),
		slog.Int("anime_count", len(cache.Anime)),
		slog.Int("tvshows_count", len(cache.TVShows)))

	return nil
}

func countWatched[T interface{ IsWatched() bool }](items []T) int {
	count := 0
	for _, item := range items {
		if item.IsWatched() {
			count++
		}
	}
	return count
}

func (a *App) getAllMovies(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexMovie, error) {
	movieLibraryKey, err := getPlexLibraryKey(libraries, "movie", nil)
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, movieLibraryKey)
	if err != nil {
		return nil, err
	}

	var movies []models.PlexMovie
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
		}

		movie := models.PlexMovie{
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Runtime:   getIntValue(item.Duration) / 60000,
			PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
			Watched:   watched,
		}
		movies = append(movies, movie)
	}

	return movies, nil
}

func (a *App) getAllAnime(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexAnime, error) {
	animeLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, animeLibraryKey)
	if err != nil {
		return nil, err
	}

	var anime []models.PlexAnime
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
		}

		animeItem := models.PlexAnime{
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Episodes:  getIntValue(item.LeafCount),
			PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
			Watched:   watched,
		}
		anime = append(anime, animeItem)
	}

	return anime, nil
}

func (a *App) getAllTVShows(ctx context.Context, libraries []operations.GetAllLibrariesDirectory) ([]models.PlexTVShow, error) {
	tvLibraryKey, err := getPlexLibraryKey(libraries, "show", func(title string) bool {
		return !strings.Contains(strings.ToLower(title), "anime")
	})
	if err != nil {
		return nil, err
	}

	items, err := a.getPlexItems(ctx, tvLibraryKey)
	if err != nil {
		return nil, err
	}

	var tvShows []models.PlexTVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		watched := false
		if item.ViewCount != nil && *item.ViewCount > 0 {
			watched = true
		}

		tvShow := models.PlexTVShow{
			Title:     item.Title,
			Year:      getIntValue(item.Year),
			Rating:    getFloatValue(item.Rating),
			Genre:     getGenres(item.Genre),
			Seasons:   getIntValue(item.ChildCount),
			PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
			Watched:   watched,
		}
		tvShows = append(tvShows, tvShow)
	}

	return tvShows, nil
}

func main() {
	app, err := NewApp()
	if err != nil {
		slog.Error("Failed to create app", slog.Any("error", err))
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	slog.Info("Starting server", slog.String("port", port))
	if err := http.ListenAndServe(":"+port, app.router); err != nil {
		slog.Error("Server error", slog.Any("error", err))
		os.Exit(1)
	}
}
