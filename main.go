// Package main implements a content recommendation service that integrates with Plex and TMDb.
// It provides a web interface for viewing recommendations and handles background tasks
// for generating new recommendations and updating content metadata.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/icco/recommender/handlers"
	"github.com/icco/recommender/lib/db"
	"github.com/icco/recommender/lib/health"
	"github.com/icco/recommender/lib/lock"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/tmdb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// JSONLogger is a custom middleware that logs HTTP requests in JSON format.
// It captures request details including method, path, status code, and duration.
func JSONLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer that captures the status code
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		// Process request
		next.ServeHTTP(ww, r)

		// Log the request details
		slog.Info("HTTP Request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
			slog.String("user_agent", r.UserAgent()),
			slog.Int("status", ww.Status()),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// main is the entry point of the application.
// It sets up the environment, initializes clients and services, and starts the HTTP server.
func main() {
	// Set up logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
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

	tmdbAPIKey := os.Getenv("TMDB_API_KEY")
	if tmdbAPIKey == "" {
		slog.Error("TMDB_API_KEY environment variable is required")
		os.Exit(1)
	}

	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		slog.Error("OPENAI_API_KEY environment variable is required")
		os.Exit(1)
	}

	// Set up database with custom JSON logger
	gormDB, err := gorm.Open(sqlite.Open("recommender.db"), &gorm.Config{
		Logger: db.NewGormLogger(slog.Default()),
	})
	if err != nil {
		slog.Error("Failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}

	if err := db.RunMigrations(gormDB, slog.Default()); err != nil {
		slog.Error("Failed to run migrations", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up distributed locking
	var etcdEndpoints []string
	if etcdEndpointsStr := os.Getenv("ETCD_ENDPOINTS"); etcdEndpointsStr != "" {
		etcdEndpoints = strings.Split(etcdEndpointsStr, ",")
	}
	
	distributedLock, err := lock.NewDistributedLock(etcdEndpoints, slog.Default())
	if err != nil {
		slog.Error("Failed to create distributed lock", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up TMDb client
	tmdbClient := tmdb.NewClient(tmdbAPIKey, slog.Default())

	// Set up Plex client
	plexClient := plex.NewClient(plexURL, plexToken, slog.Default(), gormDB, tmdbClient)

	// Set up recommender
	recommender, err := recommend.New(gormDB, plexClient, tmdbClient, slog.Default())
	if err != nil {
		slog.Error("Failed to create recommender", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up router
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(JSONLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Static file serving
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

	// Routes
	r.Get("/", handlers.HandleHome(recommender))
	r.Get("/date/{date}", handlers.HandleDate(recommender))
	r.Get("/dates", handlers.HandleDates(recommender))
	r.Get("/cron/recommend", handlers.HandleCron(recommender))
	r.Get("/cron/cache", handlers.HandleCache(plexClient))
	r.Get("/stats", handlers.HandleStats(recommender))
	r.Get("/health", health.Check(gormDB))

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Set up graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("Starting server", slog.String("port", port))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server error", slog.Any("error", err))
			stop()
		}
	}()

	// Wait for interrupt signal
	<-ctx.Done()
	stop()

	// Gracefully shutdown the server with a timeout
	slog.Info("Shutting down server gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server shutdown error", slog.Any("error", err))
	}

	// Close distributed lock
	if err := distributedLock.Close(); err != nil {
		slog.Error("Failed to close distributed lock", slog.Any("error", err))
	}

	slog.Info("Server stopped")
}
