package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/LukeHagar/plexgo/models/shared"
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
}

func NewApp() (*App, error) {
	db, err := gorm.Open(sqlite.Open("recommender.db"), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Auto-migrate the schema
	err = db.AutoMigrate(&models.Recommendation{}, &models.Movie{}, &models.Anime{}, &models.TVShow{})
	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	plexURL := os.Getenv("PLEX_URL")
	plex := plexgo.New(
		plexgo.WithSecurity(os.Getenv("PLEX_TOKEN")),
		plexgo.WithServerURL(plexURL),
	)

	app := &App{
		db:      db,
		plex:    plex,
		plexURL: plexURL,
		router:  chi.NewRouter(),
	}

	app.setupRoutes()
	return app, nil
}

func (a *App) setupRoutes() {
	a.router.Use(middleware.Logger)
	a.router.Use(middleware.Recoverer)

	a.router.Get("/", a.handleHome)
	a.router.Get("/date/{date}", a.handleDate)
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	// Get today's recommendation or create a new one
	var rec models.Recommendation
	today := time.Now().Truncate(24 * time.Hour)

	result := a.db.Where("date = ?", today).First(&rec)
	if result.Error != nil {
		// Create new recommendation
		rec = models.Recommendation{Date: today}
		if err := a.generateRecommendations(r.Context(), &rec); err != nil {
			http.Error(w, "Failed to generate recommendations", http.StatusInternalServerError)
			return
		}
		a.db.Create(&rec)
	}

	// Load the full recommendation with all relations
	a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID)

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	tmpl.Execute(w, rec)
}

func (a *App) handleDate(w http.ResponseWriter, r *http.Request) {
	dateStr := chi.URLParam(r, "date")
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "Invalid date format", http.StatusBadRequest)
		return
	}

	var rec models.Recommendation
	result := a.db.Where("date = ?", date).First(&rec)
	if result.Error != nil {
		http.Error(w, "Recommendation not found", http.StatusNotFound)
		return
	}

	a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID)

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	tmpl.Execute(w, rec)
}

func (a *App) generateRecommendations(ctx context.Context, rec *models.Recommendation) error {
	// Get Plex libraries
	res, err := a.plex.Library.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	// Generate recommendations for each type
	if err := a.generateMovieRecommendations(ctx, rec, res.Object.MediaContainer.Directory); err != nil {
		return err
	}
	if err := a.generateAnimeRecommendations(ctx, rec); err != nil {
		return err
	}
	if err := a.generateTVShowRecommendations(ctx, rec, res.Object.MediaContainer.Directory); err != nil {
		return err
	}

	return nil
}

func (a *App) generateMovieRecommendations(ctx context.Context, rec *models.Recommendation, libraries []shared.Directory) error {
	// Find the movie library
	var movieLibraryKey int
	for _, lib := range libraries {
		if lib.Type == "movie" {
			key, err := strconv.Atoi(lib.Key)
			if err != nil {
				return fmt.Errorf("invalid library key: %w", err)
			}
			movieLibraryKey = key
			break
		}
	}

	if movieLibraryKey == 0 {
		return fmt.Errorf("no movie library found")
	}

	// Get all movies from Plex
	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		Tag:          "all",
		SectionKey:   movieLibraryKey,
		Type:         operations.GetLibraryItemsQueryParamTypeMovie.ToPointer(),
		IncludeMeta:  operations.GetLibraryItemsQueryParamIncludeMetaEnable.ToPointer(),
		IncludeGuids: operations.IncludeGuidsEnable.ToPointer(),
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %w", err)
	}

	// Create maps for different movie types
	var (
		funnyMovies  []models.Movie
		actionMovies []models.Movie
		dramaMovies  []models.Movie
		seenMovies   []models.Movie
	)

	// Categorize movies
	for _, item := range items.Object.MediaContainer.Metadata {
		var year int
		if item.Year != nil {
			year = *item.Year
		}

		var rating float64
		if item.Rating != nil {
			rating = float64(*item.Rating) / 10.0
		}

		var runtime int
		if item.Duration != nil {
			runtime = *item.Duration / 60000 // Convert milliseconds to minutes
		}

		var viewCount int
		if item.ViewCount != nil {
			viewCount = *item.ViewCount
		}

		// Convert genre slice to strings
		genres := make([]string, len(item.Genre))
		for i, g := range item.Genre {
			genres[i] = string(g)
		}

		movie := models.Movie{
			Title:     item.Title,
			Year:      year,
			Rating:    rating,
			Genre:     strings.Join(genres, ", "),
			Runtime:   runtime,
			PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
			Source:    "plex",
			Seen:      viewCount > 0,
		}

		// Categorize based on genres
		for _, genreStr := range genres {
			genreLower := strings.ToLower(genreStr)
			switch {
			case strings.Contains(genreLower, "comedy"):
				movie.Type = "funny"
				funnyMovies = append(funnyMovies, movie)
			case strings.Contains(genreLower, "action") || strings.Contains(genreLower, "adventure"):
				movie.Type = "action"
				actionMovies = append(actionMovies, movie)
			case strings.Contains(genreLower, "drama"):
				movie.Type = "drama"
				dramaMovies = append(dramaMovies, movie)
			}
		}

		if movie.Seen {
			movie.Type = "seen"
			seenMovies = append(seenMovies, movie)
		}
	}

	// Select recommendations
	var recommendations []models.Movie

	// 1. Add a funny movie you haven't seen
	if len(funnyMovies) > 0 {
		recommendations = append(recommendations, funnyMovies[0])
	}

	// 2. Add an action or drama movie you haven't seen
	actionDrama := append(actionMovies, dramaMovies...)
	if len(actionDrama) > 0 {
		recommendations = append(recommendations, actionDrama[0])
	}

	// 3. Add a movie you've seen before
	if len(seenMovies) > 0 {
		recommendations = append(recommendations, seenMovies[0])
	}

	// Save recommendations
	rec.Movies = recommendations
	return nil
}

