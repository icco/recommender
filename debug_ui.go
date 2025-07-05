package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/icco/recommender/lib/db"
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
	logger.Info("Starting UI debug - checking database content")

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

	ctx := context.Background()
	
	// Check overall database contents
	logger.Info("=== DATABASE CONTENT OVERVIEW ===")
	
	// Count movies
	var movieCount int64
	if err := gormDB.WithContext(ctx).Model(&models.Movie{}).Count(&movieCount).Error; err != nil {
		logger.Error("Failed to count movies", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("Movies in database", slog.Int64("count", movieCount))

	// Count TV shows
	var tvShowCount int64
	if err := gormDB.WithContext(ctx).Model(&models.TVShow{}).Count(&tvShowCount).Error; err != nil {
		logger.Error("Failed to count TV shows", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("TV shows in database", slog.Int64("count", tvShowCount))

	// Count recommendations
	var recommendationCount int64
	if err := gormDB.WithContext(ctx).Model(&models.Recommendation{}).Count(&recommendationCount).Error; err != nil {
		logger.Error("Failed to count recommendations", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("Recommendations in database", slog.Int64("count", recommendationCount))

	// Check for today's recommendations specifically
	today := time.Now().Truncate(24 * time.Hour)
	logger.Info("=== TODAY'S RECOMMENDATIONS ===", slog.Time("date", today))

	var todayRecommendations []models.Recommendation
	if err := gormDB.WithContext(ctx).Where("date = ?", today).Find(&todayRecommendations).Error; err != nil {
		logger.Error("Failed to get today's recommendations", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("Today's recommendations found", slog.Int("count", len(todayRecommendations)))

	if len(todayRecommendations) > 0 {
		movieCount := 0
		tvShowCount := 0
		for _, rec := range todayRecommendations {
			if rec.Type == "movie" {
				movieCount++
			} else if rec.Type == "tvshow" {
				tvShowCount++
			}
			
			logger.Info("Today's recommendation",
				slog.String("title", rec.Title),
				slog.String("type", rec.Type),
				slog.Int("year", rec.Year),
				slog.Float64("rating", rec.Rating),
				slog.String("genre", rec.Genre),
				slog.String("poster_url", rec.PosterURL),
				slog.Int("runtime", rec.Runtime),
				slog.Int("tmdb_id", rec.TMDbID))
		}
		
		logger.Info("Today's recommendations summary",
			slog.Int("movies", movieCount),
			slog.Int("tv_shows", tvShowCount))
	} else {
		logger.Info("No recommendations found for today")
	}

	// Check for recommendations from the last 7 days
	logger.Info("=== RECENT RECOMMENDATIONS ===")
	
	var recentRecommendations []models.Recommendation
	sevenDaysAgo := time.Now().AddDate(0, 0, -7).Truncate(24 * time.Hour)
	if err := gormDB.WithContext(ctx).
		Where("date >= ?", sevenDaysAgo).
		Order("date DESC").
		Find(&recentRecommendations).Error; err != nil {
		logger.Error("Failed to get recent recommendations", slog.Any("error", err))
		os.Exit(1)
	}

	if len(recentRecommendations) > 0 {
		logger.Info("Recent recommendations found", slog.Int("count", len(recentRecommendations)))
		
		// Group by date
		dateMap := make(map[string][]models.Recommendation)
		for _, rec := range recentRecommendations {
			dateStr := rec.Date.Format("2006-01-02")
			dateMap[dateStr] = append(dateMap[dateStr], rec)
		}
		
		logger.Info("Recent recommendations by date")
		for dateStr, recs := range dateMap {
			movieCount := 0
			tvShowCount := 0
			for _, rec := range recs {
				if rec.Type == "movie" {
					movieCount++
				} else if rec.Type == "tvshow" {
					tvShowCount++
				}
			}
			logger.Info("Date summary",
				slog.String("date", dateStr),
				slog.Int("total", len(recs)),
				slog.Int("movies", movieCount),
				slog.Int("tv_shows", tvShowCount))
		}
	} else {
		logger.Info("No recent recommendations found")
	}

	// Check for any recommendations at all
	logger.Info("=== ALL RECOMMENDATIONS ===")
	
	var allRecommendations []models.Recommendation
	if err := gormDB.WithContext(ctx).
		Select("date, type, COUNT(*) as count").
		Group("date, type").
		Order("date DESC").
		Find(&allRecommendations).Error; err != nil {
		logger.Error("Failed to get all recommendations summary", slog.Any("error", err))
		os.Exit(1)
	}

	if len(allRecommendations) > 0 {
		logger.Info("All recommendations summary", slog.Int("date_type_combinations", len(allRecommendations)))
		
		// Get distinct dates
		var distinctDates []time.Time
		if err := gormDB.WithContext(ctx).
			Model(&models.Recommendation{}).
			Distinct("date").
			Order("date DESC").
			Pluck("date", &distinctDates).Error; err != nil {
			logger.Error("Failed to get distinct dates", slog.Any("error", err))
		} else {
			logger.Info("Dates with recommendations", slog.Int("count", len(distinctDates)))
			for i, date := range distinctDates {
				if i < 10 { // Show first 10 dates
					logger.Info("Date with recommendations", slog.String("date", date.Format("2006-01-02")))
				}
			}
		}
	} else {
		logger.Info("No recommendations found in database")
	}

	// Check if there are any issues with the data
	logger.Info("=== DATA VALIDATION ===")
	
	// Check for recommendations with empty titles
	var emptyTitleCount int64
	if err := gormDB.WithContext(ctx).Model(&models.Recommendation{}).
		Where("title = '' OR title IS NULL").
		Count(&emptyTitleCount).Error; err != nil {
		logger.Error("Failed to count empty titles", slog.Any("error", err))
	} else {
		logger.Info("Recommendations with empty titles", slog.Int64("count", emptyTitleCount))
	}

	// Check for recommendations with invalid types
	var invalidTypeCount int64
	if err := gormDB.WithContext(ctx).Model(&models.Recommendation{}).
		Where("type NOT IN ('movie', 'tvshow')").
		Count(&invalidTypeCount).Error; err != nil {
		logger.Error("Failed to count invalid types", slog.Any("error", err))
	} else {
		logger.Info("Recommendations with invalid types", slog.Int64("count", invalidTypeCount))
	}

	// Check for recommendations with zero or negative years
	var invalidYearCount int64
	if err := gormDB.WithContext(ctx).Model(&models.Recommendation{}).
		Where("year <= 0").
		Count(&invalidYearCount).Error; err != nil {
		logger.Error("Failed to count invalid years", slog.Any("error", err))
	} else {
		logger.Info("Recommendations with invalid years", slog.Int64("count", invalidYearCount))
	}

	logger.Info("UI debugging completed")
	
	// Provide diagnosis
	logger.Info("=== DIAGNOSIS ===")
	if recommendationCount == 0 {
		logger.Info("ISSUE: No recommendations found in database")
		logger.Info("SOLUTION: Run the cron job to generate recommendations or use the debug_recommend.go script")
	} else if len(todayRecommendations) == 0 {
		logger.Info("ISSUE: No recommendations for today")
		logger.Info("SOLUTION: Run the cron job for today or use the debug_recommend.go script")
	} else {
		logger.Info("SUCCESS: Recommendations exist for today")
		logger.Info("The UI should display recommendations properly")
	}
}