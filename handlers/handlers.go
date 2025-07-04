package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/icco/recommender/handlers/templates"
	"github.com/icco/recommender/lib/lock"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/validation"
	"gorm.io/gorm"
)

type errorData struct {
	Message string
}

// writeError writes an error response in the appropriate format (JSON or HTML)
// based on the request's Accept header or Content-Type preference.
func writeError(w http.ResponseWriter, r *http.Request, message string, status int) {
	// Check if the request prefers JSON
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"error": message,
		}); err != nil {
			slog.Error("Failed to encode JSON error response", slog.Any("error", err))
		}
		return
	}
	
	// Default to HTML error response
	renderError(w, message, status)
}

// wantsJSON checks if the request accepts JSON responses
func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	contentType := r.Header.Get("Content-Type")
	
	// Check Accept header
	if strings.Contains(accept, "application/json") {
		return true
	}
	
	// Check Content-Type header
	if strings.Contains(contentType, "application/json") {
		return true
	}
	
	// Check for AJAX requests
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	
	return false
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
	if err := tmpl.ExecuteTemplate(w, "base.html", errorData{Message: message}); err != nil {
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

	// Set content type for HTML responses
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Execute the base template, which will include the content template
	if err := tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
		slog.ErrorContext(ctx, "Failed to execute template", slog.Any("error", err))
		// Only attempt to write error if headers haven't been sent yet
		// Check if response has already been started by looking for a written status
		if !isResponseStarted(w) {
			renderError(w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
		}
		return false
	}

	return true
}

// isResponseStarted checks if the response has already been started (headers sent)
func isResponseStarted(w http.ResponseWriter) bool {
	// Try to set a dummy header - if response has started, this will be ignored
	// but won't cause an error. We check by seeing if we can still modify headers.
	beforeLen := len(w.Header())
	w.Header().Set("X-Check-Response-Started", "test")
	afterLen := len(w.Header())
	w.Header().Del("X-Check-Response-Started")
	
	// If header was added and removed successfully, response hasn't started
	return beforeLen == afterLen
}

// HandleHome serves the home page with today's recommendations.
// It takes a database connection and recommender instance, and returns an HTTP handler.
func HandleHome(r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		today := time.Now().Truncate(24 * time.Hour)

		recommendations, err := r.GetRecommendationsForDate(ctx, today)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeError(w, req, "No recommendations available for today. Please check back later or visit the Past Recommendations page.", http.StatusNotFound)
			} else {
				slog.ErrorContext(ctx, "Failed to get today's recommendations", slog.Any("error", err))
				writeError(w, req, "We couldn't find today's recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		if !renderTemplate(w, ctx, []string{"base.html", "home.html"}, recommendations) {
			return
		}
	}
}

// HandleDate serves recommendations for a specific date.
// It takes a database connection and recommender instance, and returns an HTTP handler.
// The date should be provided in the URL path parameter.
func HandleDate(r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		date := chi.URLParam(req, "date")
		if date == "" {
			slog.ErrorContext(ctx, "Missing date parameter")
			writeError(w, req, "date parameter is required", http.StatusBadRequest)
			return
		}

		// Validate date format
		if err := validation.ValidateDate(date); err != nil {
			slog.ErrorContext(ctx, "Invalid date format", slog.String("date", date), slog.Any("error", err))
			writeError(w, req, err.Error(), http.StatusBadRequest)
			return
		}

		parsedDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to parse date", slog.String("date", date), slog.Any("error", err))
			writeError(w, req, fmt.Sprintf("invalid date format: %v", err), http.StatusBadRequest)
			return
		}

		recommendations, err := r.GetRecommendationsForDate(ctx, parsedDate)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				slog.InfoContext(ctx, "No recommendations found for date", slog.String("date", date))
				writeError(w, req, "We couldn't find recommendations for this date.", http.StatusNotFound)
			} else {
				slog.ErrorContext(ctx, "Database error while fetching recommendations",
					slog.String("date", date),
					slog.Any("error", err))
				writeError(w, req, "We encountered an error while fetching recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		if !renderTemplate(w, ctx, []string{"base.html", "home.html"}, recommendations) {
			return
		}
	}
}

// HandleDates serves a paginated list of dates with recommendations.
// It takes a database connection and recommender instance, and returns an HTTP handler.
// Pagination parameters can be provided via query parameters 'page' and 'size'.
func HandleDates(r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		// Get and validate pagination parameters
		page := 1
		pageSize := 20
		if pageStr := req.URL.Query().Get("page"); pageStr != "" {
			if _, err := fmt.Sscanf(pageStr, "%d", &page); err != nil {
				writeError(w, req, "invalid page parameter", http.StatusBadRequest)
				return
			}
		}
		if sizeStr := req.URL.Query().Get("size"); sizeStr != "" {
			if _, err := fmt.Sscanf(sizeStr, "%d", &pageSize); err != nil {
				writeError(w, req, "invalid size parameter", http.StatusBadRequest)
				return
			}
		}

		if err := validation.ValidatePagination(page, pageSize); err != nil {
			writeError(w, req, err.Error(), http.StatusBadRequest)
			return
		}

		dates, total, err := r.GetRecommendationDates(ctx, page, pageSize)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to get dates", slog.Any("error", err))
			writeError(w, req, "We couldn't load the list of dates.", http.StatusInternalServerError)
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

		if !renderTemplate(w, ctx, []string{"base.html", "dates.html"}, data) {
			return
		}
	}
}

