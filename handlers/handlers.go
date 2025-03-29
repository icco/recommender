package handlers

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/icco/recommender/lib/recommender"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

//go:embed templates/*.html
var templateFS embed.FS

func parseTemplates(files ...string) (*template.Template, error) {
	return template.ParseFS(templateFS, files...)
}

func HandleHome(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Get today's date in YYYY-MM-DD format
		today := time.Now().Format("2006-01-02")

		// Try to get today's recommendation
		var rec models.Recommendation
		result := db.Where("date = ?", today).First(&rec)
		if result.Error != nil {
			slog.Error("Failed to get today's recommendation", slog.Any("error", result.Error))
			http.Error(w, "Failed to get today's recommendation", http.StatusInternalServerError)
			return
		}

		// Parse and execute the template
		tmpl, err := parseTemplates("templates/base.html", "templates/home.html")
		if err != nil {
			slog.Error("Failed to parse template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, rec); err != nil {
			slog.Error("Failed to execute template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func HandleDate(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		date := chi.URLParam(req, "date")
		if date == "" {
			http.Error(w, "Date parameter is required", http.StatusBadRequest)
			return
		}

		// Validate date format
		parsedDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			http.Error(w, "Invalid date format. Use YYYY-MM-DD", http.StatusBadRequest)
			return
		}

		// Try to get the recommendation for the specified date
		var rec models.Recommendation
		result := db.Where("date = ?", parsedDate).First(&rec)
		if result.Error != nil {
			slog.Error("Failed to get recommendation", slog.Any("error", result.Error))
			http.Error(w, "Failed to get recommendation", http.StatusInternalServerError)
			return
		}

		// Parse and execute the template
		tmpl, err := parseTemplates("templates/base.html", "templates/home.html")
		if err != nil {
			slog.Error("Failed to parse template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, rec); err != nil {
			slog.Error("Failed to execute template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func HandleDates(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Get all dates with recommendations
		var dates []time.Time
		result := db.Model(&models.Recommendation{}).Pluck("date", &dates)
		if result.Error != nil {
			slog.Error("Failed to get dates", slog.Any("error", result.Error))
			http.Error(w, "Failed to get dates", http.StatusInternalServerError)
			return
		}

		// Parse and execute the template
		tmpl, err := parseTemplates("templates/base.html", "templates/dates.html")
		if err != nil {
			slog.Error("Failed to parse template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, struct{ Dates []time.Time }{Dates: dates}); err != nil {
			slog.Error("Failed to execute template", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func HandleCron(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Get today's date
		today := time.Now().Truncate(24 * time.Hour)

		// Check if we already have a recommendation for today
		var count int64
		db.Model(&models.Recommendation{}).Where("date = ?", today).Count(&count)
		if count > 0 {
			fmt.Fprintf(w, "Recommendation already exists for %s\n", today.Format("2006-01-02"))
			return
		}

		// Start the recommendation generation in the background
		go func() {
			ctx := context.Background()
			rec := &models.Recommendation{Date: today}
			if err := r.GenerateRecommendations(ctx, rec); err != nil {
				slog.Error("Failed to generate recommendation", slog.Any("error", err))
			}
		}()

		fmt.Fprintf(w, "Started generating recommendation for %s\n", today.Format("2006-01-02"))
	}
}
