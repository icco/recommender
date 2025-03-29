package db

import (
	"context"
	"time"

	"log/slog"

	"gorm.io/gorm/logger"
)

// GormLogger implements the gorm.logger.Interface to provide structured logging
// for GORM database operations using slog.
type GormLogger struct {
	logger *slog.Logger
}

// NewGormLogger creates a new GORM logger that uses slog for structured logging.
// It takes a slog logger instance and returns a new GormLogger.
func NewGormLogger(logger *slog.Logger) *GormLogger {
	return &GormLogger{logger: logger}
}

// LogMode implements the gorm.logger.Interface method to set the log level.
// It returns the logger instance itself since slog handles log levels differently.
func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

// Info implements the gorm.logger.Interface method to log informational messages.
// It takes a context and message, along with optional data fields.
func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Info(msg, slog.Any("data", data))
}

// Warn implements the gorm.logger.Interface method to log warning messages.
// It takes a context and message, along with optional data fields.
func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Warn(msg, slog.Any("data", data))
}

// Error implements the gorm.logger.Interface method to log error messages.
// It takes a context and message, along with optional data fields.
func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Error(msg, slog.Any("data", data))
}

// Trace implements the gorm.logger.Interface method to log SQL queries and their execution.
// It takes a context, begin time, and a function that returns the SQL query and affected rows.
// It logs the query details, including execution time and any errors.
func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	elapsed := time.Since(begin)
	sql, rows := fc()

	if err != nil {
		l.logger.Error("GORM error",
			slog.Any("error", err),
			slog.String("sql", sql),
			slog.Int64("rows", rows),
			slog.Duration("elapsed", elapsed))
		return
	}

	l.logger.Debug("GORM query",
		slog.String("sql", sql),
		slog.Int64("rows", rows),
		slog.Duration("elapsed", elapsed))
}
