package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"math/rand"
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
	err = db.AutoMigrate(&models.Recommendation{}, &models.Movie{}, &models.Anime{}, &models.TVShow{})
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
		log.Printf("No recommendation found for today, creating new one: %v", result.Error)
		// Create new recommendation
		rec = models.Recommendation{Date: today}
		if err := a.generateRecommendations(r.Context(), &rec); err != nil {
			log.Printf("Failed to generate recommendations: %v", err)
			http.Error(w, fmt.Sprintf("Failed to generate recommendations: %v", err), http.StatusInternalServerError)
			return
		}
		if err := a.db.Create(&rec).Error; err != nil {
			log.Printf("Failed to save recommendation: %v", err)
			http.Error(w, fmt.Sprintf("Failed to save recommendation: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Load the full recommendation with all relations
	if err := a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID).Error; err != nil {
		log.Printf("Failed to load recommendation with relations: %v", err)
		http.Error(w, fmt.Sprintf("Failed to load recommendation: %v", err), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	if err := tmpl.Execute(w, rec); err != nil {
		log.Printf("Failed to execute template: %v", err)
		http.Error(w, fmt.Sprintf("Failed to render page: %v", err), http.StatusInternalServerError)
		return
	}
}

func (a *App) handleDate(w http.ResponseWriter, r *http.Request) {
	dateStr := chi.URLParam(r, "date")
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		log.Printf("Invalid date format: %v", err)
		http.Error(w, "Invalid date format", http.StatusBadRequest)
		return
	}

	var rec models.Recommendation
	result := a.db.Where("date = ?", date).First(&rec)
	if result.Error != nil {
		log.Printf("Recommendation not found for date %s: %v", dateStr, result.Error)
		http.Error(w, "Recommendation not found", http.StatusNotFound)
		return
	}

	if err := a.db.Preload("Movies").Preload("Anime").Preload("TVShows").First(&rec, rec.ID).Error; err != nil {
		log.Printf("Failed to load recommendation with relations: %v", err)
		http.Error(w, fmt.Sprintf("Failed to load recommendation: %v", err), http.StatusInternalServerError)
		return
	}

	tmpl := template.Must(template.ParseFiles("templates/home.html"))
	if err := tmpl.Execute(w, rec); err != nil {
		log.Printf("Failed to execute template: %v", err)
		http.Error(w, fmt.Sprintf("Failed to render page: %v", err), http.StatusInternalServerError)
		return
	}
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
	if err := a.generateAnimeRecommendations(ctx, rec, res.Object.MediaContainer.Directory); err != nil {
		return err
	}
	if err := a.generateTVShowRecommendations(ctx, rec, res.Object.MediaContainer.Directory); err != nil {
		return err
	}

	return nil
}

func (a *App) generateMovieRecommendations(ctx context.Context, rec *models.Recommendation, libraries []operations.GetAllLibrariesDirectory) error {
	var movieLibraryKey string
	for _, lib := range libraries {
		if lib.Type == "movie" {
			movieLibraryKey = lib.Key
			break
		}
	}

	if movieLibraryKey == "" {
		return fmt.Errorf("no movie library found")
	}

	// Convert library key to int
	sectionKey, err := strconv.Atoi(movieLibraryKey)
	if err != nil {
		return fmt.Errorf("invalid library key: %v", err)
	}

	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		SectionKey: sectionKey,
		Tag:        "all",
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %v", err)
	}

	var unwatchedMovies []models.Movie
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			var year int
			if item.Year != nil {
				year = *item.Year
			}

			var rating float64
			if item.Rating != nil {
				rating = *item.Rating
			}

			var runtime int
			if item.Duration != nil {
				runtime = *item.Duration / 60000 // Convert to minutes
			}

			var genres []string
			for _, g := range item.Genre {
				if g.Tag != nil {
					genres = append(genres, *g.Tag)
				}
			}

			movie := models.Movie{
				Title:     item.Title,
				Year:      year,
				Rating:    rating,
				Genre:     strings.Join(genres, ", "),
				Runtime:   runtime,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedMovies = append(unwatchedMovies, movie)
		}
	}

	if len(unwatchedMovies) == 0 {
		return fmt.Errorf("no unwatched movies found")
	}

	// Randomly select up to 3 movies
	rand.Shuffle(len(unwatchedMovies), func(i, j int) {
		unwatchedMovies[i], unwatchedMovies[j] = unwatchedMovies[j], unwatchedMovies[i]
	})

	if len(unwatchedMovies) > 3 {
		unwatchedMovies = unwatchedMovies[:3]
	}

	rec.Movies = unwatchedMovies
	return nil
}