// HandleCron handles the recommendation generation cron job.
// It takes a recommender instance and file lock, and returns an HTTP handler.
// The job runs asynchronously and generates recommendations for the current day.
func HandleCron(r *recommend.Recommender, fl *lock.FileLock) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		startTime := time.Now()
		today := time.Now().Truncate(24 * time.Hour)
		lockKey := fmt.Sprintf("cron-recommendations-%s", today.Format("2006-01-02"))
		
		slog.Info("Starting recommendation cron job",
			slog.Time("start_time", startTime),
			slog.String("remote_addr", req.RemoteAddr),
			slog.String("lock_key", lockKey),
		)

		// Try to acquire lock with 10 second timeout
		acquired, err := fl.TryLock(req.Context(), lockKey, 10*time.Second)
		if err != nil {
			slog.ErrorContext(req.Context(), "Failed to acquire lock for cron job",
				slog.Any("error", err),
				slog.String("lock_key", lockKey),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to acquire lock", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if !acquired {
			slog.Info("Recommendation generation already in progress",
				slog.String("lock_key", lockKey),
				slog.Time("date", today),
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Recommendation generation already in progress for %s", "timestamp": "%s"}`,
				today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
				slog.Error("Failed to write response", slog.Any("error", err))
			}
			return
		}

		exists, err := r.CheckRecommendationsExist(req.Context(), today)
		if err != nil {
			if unlockErr := fl.Unlock(req.Context(), lockKey); unlockErr != nil {
				slog.ErrorContext(req.Context(), "Failed to unlock after error", slog.Any("error", unlockErr))
			}
			slog.ErrorContext(req.Context(), "Failed to check existing recommendations",
				slog.Any("error", err),
				slog.Time("date", today),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to check existing recommendations", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if exists {
			if unlockErr := fl.Unlock(req.Context(), lockKey); unlockErr != nil {
				slog.ErrorContext(req.Context(), "Failed to unlock after exists check", slog.Any("error", unlockErr))
			}
			slog.Info("Recommendations already exist for today",
				slog.Time("date", today),
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Recommendations already exist for %s", "timestamp": "%s"}`,
				today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
				slog.Error("Failed to write response", slog.Any("error", err))
			}
			return
		}

		// Create a new background context with a timeout for the recommendation generation
		genCtx, genCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			defer func() {
				genCancel()
				// Always release the lock
				if err := fl.Unlock(context.Background(), lockKey); err != nil {
					slog.Error("Failed to release lock after recommendation generation",
						slog.Any("error", err),
						slog.String("lock_key", lockKey),
					)
				}
			}()
			slog.Info("Starting recommendation generation in background",
				slog.Time("date", today),
				slog.Duration("timeout", 5*time.Minute),
				slog.String("lock_key", lockKey),
			)
			if err := r.GenerateRecommendations(genCtx, today); err != nil {
				slog.ErrorContext(genCtx, "Failed to generate recommendations",
					slog.Any("error", err),
					slog.Time("date", today),
				)
			} else {
				slog.Info("Recommendation generation completed successfully",
					slog.Time("date", today),
					slog.Duration("duration", time.Since(startTime)),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Recommendation generation started for %s", "timestamp": "%s"}`,
			today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
			slog.Error("Failed to write response", slog.Any("error", err))
		}
	}
}

// HandleCache handles the Plex cache update cron job.
// It takes a Plex client instance and file lock, and returns an HTTP handler.
// The job runs asynchronously and updates the cache of available media.
func HandleCache(p *plex.Client, fl *lock.FileLock) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		startTime := time.Now()
		lockKey := "cache-update"
		
		slog.Info("Starting cache update job",
			slog.Time("start_time", startTime),
			slog.String("remote_addr", req.RemoteAddr),
			slog.String("lock_key", lockKey),
		)

		// Try to acquire lock with 10 second timeout
		acquired, err := fl.TryLock(req.Context(), lockKey, 10*time.Second)
		if err != nil {
			slog.ErrorContext(req.Context(), "Failed to acquire lock for cache update",
				slog.Any("error", err),
				slog.String("lock_key", lockKey),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to acquire lock", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if !acquired {
			slog.Info("Cache update already in progress",
				slog.String("lock_key", lockKey),
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Cache update already in progress", "timestamp": "%s"}`,
				time.Now().Format(time.RFC3339)); err != nil {
				slog.Error("Failed to write response", slog.Any("error", err))
			}
			return
		}

		// Create a new background context with a timeout for the cache update
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		go func() {
			defer func() {
				cancel()
				// Always release the lock
				if err := fl.Unlock(context.Background(), lockKey); err != nil {
					slog.Error("Failed to release lock after cache update",
						slog.Any("error", err),
						slog.String("lock_key", lockKey),
					)
				}
			}()
			slog.Info("Starting cache update in background",
				slog.Duration("timeout", 5*time.Minute),
				slog.String("lock_key", lockKey),
			)
			if err := p.UpdateCache(ctx); err != nil {
				slog.ErrorContext(ctx, "Failed to update cache",
					slog.Any("error", err),
				)
			} else {
				slog.Info("Cache update completed successfully",
					slog.Duration("duration", time.Since(startTime)),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Cache update started", "timestamp": "%s"}`,
			time.Now().Format(time.RFC3339)); err != nil {
			slog.Error("Failed to write response", slog.Any("error", err))
		}
	}
}

// HandleStats serves statistics about the recommendations database.
// It takes a recommender instance and returns an HTTP handler.
func HandleStats(r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		stats, err := r.GetStats(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to get stats", slog.Any("error", err))
			writeError(w, req, "We couldn't load the statistics. Please try again later.", http.StatusInternalServerError)
			return
		}

		if !renderTemplate(w, ctx, []string{"base.html", "stats.html"}, stats) {
			return
		}
	}
}
