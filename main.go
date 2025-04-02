package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/icco/recommender/handlers"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	// Set up logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	// Get environment variables
	plexURL := os.Getenv("PLEX_URL")
	if plexURL == "" {
		slog.Error("PLEX_URL environment variable is required")
		os.Exit(1)
	}

	plexToken := os.Getenv("PLEX_TOKEN")
	if plexToken == "" {
		slog.Error("PLEX_TOKEN environment variable is required")
		os.Exit(1)
	}

	// Set up database
	db, err := gorm.Open(sqlite.Open("recommender.db"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		slog.Error("Failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}

	// Auto-migrate the schema
	if err := db.AutoMigrate(&models.Recommendation{}); err != nil {
		slog.Error("Failed to migrate database", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up Plex client
	plexClient := plex.NewClient(plexURL, plexToken, slog.Default(), db)

	// Test Plex connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := plexClient.TestConnection(ctx); err != nil {
		slog.Error("Failed to connect to Plex", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up recommender
	recommender, err := recommend.New(db, plexClient, slog.Default())
	if err != nil {
		slog.Error("Failed to create recommender", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up router
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Routes
	r.Get("/", handlers.HandleHome(recommender))
	r.Get("/date/{date}", handlers.HandleDate(recommender))
	r.Get("/dates", handlers.HandleDates(recommender))
	r.Get("/cron", handlers.HandleCron(recommender))
	r.Get("/cache", handlers.HandleCache(plexClient))

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	slog.Info("Starting server", slog.String("port", port))
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), r); err != nil {
		slog.Error("Server error", slog.Any("error", err))
		os.Exit(1)
	}
}