func (a *App) generateAnimeRecommendations(ctx context.Context, rec *models.Recommendation, libraries []operations.GetAllLibrariesDirectory) error {
	var animeLibraryKey string
	for _, lib := range libraries {
		if strings.Contains(strings.ToLower(lib.Title), "anime") {
			animeLibraryKey = lib.Key
			break
		}
	}

	if animeLibraryKey == "" {
		return fmt.Errorf("no anime library found")
	}

	// Convert library key to int
	sectionKey, err := strconv.Atoi(animeLibraryKey)
	if err != nil {
		return fmt.Errorf("invalid library key: %v", err)
	}

	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		SectionKey: sectionKey,
		Tag:        "all",
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %v", err)
	}

	var unwatchedAnime []models.Anime
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			var year int
			if item.Year != nil {
				year = *item.Year
			}

			var rating float64
			if item.Rating != nil {
				rating = *item.Rating
			}

			var episodes int
			if item.LeafCount != nil {
				episodes = *item.LeafCount
			}

			var genres []string
			for _, g := range item.Genre {
				if g.Tag != nil {
					genres = append(genres, *g.Tag)
				}
			}

			anime := models.Anime{
				Title:     item.Title,
				Year:      year,
				Rating:    rating,
				Genre:     strings.Join(genres, ", "),
				Episodes:  episodes,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedAnime = append(unwatchedAnime, anime)
		}
	}

	if len(unwatchedAnime) == 0 {
		return fmt.Errorf("no unwatched anime found")
	}

	// Randomly select up to 3 anime
	rand.Shuffle(len(unwatchedAnime), func(i, j int) {
		unwatchedAnime[i], unwatchedAnime[j] = unwatchedAnime[j], unwatchedAnime[i]
	})

	if len(unwatchedAnime) > 3 {
		unwatchedAnime = unwatchedAnime[:3]
	}

	rec.Anime = unwatchedAnime
	return nil
}

func (a *App) generateTVShowRecommendations(ctx context.Context, rec *models.Recommendation, libraries []operations.GetAllLibrariesDirectory) error {
	var tvLibraryKey string
	for _, lib := range libraries {
		if lib.Type == "show" && !strings.Contains(strings.ToLower(lib.Title), "anime") {
			tvLibraryKey = lib.Key
			break
		}
	}

	if tvLibraryKey == "" {
		return fmt.Errorf("no TV show library found")
	}

	// Convert library key to int
	sectionKey, err := strconv.Atoi(tvLibraryKey)
	if err != nil {
		return fmt.Errorf("invalid library key: %v", err)
	}

	items, err := a.plex.Library.GetLibraryItems(ctx, operations.GetLibraryItemsRequest{
		SectionKey: sectionKey,
		Tag:        "all",
	})
	if err != nil {
		return fmt.Errorf("failed to get library items: %v", err)
	}

	var unwatchedTVShows []models.TVShow
	for _, item := range items.Object.MediaContainer.Metadata {
		if item.ViewCount != nil && *item.ViewCount == 0 {
			var year int
			if item.Year != nil {
				year = *item.Year
			}

			var rating float64
			if item.Rating != nil {
				rating = *item.Rating
			}

			var seasons int
			if item.ChildCount != nil {
				seasons = *item.ChildCount
			}

			var genres []string
			for _, g := range item.Genre {
				if g.Tag != nil {
					genres = append(genres, *g.Tag)
				}
			}

			tvShow := models.TVShow{
				Title:     item.Title,
				Year:      year,
				Rating:    rating,
				Genre:     strings.Join(genres, ", "),
				Seasons:   seasons,
				PosterURL: fmt.Sprintf("%s%s", a.plexURL, item.Thumb),
				Source:    "plex",
			}
			unwatchedTVShows = append(unwatchedTVShows, tvShow)
		}
	}

	if len(unwatchedTVShows) == 0 {
		return fmt.Errorf("no unwatched TV shows found")
	}

	// Randomly select up to 3 TV shows
	rand.Shuffle(len(unwatchedTVShows), func(i, j int) {
		unwatchedTVShows[i], unwatchedTVShows[j] = unwatchedTVShows[j], unwatchedTVShows[i]
	})

	if len(unwatchedTVShows) > 3 {
		unwatchedTVShows = unwatchedTVShows[:3]
	}

	rec.TVShows = unwatchedTVShows
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
