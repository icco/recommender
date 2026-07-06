// Package main implements a Plex/TMDb-powered recommendation service with a
// web UI and cron-driven background jobs.
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const service = "recommender"

var log = logging.Must(logging.NewLogger(service))

// routeTag stamps the chi route pattern onto otelhttp metric labels so HTTP
// metrics carry low-cardinality http.route values.
func routeTag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		labeler, ok := otelhttp.LabelerFromContext(r.Context())
		if !ok {
			return
		}
		if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
			labeler.Add(semconv.HTTPRoute(pattern))
		}
	})
}

// main wires dependencies and blocks until SIGINT/SIGTERM.
func main() {
	ctx, stop := signal.NotifyContext(
		logging.NewContext(context.Background(), log),
		os.Interrupt, syscall.SIGTERM,
	)
	defer stop()

	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		log.Fatalw("otel prometheus exporter", zap.Error(err))
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(mp)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mp.Shutdown(shutdownCtx); err != nil {
			log.Warnw("meter provider shutdown", zap.Error(err))
		}
	}()

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

	if os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
		log.Fatalw("GOOGLE_CLOUD_PROJECT environment variable is required")
	}
	if os.Getenv("GOOGLE_CLOUD_LOCATION") == "" {
		log.Fatalw("GOOGLE_CLOUD_LOCATION environment variable is required")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatalw("DATABASE_URL environment variable is required")
	}

	gormDB, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: db.NewGormLogger(log.Desugar()),
	})
	if err != nil {
		log.Fatalw("Failed to connect to database", zap.Error(err))
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		log.Fatalw("Failed to get database handle", zap.Error(err))
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := db.RunMigrations(ctx, gormDB); err != nil {
		log.Fatalw("Failed to run migrations", zap.Error(err))
	}

	fileLock := lock.NewFileLock(ctx)

	tmdbClient := tmdb.NewClient(tmdbAPIKey)

	plexClient := plex.NewClient(plexURL, plexToken, gormDB, tmdbClient)

	geminiModel := os.Getenv("GEMINI_MODEL")
	if geminiModel == "" {
		geminiModel = "gemini-2.5-flash"
	}
	chat, err := recommend.NewGeminiChatter(ctx, geminiModel)
	if err != nil {
		log.Fatalw("Failed to create Gemini client", zap.Error(err))
	}

	sigCfg := recommend.SignalConfig{
		TraktClientID:     os.Getenv("TRAKT_CLIENT_ID"),
		TraktClientSecret: os.Getenv("TRAKT_CLIENT_SECRET"),
		AniListUsername:   os.Getenv("ANILIST_USERNAME"),
	}

	// posterDir holds locally cached Plex posters; POSTER_DIR is operator config.
	posterDir := os.Getenv("POSTER_DIR")
	if posterDir == "" {
		posterDir = "posters"
	}
	if err := os.MkdirAll(posterDir, 0o750); err != nil { //nolint:gosec // posterDir is operator-set config, not user input
		log.Fatalw("Failed to create poster dir", zap.Error(err))
	}

	recommender, err := recommend.New(gormDB, plexClient, tmdbClient, chat, geminiModel, sigCfg, posterDir)
	if err != nil {
		log.Fatalw("Failed to create recommender", zap.Error(err))
	}

	r := chi.NewRouter()

	r.Use(logging.Middleware(log.Desugar()))
	r.Use(routeTag)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))))
	r.Handle("/posters/*", http.StripPrefix("/posters/", http.FileServer(http.Dir(posterDir))))

	r.Get("/", handlers.HandleHome(recommender))
	r.Get("/date/{date}", handlers.HandleDate(recommender))
	r.Get("/dates", handlers.HandleDates(recommender))
	r.Get("/cron/recommend", handlers.HandleCron(recommender, fileLock))
	r.Get("/cron/cache", handlers.HandleCache(plexClient, recommender, fileLock))
	r.Get("/trakt/connect", handlers.HandleTraktConnect(recommender, os.Getenv("TRAKT_CONNECT_TOKEN")))
	r.Get("/stats", handlers.HandleStats(recommender))
	r.Get("/health", health.Check(gormDB))
	r.Method(http.MethodGet, "/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

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

	handler := otelhttp.NewHandler(r, service,
		otelhttp.WithFilter(func(req *http.Request) bool {
			return req.URL.Path != "/metrics"
		}),
	)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", portNum),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
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
