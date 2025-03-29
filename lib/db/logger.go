package db

import (
	"context"
	"time"

	"log/slog"

	"gorm.io/gorm/logger"
)

// GormLogger implements gorm.logger.Interface
type GormLogger struct {
	logger *slog.Logger
}

func NewGormLogger(logger *slog.Logger) *GormLogger {
	return &GormLogger{logger: logger}
}

func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Info(msg, slog.Any("data", data))
}

func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Warn(msg, slog.Any("data", data))
}

func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	l.logger.Error(msg, slog.Any("data", data))
}

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
