package validation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

var dateRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ValidateDate checks if a date string is in the correct format (YYYY-MM-DD)
func ValidateDate(date string) error {
	if !dateRegex.MatchString(date) {
		return fmt.Errorf("invalid date format: %s, expected YYYY-MM-DD", date)
	}

	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("invalid date: %s", err)
	}

	// Check if date is in the future
	if parsed.After(time.Now()) {
		return fmt.Errorf("date cannot be in the future")
	}

	return nil
}

// ValidatePagination validates pagination parameters
func ValidatePagination(page, size int) error {
	if page < 1 {
		return fmt.Errorf("page must be greater than 0")
	}
	if size < 1 || size > 100 {
		return fmt.Errorf("size must be between 1 and 100")
	}
	return nil
}

// WriteError writes a validation error response
func WriteError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": err.Error(),
	})
}
