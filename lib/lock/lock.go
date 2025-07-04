package lock

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// FileLock provides a simple file-based locking mechanism
type FileLock struct {
	logger *slog.Logger
}

// NewFileLock creates a new file-based lock instance
func NewFileLock(logger *slog.Logger) *FileLock {
	logger.Info("Using local file-based locking")
	return &FileLock{
		logger: logger,
	}
}

// TryLock attempts to acquire a lock with the given key and timeout
func (fl *FileLock) TryLock(ctx context.Context, key string, timeout time.Duration) (bool, error) {
	lockFile := fl.getLockFilePath(key)
	
	// Ensure the lock directory exists
	if err := os.MkdirAll(filepath.Dir(lockFile), 0755); err != nil {
		return false, fmt.Errorf("failed to create lock directory: %w", err)
	}
	
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		// Try to create the lock file exclusively
		file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			if os.IsExist(err) {
				// Check if the existing lock is stale
				if fl.isLockStale(lockFile, timeout*2) {
					fl.logger.Warn("Removing stale lock file", slog.String("file", lockFile))
					os.Remove(lockFile)
					continue
				}
				
				// Lock exists and is not stale, wait and retry
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}
			return false, fmt.Errorf("failed to create lock file: %w", err)
		}
		
		// Write current timestamp and process info to the lock file
		fmt.Fprintf(file, "%d\n%d\n", time.Now().Unix(), os.Getpid())
		file.Close()
		
		fl.logger.Debug("Acquired lock", slog.String("key", key), slog.String("file", lockFile))
		return true, nil
	}
	
	return false, nil // Timeout exceeded
}

// Unlock releases the lock for the given key
func (fl *FileLock) Unlock(ctx context.Context, key string) error {
	lockFile := fl.getLockFilePath(key)
	
	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}
	
	fl.logger.Debug("Released lock", slog.String("key", key), slog.String("file", lockFile))
	return nil
}

// Close cleans up any resources (no-op for file locks)
func (fl *FileLock) Close() error {
	return nil
}

// getLockFilePath returns the file path for a lock key
func (fl *FileLock) getLockFilePath(key string) string {
	// Use a temporary directory for lock files
	lockDir := filepath.Join(os.TempDir(), "recommender-locks")
	return filepath.Join(lockDir, key+".lock")
}

// isLockStale checks if a lock file is older than the given duration
func (fl *FileLock) isLockStale(lockFile string, staleDuration time.Duration) bool {
	info, err := os.Stat(lockFile)
	if err != nil {
		return true // If we can't stat it, consider it stale
	}
	
	return time.Since(info.ModTime()) > staleDuration
}