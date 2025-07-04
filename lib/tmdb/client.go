package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"log/slog"
)

// Client represents a TMDb API client that handles communication with The Movie Database API.
// It provides methods for searching movies and TV shows and retrieving their metadata.
// Includes rate limiting, retry logic, timeouts, and circuit breaker pattern.
type Client struct {
	apiKey       string
	baseURL      string
	httpClient   *http.Client
	logger       *slog.Logger
	rateLimiter  *rateLimiter
	circuitBreaker *circuitBreaker
}

// rateLimiter implements a sliding window rate limiter for TMDb API
// TMDb allows 40 requests per 10 seconds
type rateLimiter struct {
	mu          sync.Mutex
	requests    []time.Time
	maxRequests int
	window      time.Duration
}

// circuitBreaker implements the circuit breaker pattern for API resilience
type circuitBreaker struct {
	mu           sync.Mutex
	state        circuitState
	failureCount int
	lastFailure  time.Time
	maxFailures  int
	timeout      time.Duration
}

type circuitState int

const (
	closed circuitState = iota
	open
	halfOpen
)

// APIError represents a structured error from the TMDb API
type APIError struct {
	StatusCode int
	Message    string
	URL        string
	Method     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("TMDb API error: %d %s for %s %s", e.StatusCode, e.Message, e.Method, e.URL)
}

// SearchResult represents the response from a movie search on TMDb.
// It contains a list of movies matching the search criteria.
type SearchResult struct {
	Results []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		ReleaseDate string  `json:"release_date"`
		PosterPath  string  `json:"poster_path"`
		VoteAverage float64 `json:"vote_average"`
	} `json:"results"`
}

// TVSearchResult represents the response from a TV show search on TMDb.
// It contains a list of TV shows matching the search criteria.
type TVSearchResult struct {
	Results []struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		FirstAirDate string  `json:"first_air_date"`
		PosterPath   string  `json:"poster_path"`
		VoteAverage  float64 `json:"vote_average"`
	} `json:"results"`
}

// NewClient creates a new TMDb client with the provided API key and logger.
// It initializes the HTTP client with timeouts and sets up rate limiting and circuit breaker.
func NewClient(apiKey string, logger *slog.Logger) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: "https://api.themoviedb.org/3",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
			},
		},
		logger: logger,
		rateLimiter: &rateLimiter{
			maxRequests: 40,
			window:      10 * time.Second,
		},
		circuitBreaker: &circuitBreaker{
			maxFailures: 5,
			timeout:     60 * time.Second,
		},
	}
}

// newRateLimiter creates a new rate limiter instance
func newRateLimiter(maxRequests int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		maxRequests: maxRequests,
		window:      window,
	}
}

// allow checks if a request can be made based on the rate limit
func (rl *rateLimiter) allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	// Remove requests outside the window
	for len(rl.requests) > 0 && now.Sub(rl.requests[0]) > rl.window {
		rl.requests = rl.requests[1:]
	}

	// Check if we can make a request
	if len(rl.requests) < rl.maxRequests {
		rl.requests = append(rl.requests, now)
		return true
	}

	return false
}

// wait blocks until a request can be made
func (rl *rateLimiter) wait(ctx context.Context) error {
	for !rl.allow() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// Continue checking
		}
	}
	return nil
}

// canExecute checks if the circuit breaker allows the request
func (cb *circuitBreaker) canExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case closed:
		return true
	case open:
		if time.Since(cb.lastFailure) > cb.timeout {
			cb.state = halfOpen
			return true
		}
		return false
	case halfOpen:
		return true
	default:
		return false
	}
}

// recordSuccess records a successful request
func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	cb.state = closed
}

// recordFailure records a failed request
func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailure = time.Now()

	if cb.failureCount >= cb.maxFailures {
		cb.state = open
	}
}

