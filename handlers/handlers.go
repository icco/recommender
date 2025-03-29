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
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommender"
	"github.com/icco/recommender/lib/validation"
	"github.com/icco/recommender/models"
	"gorm.io/gorm"
)

//go:embed templates/*.html
var templateFS embed.FS

func parseTemplates(files ...string) (*template.Template, error) {
	return template.ParseFS(templateFS, files...)
}

type errorData struct {
	Message string
}

func renderError(w http.ResponseWriter, message string, status int) {
	tmpl, err := parseTemplates("templates/base.html", "templates/error.html")
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

// RequireAuth middleware ensures the request is authenticated
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			renderError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// TODO: Implement proper token validation
		next.ServeHTTP(w, r)
	})
}

func HandleHome(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		today := time.Now().Format("2006-01-02")

		var rec models.Recommendation
		result := db.WithContext(ctx).Where("date = ?", today).First(&rec)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				renderError(w, "No recommendations available for today. Please check back later or visit the Past Recommendations page.", http.StatusNotFound)
			} else {
				slog.ErrorContext(ctx, "Failed to get today's recommendation", slog.Any("error", result.Error))
				renderError(w, "We couldn't find today's recommendations. Please try again later.", http.StatusInternalServerError)
			}
			return
		}

		tmpl, err := parseTemplates("templates/base.html", "templates/home.html")
		if err != nil {
			slog.ErrorContext(ctx, "Failed to parse template", slog.Any("error", err))
			renderError(w, "Something went wrong while loading the page.", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, rec); err != nil {
			slog.ErrorContext(ctx, "Failed to execute template", slog.Any("error", err))
			renderError(w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
			return
		}
	}
}

func HandleDate(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		date := chi.URLParam(req, "date")
		if date == "" {
			validation.WriteError(w, fmt.Errorf("date parameter is required"), http.StatusBadRequest)
			return
		}

		// Validate date format
		if err := validation.ValidateDate(date); err != nil {
			validation.WriteError(w, err, http.StatusBadRequest)
			return
		}

		parsedDate, _ := time.Parse("2006-01-02", date)

		var rec models.Recommendation
		result := db.WithContext(ctx).Where("date = ?", parsedDate).First(&rec)
		if result.Error != nil {
			slog.ErrorContext(ctx, "Failed to get recommendation", slog.Any("error", result.Error))
			renderError(w, "We couldn't find recommendations for this date.", http.StatusNotFound)
			return
		}

		tmpl, err := parseTemplates("templates/base.html", "templates/home.html")
		if err != nil {
			slog.ErrorContext(ctx, "Failed to parse template", slog.Any("error", err))
			renderError(w, "Something went wrong while loading the page.", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, rec); err != nil {
			slog.ErrorContext(ctx, "Failed to execute template", slog.Any("error", err))
			renderError(w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
			return
		}
	}
}

func HandleDates(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
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

		tmpl, err := parseTemplates("templates/base.html", "templates/dates.html")
		if err != nil {
			slog.ErrorContext(ctx, "Failed to parse template", slog.Any("error", err))
			renderError(w, "Something went wrong while loading the page.", http.StatusInternalServerError)
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

		if err := tmpl.Execute(w, data); err != nil {
			slog.ErrorContext(ctx, "Failed to execute template", slog.Any("error", err))
			renderError(w, "Something went wrong while displaying the page.", http.StatusInternalServerError)
			return
		}
	}
}

func HandleCron(db *gorm.DB, r *recommender.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer cancel()

		today := time.Now().Truncate(24 * time.Hour)

		var count int64
		if err := db.WithContext(ctx).Model(&models.Recommendation{}).Where("date = ?", today).Count(&count).Error; err != nil {
			slog.ErrorContext(ctx, "Failed to check existing recommendation", slog.Any("error", err))
			renderError(w, "Failed to check existing recommendation.", http.StatusInternalServerError)
			return
		}

		if count > 0 {
			fmt.Fprintf(w, "Recommendation already exists for %s\n", today.Format("2006-01-02"))
			return
		}

		go func() {
			rec := &models.Recommendation{Date: today}
			if err := r.GenerateRecommendations(ctx, rec); err != nil {
				slog.ErrorContext(ctx, "Failed to generate recommendation", slog.Any("error", err))
			}
		}()

		fmt.Fprintf(w, "Started generating recommendation for %s\n", today.Format("2006-01-02"))
	}
}

func HandleCache(db *gorm.DB, p *plex.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer cancel()

		go func() {
			if err := p.UpdateCache(ctx); err != nil {
				slog.ErrorContext(ctx, "Failed to update cache", slog.Any("error", err))
			}
		}()

		fmt.Fprintf(w, "Started updating Plex and Anilist cache\n")
	}
}
