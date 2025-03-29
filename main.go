package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/icco/recommender/handlers"
	"github.com/icco/recommender/lib/db"
	"github.com/icco/recommender/lib/health"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommender"
	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type App struct {
	db          *gorm.DB
	plex        *plex.Client
	recommender *recommender.Recommender
	router      *chi.Mux
	logger      *slog.Logger
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

	// Create JSON logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger) // Set as default logger

	// Configure GORM with our logger
	gormDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: db.NewGormLogger(logger),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Auto-migrate the schema
	err = gormDB.AutoMigrate(
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

	plexClient := plex.NewClient(plexURL, plexToken, logger)
	recommender, err := recommender.New(gormDB, plexClient, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create recommender: %w", err)
	}

	app := &App{
		db:          gormDB,
		plex:        plexClient,
		recommender: recommender,
		router:      chi.NewRouter(),
		logger:      logger,
	}

	app.setupRoutes()
	return app, nil
}

func (a *App) setupRoutes() {
	// Middleware
	a.router.Use(middleware.RequestID)
	a.router.Use(middleware.RealIP)
	a.router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			duration := time.Since(start)
			a.logger.Info("HTTP request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
				slog.Duration("duration", duration),
			)
		})
	})
	a.router.Use(middleware.Recoverer)
	a.router.Use(middleware.Timeout(60 * time.Second))
	a.router.Use(middleware.Compress(5))

	// Rate limiting
	a.router.Use(middleware.Throttle(100)) // 100 requests per minute

	// Health check
	a.router.Get("/health", health.Check(a.db))

	// Main routes
	a.router.Get("/", handlers.HandleHome(a.db, a.recommender))
	a.router.Get("/date", handlers.HandleDates(a.db, a.recommender))
	a.router.Get("/date/{date}", handlers.HandleDate(a.db, a.recommender))

	// Cron routes
	a.router.Get("/cron/recommend", handlers.HandleCron(a.db, a.recommender))
	a.router.Get("/cron/cache", handlers.HandleCache(a.db, a.plex))
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
