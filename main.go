// Package main implements a content recommendation service that integrates with Plex and TMDb.
// It provides a web interface for viewing recommendations and handles background tasks
// for generating new recommendations and updating content metadata.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/handlers"
	"github.com/icco/recommender/lib/db"
	"github.com/icco/recommender/lib/health"
	"github.com/icco/recommender/lib/lock"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/static"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const service = "recommender"

var log = logging.Must(logging.NewLogger(service))

// main is the entry point of the application.
// It sets up the environment, initializes clients and services, and starts the HTTP server.
func main() {
	ctx, stop := signal.NotifyContext(
		logging.NewContext(context.Background(), log),
		os.Interrupt, syscall.SIGTERM,
	)
	defer stop()

	plexURL := os.Getenv("PLEX_URL")
	if plexURL == "" {
		log.Fatalw("PLEX_URL environment variable is required")
	}

	plexToken := os.Getenv("PLEX_TOKEN")
	if plexToken == "" {
		log.Fatalw("PLEX_TOKEN environment variable is required")
	}

	tmdbAPIKey := os.Getenv("TMDB_API_KEY")
	if tmdbAPIKey == "" {
		log.Fatalw("TMDB_API_KEY environment variable is required")
	}

	if os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatalw("OPENAI_API_KEY environment variable is required")
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "recommender.db"
	}

	gormDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: db.NewGormLogger(log.Desugar()),
	})
	if err != nil {
		log.Fatalw("Failed to connect to database", zap.Error(err))
	}

	if err := db.RunMigrations(ctx, gormDB); err != nil {
		log.Fatalw("Failed to run migrations", zap.Error(err))
	}

	fileLock := lock.NewFileLock(ctx)

	tmdbClient := tmdb.NewClient(tmdbAPIKey)

	plexClient := plex.NewClient(plexURL, plexToken, gormDB, tmdbClient)

	recommender, err := recommend.New(gormDB, plexClient, tmdbClient)
	if err != nil {
		log.Fatalw("Failed to create recommender", zap.Error(err))
	}

	r := chi.NewRouter()

	r.Use(logging.Middleware(log.Desugar()))
	r.Use(middleware.Timeout(60 * time.Second))

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))))

	r.Get("/", handlers.HandleHome(recommender))
	r.Get("/date/{date}", handlers.HandleDate(recommender))
	r.Get("/dates", handlers.HandleDates(recommender))
	r.Get("/cron/recommend", handlers.HandleCron(recommender, fileLock))
	r.Get("/cron/cache", handlers.HandleCache(plexClient, fileLock))
	r.Get("/stats", handlers.HandleStats(recommender))
	r.Get("/health", health.Check(gormDB))

	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8080"
	}
	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalw("PORT must be a valid integer", zap.Error(err))
	}
	if portNum < 1 || portNum > 65535 {
		log.Fatalw("PORT must be between 1 and 65535", "port", portNum)
	}

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", portNum),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Infow("Starting server", "port", portNum)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorw("Server error", zap.Error(err))
			stop()
		}
	}()

	<-ctx.Done()
	stop()

	log.Infow("Shutting down server gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Errorw("Server shutdown error", zap.Error(err))
	}

	if err := fileLock.Close(); err != nil {
		log.Errorw("Failed to close file lock", zap.Error(err))
	}

	log.Infow("Server stopped")
}
