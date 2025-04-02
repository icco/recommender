package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/icco/recommender/handlers/templates"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/validation"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

type errorData struct {
	Message string
}

// renderError renders an error page using the error template.
// It takes a response writer, error message, and HTTP status code.
func renderError(w http.ResponseWriter, message string, status int) {
	tmpl, err := templates.ParseTemplates("base.html", "error.html")
	if err != nil {
		slog.Error("Failed to parse error template", slog.Any("error", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(status)
	if err := tmpl.Execute(w, errorData{Message: message}); err != nil {
		slog.Error("Failed to execute error template", slog.Any("error", err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// renderTemplate renders a template with the given data and handles errors.
// It takes a response writer, template files, and data to render.
// Returns true if rendering was successful, false otherwise.
func renderTemplate(w http.ResponseWriter, ctx context.Context, files []string, data interface{}) bool {
	tmpl, err := templates.ParseTemplates(files...)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to parse template", slog.Any("error", err))
		renderError(w, "Something went wrong while loading the page.", http.StatusInternalServerError)
		return false
	}

	if err := tmpl.Execute(w, data); err != nil {
		slog.ErrorContext(ctx, "Failed to execute template", slog.Any("error", err))
		renderError(w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
		return false
	}

	return true
}

// HandleHome serves the home page with today's recommendations.
// It takes a database connection and recommender instance, and returns an HTTP handler.
func HandleHome(db *gorm.DB, r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		today := time.Now().Format("2006-01-02")

		var rec models.Recommendation
		result := db.WithContext(ctx).Where("date = ?", today).First(&rec)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				renderError(w, "No recommendations available for today. Please check back later or visit the Past Recommendations page.", http.StatusNotFound)
			} else {
				slog.ErrorContext(ctx, "Failed to get today's recommendation", slog.Any("error", result.Error))
				renderError(w, "We couldn't find today's recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		renderTemplate(w, ctx, []string{"base.html", "home.html"}, rec)
	}
}

// HandleDate serves recommendations for a specific date.
// It takes a database connection and recommender instance, and returns an HTTP handler.
// The date should be provided in the URL path parameter.
func HandleDate(db *gorm.DB, r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		date := chi.URLParam(req, "date")
		if date == "" {
			slog.ErrorContext(ctx, "Missing date parameter")
			validation.WriteError(w, fmt.Errorf("date parameter is required"), http.StatusBadRequest)
			return
		}

		// Validate date format
		if err := validation.ValidateDate(date); err != nil {
			slog.ErrorContext(ctx, "Invalid date format", slog.String("date", date), slog.Any("error", err))
			validation.WriteError(w, err, http.StatusBadRequest)
			return
		}

		parsedDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to parse date", slog.String("date", date), slog.Any("error", err))
			validation.WriteError(w, fmt.Errorf("invalid date format: %w", err), http.StatusBadRequest)
			return
		}

		var rec models.Recommendation
		result := db.WithContext(ctx).Where("date = ?", parsedDate).First(&rec)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				slog.InfoContext(ctx, "No recommendation found for date", slog.String("date", date))
				renderError(w, "We couldn't find recommendations for this date.", http.StatusNotFound)
			} else {
				slog.ErrorContext(ctx, "Database error while fetching recommendation",
					slog.String("date", date),
					slog.Any("error", result.Error))
				renderError(w, "We encountered an error while fetching recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		renderTemplate(w, ctx, []string{"base.html", "home.html"}, rec)
	}
}

// HandleDates serves a paginated list of dates with recommendations.
// It takes a database connection and recommender instance, and returns an HTTP handler.
// Pagination parameters can be provided via query parameters 'page' and 'size'.
func HandleDates(db *gorm.DB, r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		// Get and validate pagination parameters
		page := 1
		pageSize := 20
		if pageStr := req.URL.Query().Get("page"); pageStr != "" {
			if _, err := fmt.Sscanf(pageStr, "%d", &page); err != nil {
				validation.WriteError(w, fmt.Errorf("invalid page parameter"), http.StatusBadRequest)
				return
			}
		}
		if sizeStr := req.URL.Query().Get("size"); sizeStr != "" {
			if _, err := fmt.Sscanf(sizeStr, "%d", &pageSize); err != nil {
				validation.WriteError(w, fmt.Errorf("invalid size parameter"), http.StatusBadRequest)
				return
			}
		}

		if err := validation.ValidatePagination(page, pageSize); err != nil {
			validation.WriteError(w, err, http.StatusBadRequest)
			return
		}

		// Get total count
		var total int64
		if err := db.WithContext(ctx).Model(&models.Recommendation{}).Count(&total).Error; err != nil {
			slog.ErrorContext(ctx, "Failed to get total count", slog.Any("error", err))
			renderError(w, "We couldn't load the list of dates.", http.StatusInternalServerError)
			return
		}

		// Get paginated dates
		var dates []time.Time
		result := db.WithContext(ctx).
			Model(&models.Recommendation{}).
			Order("date DESC").
			Offset((page-1)*pageSize).
			Limit(pageSize).
			Pluck("date", &dates)
		if result.Error != nil {
			slog.ErrorContext(ctx, "Failed to get dates", slog.Any("error", result.Error))
			renderError(w, "We couldn't load the list of dates.", http.StatusInternalServerError)
			return
		}

		data := struct {
			Dates      []time.Time
			Page       int
			PageSize   int
			Total      int64
			TotalPages int
		}{
			Dates:      dates,
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: int((total + int64(pageSize) - 1) / int64(pageSize)),
		}

		renderTemplate(w, ctx, []string{"base.html", "dates.html"}, data)
	}
}

// HandleCron handles the recommendation generation cron job.
// It takes a database connection and recommender instance, and returns an HTTP handler.
// The job runs asynchronously and generates recommendations for the current day.
func HandleCron(db *gorm.DB, r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		startTime := time.Now()
		slog.Info("Starting recommendation cron job",
			slog.Time("start_time", startTime),
			slog.String("remote_addr", req.RemoteAddr),
		)

		today := time.Now().Truncate(24 * time.Hour)

		var count int64
		if err := db.WithContext(req.Context()).Model(&models.Recommendation{}).Where("date = ?", today).Count(&count).Error; err != nil {
			slog.ErrorContext(req.Context(), "Failed to check existing recommendation",
				slog.Any("error", err),
				slog.Time("date", today),
			)
			http.Error(w, `{"error": "Failed to check existing recommendation", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if count > 0 {
			slog.Info("Recommendation already exists for today",
				slog.Time("date", today),
				slog.Int64("count", count),
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Recommendation already exists for %s", "timestamp": "%s"}`,
				today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
				slog.Error("Failed to write response", slog.Any("error", err))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			return
		}

		// Create a new background context with a timeout for the recommendation generation
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			defer cancel()
			slog.Info("Starting recommendation generation in background",
				slog.Time("date", today),
				slog.Duration("timeout", 5*time.Minute),
			)
			rec := &models.Recommendation{Date: today}

			slog.Debug("Initializing recommendation generation",
				slog.Time("date", today),
			)

			if err := r.GenerateRecommendations(ctx, rec); err != nil {
				slog.ErrorContext(ctx, "Failed to generate recommendation",
					slog.Any("error", err),
					slog.Time("date", today),
				)
			} else {
				slog.Info("Successfully generated recommendation",
					slog.Time("date", today),
					slog.Duration("duration", time.Since(startTime)),
				)
				slog.Debug("Recommendation details",
					slog.Int("movies_count", len(rec.Movies)),
					slog.Int("anime_count", len(rec.Anime)),
					slog.Int("tvshows_count", len(rec.TVShows)),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Started generating recommendation for %s", "timestamp": "%s"}`,
			today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
			slog.Error("Failed to write response", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}

// HandleCache handles the Plex cache update cron job.
// It takes a database connection and Plex client instance, and returns an HTTP handler.
// The job runs asynchronously and updates the cache of available media.
func HandleCache(db *gorm.DB, p *plex.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		startTime := time.Now()
		slog.Info("Starting cache update cron job",
			slog.Time("start_time", startTime),
			slog.String("remote_addr", req.RemoteAddr),
		)

		// Create a new background context with a timeout for the cache update
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			defer cancel()
			slog.Info("Starting cache update in background",
				slog.Duration("timeout", 5*time.Minute),
			)

			slog.Debug("Initializing cache update process")

			if err := p.UpdateCache(ctx); err != nil {
				slog.ErrorContext(ctx, "Failed to update cache",
					slog.Any("error", err),
					slog.Duration("duration", time.Since(startTime)),
				)
			} else {
				slog.Info("Successfully updated cache",
					slog.Duration("duration", time.Since(startTime)),
				)

				// Log cache statistics
				var movieCount, tvShowCount int64
				if err := db.WithContext(ctx).Model(&models.PlexMovie{}).Count(&movieCount).Error; err != nil {
					slog.ErrorContext(ctx, "Failed to get movie count", slog.Any("error", err))
				}
				if err := db.WithContext(ctx).Model(&models.PlexTVShow{}).Count(&tvShowCount).Error; err != nil {
					slog.ErrorContext(ctx, "Failed to get TV show count", slog.Any("error", err))
				}

				// Count anime separately
				var animeCount int64
				if err := db.WithContext(ctx).Model(&models.PlexTVShow{}).Where("is_anime = ?", true).Count(&animeCount).Error; err != nil {
					slog.ErrorContext(ctx, "Failed to get anime count", slog.Any("error", err))
				}

				slog.Info("Cache statistics",
					slog.Int64("movies", movieCount),
					slog.Int64("anime", animeCount),
					slog.Int64("tv_shows", tvShowCount),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Started cache update in background", "timestamp": "%s"}`,
			time.Now().Format(time.RFC3339)); err != nil {
			slog.Error("Failed to write response", slog.Any("error", err))
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}