func (a *App) generateAnimeRecommendations(ctx context.Context, rec *models.Recommendation) error {
	// Find anime library
	res, err := a.plex.Library.GetAllLibraries(ctx)
	if err != nil {
		return fmt.Errorf("failed to get libraries: %w", err)
	}

	var animeLibraryKey int
	for _, lib := range res.Object.MediaContainer.Directory {
		if strings.Contains(strings.ToLower(lib.Title), "anime") {
			key, err := strconv.Atoi(lib.Key)
			if err != nil {
				return fmt.Errorf("invalid library key: %w", err)
			}
			animeLibraryKey = key
			break
		}
	}

	if animeLibraryKey == 0 {
		return fmt.Errorf("no anime library found")
	}

	// Get all anime from Plex
	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		Tag:          "all",
		SectionKey:   animeLibraryKey,
		Type:         operations.GetLibraryItemsQueryParamTypeShow.ToPointer(),
		IncludeMeta:  operations.GetLibraryItemsQueryParamIncludeMetaEnable.ToPointer(),
		IncludeGuids: operations.IncludeGuidsEnable.ToPointer(),
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %w", err)
	}

	// Filter for unwatched anime
	var unwatchedAnime []models.Anime
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount == 0 {
			anime := models.Anime{
				Title:     item.Title,
				Year:      item.Year,
				Rating:    float64(item.Rating) / 10.0,
				Genre:     strings.Join(item.Genre, ", "),
				Episodes:  item.LeafCount,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedAnime = append(unwatchedAnime, anime)
		}
	}

	// Select up to 3 random unwatched anime
	var recommendations []models.Anime
	for i := 0; i < 3 && i < len(unwatchedAnime); i++ {
		recommendations = append(recommendations, unwatchedAnime[i])
	}

	rec.Anime = recommendations
	return nil
}

func (a *App) generateTVShowRecommendations(ctx context.Context, rec *models.Recommendation, libraries []shared.Directory) error {
	// Find TV show library
	var tvLibraryKey int
	for _, lib := range libraries {
		if lib.Type == "show" && !strings.Contains(strings.ToLower(lib.Title), "anime") {
			key, err := strconv.Atoi(lib.Key)
			if err != nil {
				return fmt.Errorf("invalid library key: %w", err)
			}
			tvLibraryKey = key
			break
		}
	}

	if tvLibraryKey == 0 {
		return fmt.Errorf("no TV show library found")
	}

	// Get all TV shows from Plex
	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		Tag:          "all",
		SectionKey:   tvLibraryKey,
		Type:         operations.GetLibraryItemsQueryParamTypeShow.ToPointer(),
		IncludeMeta:  operations.GetLibraryItemsQueryParamIncludeMetaEnable.ToPointer(),
		IncludeGuids: operations.IncludeGuidsEnable.ToPointer(),
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %w", err)
	}

	// Filter for unwatched TV shows
	var unwatchedShows []models.TVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount == 0 {
			show := models.TVShow{
				Title:     item.Title,
				Year:      item.Year,
				Rating:    float64(item.Rating) / 10.0,
				Genre:     strings.Join(item.Genre, ", "),
				Seasons:   item.ChildCount,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedShows = append(unwatchedShows, show)
		}
	}

	// Select up to 3 random unwatched TV shows
	var recommendations []models.TVShow
	for i := 0; i < 3 && i < len(unwatchedShows); i++ {
		recommendations = append(recommendations, unwatchedShows[i])
	}

	rec.TVShows = recommendations
	return nil
}

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatal(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting server on port %s", port)
	if err := http.ListenAndServe(":"+port, app.router); err != nil {
		log.Fatal(err)
	}
}

func GenerateTags(ctx context.Context, text string) ([]string, error) {
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: fmt.Sprintf("given the journal entry %q, generate a few options of single words to summarize the content. Output should be a comma seperated list.", text),
		},
	}

	req := openai.ChatCompletionRequest{
		Model:    openai.GPT4oMini20240718,
		Messages: messages,
	}

	var tags []string
	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}

	for _, choice := range resp.Choices {
		outText := choice.Message.Content
		newTags := strings.Split(outText, ",")
		for _, tag := range newTags {
			tags = append(tags, strings.TrimSpace(tag))
		}
	}
	log.Printf("tags: %+v", tags)

	return nil, nil
}
