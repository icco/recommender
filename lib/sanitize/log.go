package sanitize

import (
	"context"
	"time"

	"github.com/icco/gutil/logging"
)

// LogRecommendationCronStart logs the start of the recommendation cron handler.
func LogRecommendationCronStart(ctx context.Context, startTime time.Time, remoteAddr, lockKey string) {
	logging.FromContext(ctx).Infow("Starting recommendation cron job",
		"start_time", startTime,
		"remote_addr", ForLog(remoteAddr),
		"lock_key", ForLog(lockKey),
	)
}

// LogCacheUpdateJobStart logs the start of the Plex cache update cron handler.
func LogCacheUpdateJobStart(ctx context.Context, startTime time.Time, remoteAddr, lockKey string) {
	logging.FromContext(ctx).Infow("Starting cache update job",
		"start_time", startTime,
		"remote_addr", ForLog(remoteAddr),
		"lock_key", ForLog(lockKey),
	)
}