// SearchMovie searches for movies on TMDb using the provided title and year.
// It returns a list of matching movies with their metadata.
// Includes rate limiting, retry logic, and circuit breaker pattern.
func (c *Client) SearchMovie(ctx context.Context, title string, year int) (*SearchResult, error) {
	url := fmt.Sprintf("%s/search/movie?api_key=%s&query=%s&year=%d",
		c.baseURL, c.apiKey, strings.ReplaceAll(title, " ", "+"), year)

	retryFunc := func() (*SearchResult, error) {
		// Check circuit breaker
		if !c.circuitBreaker.canExecute() {
			return nil, &APIError{
				StatusCode: http.StatusServiceUnavailable,
				Message:    "Circuit breaker is open",
				URL:        url,
				Method:     "GET",
			}
		}

		// Wait for rate limit
		if err := c.rateLimiter.wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		// Add headers for better API interaction
		req.Header.Set("User-Agent", "recommender/1.0")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.circuitBreaker.recordFailure()
			return nil, &APIError{
				StatusCode: 0,
				Message:    err.Error(),
				URL:        url,
				Method:     "GET",
			}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				c.logger.Error("failed to close response body", "error", err)
			}
		}()

		// Handle different status codes
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			apiErr := &APIError{
				StatusCode: resp.StatusCode,
				Message:    string(body),
				URL:        url,
				Method:     "GET",
			}

			// Parse Retry-After header if present
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
					apiErr.RetryAfter = duration
				}
			}

			// Record failure for circuit breaker
			if resp.StatusCode >= 500 {
				c.circuitBreaker.recordFailure()
			}

			return nil, apiErr
		}

		var result SearchResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			c.circuitBreaker.recordFailure()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		c.circuitBreaker.recordSuccess()
		return &result, nil
	}

	// Simple retry implementation for now
	for attempt := 0; attempt < 3; attempt++ {
		result, err := retryFunc()
		if err == nil {
			return result, nil
		}
		
		// Log the retry
		c.logger.Warn("Retrying TMDb search movie",
			slog.Int("attempt", attempt+1),
			slog.String("error", err.Error()))
		
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	
	// Final attempt
	result, err := retryFunc()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SearchTVShow searches for TV shows on TMDb using the provided title and year.
// It returns a list of matching TV shows with their metadata.
// Includes rate limiting, retry logic, and circuit breaker pattern.
func (c *Client) SearchTVShow(ctx context.Context, title string, year int) (*TVSearchResult, error) {
	url := fmt.Sprintf("%s/search/tv?api_key=%s&query=%s&first_air_date_year=%d",
		c.baseURL, c.apiKey, strings.ReplaceAll(title, " ", "+"), year)

	retryFunc := func() (*TVSearchResult, error) {
		// Check circuit breaker
		if !c.circuitBreaker.canExecute() {
			return nil, &APIError{
				StatusCode: http.StatusServiceUnavailable,
				Message:    "Circuit breaker is open",
				URL:        url,
				Method:     "GET",
			}
		}

		// Wait for rate limit
		if err := c.rateLimiter.wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		// Add headers for better API interaction
		req.Header.Set("User-Agent", "recommender/1.0")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.circuitBreaker.recordFailure()
			return nil, &APIError{
				StatusCode: 0,
				Message:    err.Error(),
				URL:        url,
				Method:     "GET",
			}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				c.logger.Error("failed to close response body", "error", err)
			}
		}()

		// Handle different status codes
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			apiErr := &APIError{
				StatusCode: resp.StatusCode,
				Message:    string(body),
				URL:        url,
				Method:     "GET",
			}

			// Parse Retry-After header if present
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
					apiErr.RetryAfter = duration
				}
			}

			// Record failure for circuit breaker
			if resp.StatusCode >= 500 {
				c.circuitBreaker.recordFailure()
			}

			return nil, apiErr
		}

		var result TVSearchResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			c.circuitBreaker.recordFailure()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		c.circuitBreaker.recordSuccess()
		return &result, nil
	}

	// Simple retry implementation for now
	for attempt := 0; attempt < 3; attempt++ {
		result, err := retryFunc()
		if err == nil {
			return result, nil
		}
		
		// Log the retry
		c.logger.Warn("Retrying TMDb search TV show",
			slog.Int("attempt", attempt+1),
			slog.String("error", err.Error()))
		
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	
	// Final attempt
	result, err := retryFunc()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetPosterURL generates the full URL for a movie or TV show poster using the poster path.
// It returns an empty string if the poster path is empty.
func (c *Client) GetPosterURL(posterPath string) string {
	if posterPath == "" {
		return ""
	}
	return fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", posterPath)
}

