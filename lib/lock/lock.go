// Package lock provides file-based locking primitives for serializing
// background work (e.g. cron-style jobs) across replicas of the service.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/icco/gutil/logging"
	"go.uber.org/zap"
)

// FileLock provides a simple file-based locking mechanism.
type FileLock struct{}

// NewFileLock creates a new file-based lock instance and emits a startup log
// using the logger attached to the provided context.
func NewFileLock(ctx context.Context) *FileLock {
	logging.FromContext(ctx).Infow("Using local file-based locking")
	return &FileLock{}
}

// TryLock attempts to acquire a lock with the given key and timeout.
func (fl *FileLock) TryLock(ctx context.Context, key string, timeout time.Duration) (bool, error) {
	l := logging.FromContext(ctx)
	lockFile := fl.getLockFilePath(key)

	if err := os.MkdirAll(filepath.Dir(lockFile), 0750); err != nil {
		return false, fmt.Errorf("failed to create lock directory: %w", err)
	}

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// #nosec G304 - lockFile is generated through controlled logic in getLockFilePath
		file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			if os.IsExist(err) {
				if fl.isLockStale(lockFile, timeout*2) {
					l.Warnw("Removing stale lock file", "file", lockFile)
					if removeErr := os.Remove(lockFile); removeErr != nil {
						l.Errorw("Failed to remove stale lock file",
							"file", lockFile,
							zap.Error(removeErr),
						)
					}
					continue
				}

				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}
			return false, fmt.Errorf("failed to create lock file: %w", err)
		}

		if _, err := fmt.Fprintf(file, "%d\n%d\n", time.Now().Unix(), os.Getpid()); err != nil {
			l.Errorw("Failed to write to lock file", "file", lockFile, zap.Error(err))
			if closeErr := file.Close(); closeErr != nil {
				l.Errorw("Failed to close lock file after write error",
					"file", lockFile,
					zap.Error(closeErr),
				)
			}
			return false, fmt.Errorf("failed to write to lock file: %w", err)
		}
		if err := file.Close(); err != nil {
			l.Errorw("Failed to close lock file", "file", lockFile, zap.Error(err))
			return false, fmt.Errorf("failed to close lock file: %w", err)
		}

		l.Debugw("Acquired lock", "key", key, "file", lockFile)
		return true, nil
	}

	return false, nil
}

// Unlock releases the lock for the given key.
func (fl *FileLock) Unlock(ctx context.Context, key string) error {
	l := logging.FromContext(ctx)
	lockFile := fl.getLockFilePath(key)

	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	l.Debugw("Released lock", "key", key, "file", lockFile)
	return nil
}

// Close cleans up any resources (no-op for file locks).
func (fl *FileLock) Close() error {
	return nil
}

// getLockFilePath returns the file path for a lock key.
func (fl *FileLock) getLockFilePath(key string) string {
	lockDir := filepath.Join(os.TempDir(), "recommender-locks")
	return filepath.Clean(filepath.Join(lockDir, key+".lock"))
}

// isLockStale checks if a lock file is older than the given duration.
func (fl *FileLock) isLockStale(lockFile string, staleDuration time.Duration) bool {
	info, err := os.Stat(lockFile)
	if err != nil {
		return true
	}

	return time.Since(info.ModTime()) > staleDuration
}
