package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"log/slog"

	"gorm.io/gorm"
)

type Health struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	DB        struct {
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	} `json:"db"`
}

func Check(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		health := Health{
			Status:    "ok",
			Timestamp: time.Now(),
		}

		// Check database connection
		sqlDB, err := db.DB()
		if err != nil {
			health.Status = "degraded"
			health.DB.Status = "error"
			health.DB.Message = "Failed to get database connection"
			writeHealth(w, health, http.StatusServiceUnavailable)
			return
		}

		if err := sqlDB.PingContext(ctx); err != nil {
			health.Status = "degraded"
			health.DB.Status = "error"
			health.DB.Message = "Database ping failed"
			writeHealth(w, health, http.StatusServiceUnavailable)
			return
		}

		health.DB.Status = "ok"
		writeHealth(w, health, http.StatusOK)
	}
}

func writeHealth(w http.ResponseWriter, health Health, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(health); err != nil {
		slog.Error("Failed to encode health response", slog.Any("error", err))
	}
}
