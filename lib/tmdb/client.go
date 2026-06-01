// Package tmdb provides a small client for The Movie Database REST API,
// including rate limiting, retry, and circuit-breaker behavior used by the
// recommender service.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/icco/gutil/logging"
	"go.uber.org/zap"
)

// Client is a TMDb API client with rate limiting, retries, timeouts, and a
// circuit breaker. The api key is attached to outbound requests inside do and
// is never copied into errors or logs.
type Client struct {
	apiKey         string
	baseURL        string
	httpClient     *http.Client
	rateLimiter    *rateLimiter
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

// ErrCircuitOpen lets callers short-circuit retry/log loops when TMDb is known-down.
var ErrCircuitOpen = errors.New("circuit open")

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

// NewClient returns a configured TMDb client. Loggers are taken from the
// per-call ctx via gutil/logging.
func NewClient(apiKey string) *Client {
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

// do builds an http.Request from safeURL (which has no api key) and attaches
// the api key as a query parameter just before sending. The api key never
// leaks into errors or logs because callers only ever see safeURL plus the
// generic transport error.
func (c *Client) do(ctx context.Context, safeURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, safeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	q := req.URL.Query()
	q.Set("api_key", c.apiKey)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", "recommender/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Discard err.Error() because Go's net/http embeds the request URL
		// (which carries the api key) in the error message.
		return nil, errors.New("transport error")
	}
	return resp, nil
}

// SearchMovie searches TMDb for movies by title and year. Includes rate
// limiting, retry, and circuit breaker behavior.
func (c *Client) SearchMovie(ctx context.Context, title string, year int) (*SearchResult, error) {
	l := logging.FromContext(ctx)
	// safeURL never includes the api key so it is safe to embed in errors and logs.
	safeURL := fmt.Sprintf("%s/search/movie?query=%s&year=%d",
		c.baseURL, strings.ReplaceAll(title, " ", "+"), year)

	retryFunc := func() (*SearchResult, error) {
		if !c.circuitBreaker.canExecute() {
			return nil, ErrCircuitOpen
		}

		if err := c.rateLimiter.wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}

		resp, err := c.do(ctx, safeURL)
		if err != nil {
			c.circuitBreaker.recordFailure()
			return nil, &APIError{
				StatusCode: 0,
				Message:    "transport error",
				URL:        safeURL,
				Method:     http.MethodGet,
			}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				l.Errorw("failed to close response body", zap.Error(err))
			}
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			apiErr := &APIError{
				StatusCode: resp.StatusCode,
				Message:    string(body),
				URL:        safeURL,
				Method:     http.MethodGet,
			}

			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
					apiErr.RetryAfter = duration
				}
			}

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

	for attempt := range 3 {
		result, err := retryFunc()
		if err == nil {
			return result, nil
		}

		// When the breaker is open every retry will fail the same way, so
		// fail fast instead of logging warn+sleep+retry 3 times per call.
		if errors.Is(err, ErrCircuitOpen) {
			return nil, err
		}

		l.Warnw("Retrying TMDb search movie",
			"attempt", attempt+1,
			zap.Error(err),
		)

		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}

	result, err := retryFunc()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SearchTVShow searches TMDb for TV shows by title and year. Includes rate
// limiting, retry, and circuit breaker behavior.
func (c *Client) SearchTVShow(ctx context.Context, title string, year int) (*TVSearchResult, error) {
	l := logging.FromContext(ctx)
	// safeURL never includes the api key so it is safe to embed in errors and logs.
	safeURL := fmt.Sprintf("%s/search/tv?query=%s&first_air_date_year=%d",
		c.baseURL, strings.ReplaceAll(title, " ", "+"), year)

	retryFunc := func() (*TVSearchResult, error) {
		if !c.circuitBreaker.canExecute() {
			return nil, ErrCircuitOpen
		}

		if err := c.rateLimiter.wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}

		resp, err := c.do(ctx, safeURL)
		if err != nil {
			c.circuitBreaker.recordFailure()
			return nil, &APIError{
				StatusCode: 0,
				Message:    "transport error",
				URL:        safeURL,
				Method:     http.MethodGet,
			}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				l.Errorw("failed to close response body", zap.Error(err))
			}
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			apiErr := &APIError{
				StatusCode: resp.StatusCode,
				Message:    string(body),
				URL:        safeURL,
				Method:     http.MethodGet,
			}

			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if duration, err := time.ParseDuration(retryAfter + "s"); err == nil {
					apiErr.RetryAfter = duration
				}
			}

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

	for attempt := range 3 {
		result, err := retryFunc()
		if err == nil {
			return result, nil
		}

		if errors.Is(err, ErrCircuitOpen) {
			return nil, err
		}

		l.Warnw("Retrying TMDb search TV show",
			"attempt", attempt+1,
			zap.Error(err),
		)

		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}

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
