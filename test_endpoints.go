package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/icco/recommender/handlers"
	"github.com/icco/recommender/lib/db"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	// Set up logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	logger := slog.Default()
	logger.Info("Starting endpoint testing")

	// Check required environment variables
	plexURL := os.Getenv("PLEX_URL")
	if plexURL == "" {
		logger.Error("PLEX_URL environment variable is required")
		os.Exit(1)
	}

	plexToken := os.Getenv("PLEX_TOKEN")
	if plexToken == "" {
		logger.Error("PLEX_TOKEN environment variable is required")
		os.Exit(1)
	}

	tmdbAPIKey := os.Getenv("TMDB_API_KEY")
	if tmdbAPIKey == "" {
		logger.Error("TMDB_API_KEY environment variable is required")
		os.Exit(1)
	}

	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		logger.Error("OPENAI_API_KEY environment variable is required")
		os.Exit(1)
	}

	// Get database path from environment or use default
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "recommender.db"
	}

	// Connect to database
	logger.Info("Connecting to database", slog.String("path", dbPath))
	gormDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: db.NewGormLogger(logger),
	})
	if err != nil {
		logger.Error("Failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}

	// Run migrations
	if err := db.RunMigrations(gormDB, logger); err != nil {
		logger.Error("Failed to run migrations", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up clients
	tmdbClient := tmdb.NewClient(tmdbAPIKey, logger)
	plexClient := plex.NewClient(plexURL, plexToken, logger, gormDB, tmdbClient)
	recommender, err := recommend.New(gormDB, plexClient, tmdbClient, logger)
	if err != nil {
		logger.Error("Failed to create recommender", slog.Any("error", err))
		os.Exit(1)
	}

	// Set up router
	r := chi.NewRouter()
	r.Get("/", handlers.HandleHome(recommender))
	r.Get("/date/{date}", handlers.HandleDate(recommender))
	r.Get("/dates", handlers.HandleDates(recommender))
	r.Get("/stats", handlers.HandleStats(recommender))

	// Test different scenarios
	testEndpoints(r, gormDB, logger)
}

func testEndpoints(r *chi.Mux, db *gorm.DB, logger *slog.Logger) {
	logger.Info("=== TESTING ENDPOINTS ===")

	// Test 1: Home page with no recommendations
	logger.Info("Test 1: Home page with no recommendations")
	testHomePage(r, logger, "should show no recommendations message")

	// Test 2: Create some test recommendations
	logger.Info("Test 2: Creating test recommendations")
	today := time.Now().Truncate(24 * time.Hour)
	if err := createTestRecommendations(db, today, logger); err != nil {
		logger.Error("Failed to create test recommendations", slog.Any("error", err))
		return
	}

	// Test 3: Home page with recommendations
	logger.Info("Test 3: Home page with recommendations")
	testHomePage(r, logger, "should show recommendations")

	// Test 4: Specific date page
	logger.Info("Test 4: Specific date page")
	testDatePage(r, today, logger)

	// Test 5: Dates listing page
	logger.Info("Test 5: Dates listing page")
	testDatesPage(r, logger)

	// Test 6: Stats page
	logger.Info("Test 6: Stats page")
	testStatsPage(r, logger)

	// Test 7: Invalid date
	logger.Info("Test 7: Invalid date")
	testInvalidDate(r, logger)

	// Test 8: Non-existent date
	logger.Info("Test 8: Non-existent date")
	futureDate := time.Now().AddDate(0, 0, 30)
	testDatePage(r, futureDate, logger)

	logger.Info("=== ENDPOINT TESTING COMPLETED ===")
}

func testHomePage(r *chi.Mux, logger *slog.Logger, expected string) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	
	r.ServeHTTP(w, req)
	
	logger.Info("Home page response",
		slog.Int("status", w.Code),
		slog.String("content_type", w.Header().Get("Content-Type")),
		slog.Int("body_length", len(w.Body.String())),
		slog.String("expected", expected))
	
	body := w.Body.String()
	
	// Check for common issues
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		logger.Error("Unexpected status code", slog.Int("status", w.Code))
	}
	
	if strings.Contains(body, "No Recommendations Available") {
		logger.Info("Home page correctly shows no recommendations message")
	} else if strings.Contains(body, "Recommendations for") {
		logger.Info("Home page correctly shows recommendations")
	} else {
		logger.Warn("Home page content unclear", slog.String("body_preview", body[:min(200, len(body))]))
	}
	
	// Check for template errors
	if strings.Contains(body, "template:") || strings.Contains(body, "error executing template") {
		logger.Error("Template error detected", slog.String("body_preview", body[:min(500, len(body))]))
	}
}

func testDatePage(r *chi.Mux, date time.Time, logger *slog.Logger) {
	dateStr := date.Format("2006-01-02")
	req := httptest.NewRequest("GET", "/date/"+dateStr, nil)
	w := httptest.NewRecorder()
	
	r.ServeHTTP(w, req)
	
	logger.Info("Date page response",
		slog.String("date", dateStr),
		slog.Int("status", w.Code),
		slog.String("content_type", w.Header().Get("Content-Type")),
		slog.Int("body_length", len(w.Body.String())))
	
	body := w.Body.String()
	
	if w.Code == http.StatusOK {
		logger.Info("Date page loaded successfully")
	} else if w.Code == http.StatusNotFound {
		logger.Info("Date page correctly shows not found for date with no recommendations")
	} else {
		logger.Warn("Unexpected status code for date page", slog.Int("status", w.Code))
	}
	
	if strings.Contains(body, "template:") || strings.Contains(body, "error executing template") {
		logger.Error("Template error in date page", slog.String("body_preview", body[:min(500, len(body))]))
	}
}

func testDatesPage(r *chi.Mux, logger *slog.Logger) {
	req := httptest.NewRequest("GET", "/dates", nil)
	w := httptest.NewRecorder()
	
	r.ServeHTTP(w, req)
	
	logger.Info("Dates page response",
		slog.Int("status", w.Code),
		slog.String("content_type", w.Header().Get("Content-Type")),
		slog.Int("body_length", len(w.Body.String())))
	
	body := w.Body.String()
	
	if w.Code == http.StatusOK {
		logger.Info("Dates page loaded successfully")
	} else {
		logger.Error("Dates page failed to load", slog.Int("status", w.Code))
	}
	
	if strings.Contains(body, "template:") || strings.Contains(body, "error executing template") {
		logger.Error("Template error in dates page", slog.String("body_preview", body[:min(500, len(body))]))
	}
}

func testStatsPage(r *chi.Mux, logger *slog.Logger) {
	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()
	
	r.ServeHTTP(w, req)
	
	logger.Info("Stats page response",
		slog.Int("status", w.Code),
		slog.String("content_type", w.Header().Get("Content-Type")),
		slog.Int("body_length", len(w.Body.String())))
	
	body := w.Body.String()
	
	if w.Code == http.StatusOK {
		logger.Info("Stats page loaded successfully")
	} else {
		logger.Error("Stats page failed to load", slog.Int("status", w.Code))
	}
	
	if strings.Contains(body, "template:") || strings.Contains(body, "error executing template") {
		logger.Error("Template error in stats page", slog.String("body_preview", body[:min(500, len(body))]))
	}
}

func testInvalidDate(r *chi.Mux, logger *slog.Logger) {
	req := httptest.NewRequest("GET", "/date/invalid-date", nil)
	w := httptest.NewRecorder()
	
	r.ServeHTTP(w, req)
	
	logger.Info("Invalid date response",
		slog.Int("status", w.Code),
		slog.String("content_type", w.Header().Get("Content-Type")))
	
	if w.Code == http.StatusBadRequest {
		logger.Info("Invalid date correctly returns 400 Bad Request")
	} else {
		logger.Warn("Invalid date returned unexpected status", slog.Int("status", w.Code))
	}
}

func createTestRecommendations(db *gorm.DB, date time.Time, logger *slog.Logger) error {
	ctx := context.Background()
	
	// Create test recommendations
	recommendations := []models.Recommendation{
		{
			Date:      date,
			Title:     "Test Movie 1",
			Type:      "movie",
			Year:      2023,
			Rating:    8.5,
			Genre:     "Action",
			PosterURL: "https://example.com/poster1.jpg",
			Runtime:   120,
			TMDbID:    12345,
		},
		{
			Date:      date,
			Title:     "Test Movie 2",
			Type:      "movie",
			Year:      2022,
			Rating:    7.8,
			Genre:     "Comedy",
			PosterURL: "https://example.com/poster2.jpg",
			Runtime:   95,
			TMDbID:    12346,
		},
		{
			Date:      date,
			Title:     "Test TV Show 1",
			Type:      "tvshow",
			Year:      2021,
			Rating:    9.0,
			Genre:     "Drama",
			PosterURL: "https://example.com/poster3.jpg",
			Runtime:   3, // seasons
			TMDbID:    12347,
		},
		{
			Date:      date,
			Title:     "Test TV Show 2",
			Type:      "tvshow",
			Year:      2020,
			Rating:    8.2,
			Genre:     "Sci-Fi",
			PosterURL: "https://example.com/poster4.jpg",
			Runtime:   2, // seasons
			TMDbID:    12348,
		},
	}
	
	// Check if recommendations already exist for this date
	var existingCount int64
	if err := db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ?", date).
		Count(&existingCount).Error; err != nil {
		return fmt.Errorf("failed to check existing recommendations: %w", err)
	}
	
	if existingCount > 0 {
		logger.Info("Test recommendations already exist for date", 
			slog.Time("date", date),
			slog.Int64("count", existingCount))
		return nil
	}
	
	// Create recommendations in transaction
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, rec := range recommendations {
			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to create recommendation %s: %w", rec.Title, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to create test recommendations: %w", err)
	}
	
	logger.Info("Created test recommendations", 
		slog.Time("date", date),
		slog.Int("count", len(recommendations)))
	
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}