package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/handlers/templates"
	"github.com/icco/recommender/lib/lock"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend"
	"github.com/icco/recommender/lib/sanitize"
	"github.com/icco/recommender/lib/validation"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// baseTemplate is the filename of the shared base layout template used by
// every page render.
const baseTemplate = "base.html"

type errorData struct {
	Message string
}

// writeError writes an error response in the appropriate format (JSON or HTML)
// based on the request's Accept header or Content-Type preference.
func writeError(w http.ResponseWriter, r *http.Request, message string, status int) {
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"error": message,
		}); err != nil {
			logging.FromContext(r.Context()).Errorw("Failed to encode JSON error response", zap.Error(err))
		}
		return
	}

	renderError(r.Context(), w, message, status)
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
func renderError(ctx context.Context, w http.ResponseWriter, message string, status int) {
	l := logging.FromContext(ctx)
	tmpl, err := templates.ParseTemplates(baseTemplate, "error.html")
	if err != nil {
		l.Errorw("Failed to parse error template", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, baseTemplate, errorData{Message: message}); err != nil {
		l.Errorw("Failed to execute error template", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// renderTemplate renders a template with the given data and handles errors.
// Returns true if rendering was successful, false otherwise.
func renderTemplate(ctx context.Context, w http.ResponseWriter, files []string, data interface{}) bool {
	l := logging.FromContext(ctx)
	tmpl, err := templates.ParseTemplates(files...)
	if err != nil {
		l.Errorw("Failed to parse template", zap.Error(err))
		renderError(ctx, w, "Something went wrong while loading the page.", http.StatusInternalServerError)
		return false
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := tmpl.ExecuteTemplate(w, baseTemplate, data); err != nil {
		l.Errorw("Failed to execute template", zap.Error(err))
		if !isResponseStarted(w) {
			renderError(ctx, w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
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

		today := time.Now().UTC().Truncate(24 * time.Hour)

		recommendations, err := r.GetRecommendationsForDate(ctx, today)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeError(w, req, "No recommendations available for today. Please check back later or visit the Past Recommendations page.", http.StatusNotFound)
			} else {
				logging.FromContext(ctx).Errorw("Failed to get today's recommendations", zap.Error(err))
				writeError(w, req, "We couldn't find today's recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		if !renderTemplate(ctx, w, []string{baseTemplate, "home.html"}, recommendations) {
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
		l := logging.FromContext(ctx)

		date := chi.URLParam(req, "date")
		if date == "" {
			l.Errorw("Missing date parameter")
			writeError(w, req, "date parameter is required", http.StatusBadRequest)
			return
		}

		if err := validation.ValidateDate(date); err != nil {
			l.Errorw("Invalid date format", "date", date, zap.Error(err))
			writeError(w, req, err.Error(), http.StatusBadRequest)
			return
		}

		parsedDate, err := time.Parse("2006-01-02", date)
		if err != nil {
			l.Errorw("Failed to parse date", "date", date, zap.Error(err))
			writeError(w, req, fmt.Sprintf("invalid date format: %v", err), http.StatusBadRequest)
			return
		}
		parsedDate = parsedDate.UTC()

		recommendations, err := r.GetRecommendationsForDate(ctx, parsedDate)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				l.Infow("No recommendations found for date", "date", date)
				writeError(w, req, "We couldn't find recommendations for this date.", http.StatusNotFound)
			} else {
				l.Errorw("Database error while fetching recommendations",
					"date", date,
					zap.Error(err))
				writeError(w, req, "We encountered an error while fetching recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		if !renderTemplate(ctx, w, []string{baseTemplate, "home.html"}, recommendations) {
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
			logging.FromContext(ctx).Errorw("Failed to get dates", zap.Error(err))
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

		if !renderTemplate(ctx, w, []string{baseTemplate, "dates.html"}, data) {
			return
		}
	}
}

// cronBackgroundLockKey serializes all heavy cron work (cache refresh and recommendation
// generation) so they never run concurrently. Otherwise a cache rebuild can delete
// movie/tv rows while recommendation generation is reading them.
const cronBackgroundLockKey = "cron-serial"

// HandleCron handles the recommendation generation cron job.
// It takes a recommender instance and file lock, and returns an HTTP handler.
// The job runs asynchronously and generates recommendations for the current day.
func HandleCron(r *recommend.Recommender, fl *lock.FileLock) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		l := logging.FromContext(ctx)
		startTime := time.Now()
		today := time.Now().UTC().Truncate(24 * time.Hour)
		lockKey := cronBackgroundLockKey

		sanitize.LogRecommendationCronStart(ctx, startTime, req.RemoteAddr, lockKey)

		acquired, err := fl.TryLock(ctx, lockKey, 10*time.Second)
		if err != nil {
			l.Errorw("Failed to acquire lock for cron job",
				"lock_key", lockKey,
				zap.Error(err),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to acquire lock", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if !acquired {
			l.Infow("Cron job already in progress (cache or recommendations); try again later",
				"lock_key", lockKey,
				"date", today,
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Another cron job is already running (cache or recommendations); try again later", "timestamp": "%s"}`,
				time.Now().Format(time.RFC3339)); err != nil {
				l.Errorw("Failed to write response", zap.Error(err))
			}
			return
		}

		exists, err := r.CheckRecommendationsExist(ctx, today)
		if err != nil {
			if unlockErr := fl.Unlock(ctx, lockKey); unlockErr != nil {
				l.Errorw("Failed to unlock after error", zap.Error(unlockErr))
			}
			l.Errorw("Failed to check existing recommendations",
				"date", today,
				zap.Error(err),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to check existing recommendations", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if exists {
			if unlockErr := fl.Unlock(ctx, lockKey); unlockErr != nil {
				l.Errorw("Failed to unlock after exists check", zap.Error(unlockErr))
			}
			l.Infow("Recommendations already exist for today (double-check within lock)",
				"date", today,
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Recommendations already exist for %s", "timestamp": "%s"}`,
				today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
				l.Errorw("Failed to write response", zap.Error(err))
			}
			return
		}

		// Background work needs its own context independent of the request, but with the
		// same logger so cron logs remain correlated.
		genCtx, genCancel := context.WithTimeout(logging.NewContext(context.Background(), l), 5*time.Minute)
		l.Infow("Dispatching recommendation generation to background",
			"date", today,
			"lock_key", lockKey,
		)
		go func() {
			defer func() {
				genCancel()
				if err := fl.Unlock(context.Background(), lockKey); err != nil {
					l.Errorw("Failed to release lock after recommendation generation",
						"lock_key", lockKey,
						zap.Error(err),
					)
				}
			}()
			l.Infow("Starting recommendation generation in background",
				"date", today,
				"timeout", 5*time.Minute,
				"lock_key", lockKey,
			)
			if err := r.GenerateRecommendations(genCtx, today); err != nil {
				l.Errorw("Failed to generate recommendations",
					"date", today,
					zap.Error(err),
				)
			} else {
				l.Infow("Recommendation generation completed successfully",
					"date", today,
					"duration", time.Since(startTime),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Recommendation generation started for %s", "timestamp": "%s"}`,
			today.Format("2006-01-02"), time.Now().Format(time.RFC3339)); err != nil {
			l.Errorw("Failed to write response", zap.Error(err))
		}
	}
}

// HandleCache handles the Plex cache update cron job.
// It takes a Plex client instance and file lock, and returns an HTTP handler.
// The job runs asynchronously and updates the cache of available media.
func HandleCache(p *plex.Client, fl *lock.FileLock) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		l := logging.FromContext(ctx)
		startTime := time.Now()
		lockKey := cronBackgroundLockKey

		sanitize.LogCacheUpdateJobStart(ctx, startTime, req.RemoteAddr, lockKey)

		acquired, err := fl.TryLock(ctx, lockKey, 10*time.Second)
		if err != nil {
			l.Errorw("Failed to acquire lock for cache update",
				"lock_key", lockKey,
				zap.Error(err),
			)
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error": "Failed to acquire lock", "timestamp": "`+time.Now().Format(time.RFC3339)+`"}`, http.StatusInternalServerError)
			return
		}

		if !acquired {
			l.Infow("Cron job already in progress (cache or recommendations); try again later",
				"lock_key", lockKey,
			)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintf(w, `{"message": "Another cron job is already running (cache or recommendations); try again later", "timestamp": "%s"}`,
				time.Now().Format(time.RFC3339)); err != nil {
				l.Errorw("Failed to write response", zap.Error(err))
			}
			return
		}

		bgCtx, cancel := context.WithTimeout(logging.NewContext(context.Background(), l), 5*time.Minute)
		l.Infow("Dispatching Plex cache update to background",
			"lock_key", lockKey,
		)
		go func() {
			defer func() {
				cancel()
				if err := fl.Unlock(context.Background(), lockKey); err != nil {
					l.Errorw("Failed to release lock after cache update",
						"lock_key", lockKey,
						zap.Error(err),
					)
				}
			}()
			l.Infow("Starting cache update in background",
				"timeout", 5*time.Minute,
				"lock_key", lockKey,
			)
			if err := p.UpdateCache(bgCtx); err != nil {
				l.Errorw("Failed to update cache", zap.Error(err))
			} else {
				l.Infow("Cache update completed successfully",
					"duration", time.Since(startTime),
				)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message": "Cache update started", "timestamp": "%s"}`,
			time.Now().Format(time.RFC3339)); err != nil {
			l.Errorw("Failed to write response", zap.Error(err))
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
			logging.FromContext(ctx).Errorw("Failed to get stats", zap.Error(err))
			writeError(w, req, "We couldn't load the statistics. Please try again later.", http.StatusInternalServerError)
			return
		}

		if !renderTemplate(ctx, w, []string{baseTemplate, "stats.html"}, stats) {
			return
		}
	}
}
