package sanitize

import (
	"log/slog"
	"time"
)

// LogHTTPRequest logs a completed HTTP request with neutralized string fields.
func LogHTTPRequest(method, path, remoteAddr, userAgent string, status int, duration time.Duration) {
	slog.Info("HTTP Request",
		slog.String("method", ForLog(method)),
		slog.String("path", ForLog(path)),
		slog.String("remote_addr", ForLog(remoteAddr)),
		slog.String("user_agent", ForLog(userAgent)),
		slog.Int("status", status),
		slog.Duration("duration", duration),
	)
}

// LogRecommendationCronStart logs the start of the recommendation cron handler.
func LogRecommendationCronStart(startTime time.Time, remoteAddr, lockKey string) {
	slog.Info("Starting recommendation cron job",
		slog.Time("start_time", startTime),
		slog.String("remote_addr", ForLog(remoteAddr)),
		slog.String("lock_key", ForLog(lockKey)),
	)
}

// LogCacheUpdateJobStart logs the start of the Plex cache update cron handler.
func LogCacheUpdateJobStart(startTime time.Time, remoteAddr, lockKey string) {
	slog.Info("Starting cache update job",
		slog.Time("start_time", startTime),
		slog.String("remote_addr", ForLog(remoteAddr)),
		slog.String("lock_key", ForLog(lockKey)),
	)
}
