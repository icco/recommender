// Package health exposes a /health HTTP handler that reports liveness and
// database connectivity for the recommender service.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/icco/gutil/logging"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Health represents the health check response structure.
// It includes the overall status, timestamp, and database health information.
type Health struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	DB        struct {
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	} `json:"db"`
}

// Check returns an HTTP handler that performs health checks on the application.
// It verifies the database connection and returns the health status.
// The handler returns a JSON response with the health information.
func Check(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		health := Health{
			Status:    "ok",
			Timestamp: time.Now(),
		}

		sqlDB, err := db.DB()
		if err != nil {
			health.Status = "degraded"
			health.DB.Status = "error"
			health.DB.Message = "Failed to get database connection"
			writeHealth(ctx, w, health, http.StatusServiceUnavailable)
			return
		}

		if err := sqlDB.PingContext(ctx); err != nil {
			health.Status = "degraded"
			health.DB.Status = "error"
			health.DB.Message = "Database ping failed"
			writeHealth(ctx, w, health, http.StatusServiceUnavailable)
			return
		}

		health.DB.Status = "ok"
		writeHealth(ctx, w, health, http.StatusOK)
	}
}

// writeHealth writes the health check response to the HTTP response writer.
func writeHealth(ctx context.Context, w http.ResponseWriter, health Health, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(health); err != nil {
		logging.FromContext(ctx).Errorw("Failed to encode health response", zap.Error(err))
	}
}
