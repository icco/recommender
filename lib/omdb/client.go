// Package omdb provides a small client for the OMDb API, used to fetch
// Metacritic scores (Metascore) for titles owned in Plex. Metacritic has no
// public API of its own; OMDb exposes the Metascore keyed by IMDb ID, which
// matches the GUIDs already cached from Plex.
//
// The client mirrors the resilience shape of lib/tmdb: sliding-window rate
// limiting, a circuit breaker, bounded retries, and a safeURL that keeps the
// api key out of every error and log line.
package omdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/icco/gutil/logging"
	"go.uber.org/zap"
)

// ErrCircuitOpen lets callers short-circuit retry/log loops when OMDb is known-down.
var ErrCircuitOpen = errors.New("circuit open")

// ErrNotFound is returned when OMDb has no record for the requested id.
var ErrNotFound = errors.New("omdb: title not found")

// Client is an OMDb API client. The api key is attached to outbound requests
// inside do and is never copied into errors or logs.
type Client struct {
	apiKey string
	// BaseURL is the OMDb endpoint; exported so tests can point at a stub.
	BaseURL        string
	httpClient     *http.Client
	rateLimiter    *rateLimiter
	circuitBreaker *circuitBreaker
}

// rateLimiter implements a sliding window rate limiter. OMDb's free tier is a
// daily quota rather than a burst limit, so this exists to keep enrichment from
// hammering the service, not to satisfy a documented per-second cap.
type rateLimiter struct {
	mu          sync.Mutex
	requests    []time.Time
	maxRequests int
	window      time.Duration
}

// circuitBreaker implements the circuit breaker pattern for API resilience.
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

// APIError represents a structured error from the OMDb API.
type APIError struct {
	StatusCode int
	Message    string
	URL        string
	Method     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("OMDb API error: %d %s for %s %s", e.StatusCode, e.Message, e.Method, e.URL)
}

// Title is the subset of an OMDb record this service cares about.
type Title struct {
	Title string
	Year  int
	// Type is OMDb's media type: "movie", "series", or "episode".
	Type string
	// Metascore is the Metacritic critic score (0-100), or nil when Metacritic
	// has no score for the title. Metacritic scores TV by season rather than by
	// series, so this is frequently absent for shows.
	Metascore *int
}

// NewClient returns a configured OMDb client.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		BaseURL: "https://www.omdbapi.com/",
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
			maxRequests: 20,
			window:      10 * time.Second,
		},
		circuitBreaker: &circuitBreaker{
			maxFailures: 5,
			timeout:     60 * time.Second,
		},
	}
}

// allow reports whether a request fits inside the current window.
func (rl *rateLimiter) allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for len(rl.requests) > 0 && now.Sub(rl.requests[0]) > rl.window {
		rl.requests = rl.requests[1:]
	}
	if len(rl.requests) < rl.maxRequests {
		rl.requests = append(rl.requests, now)
		return true
	}
	return false
}

// wait blocks until a request can be made or ctx is done.
func (rl *rateLimiter) wait(ctx context.Context) error {
	for !rl.allow() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// canExecute reports whether the breaker permits a request.
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

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	cb.state = closed
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailure = time.Now()
	if cb.failureCount >= cb.maxFailures {
		cb.state = open
	}
}

// do builds a request from safeURL (which carries no api key) and attaches the
// key just before sending, so the key never reaches an error string or a log.
func (c *Client) do(ctx context.Context, safeURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, safeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	q := req.URL.Query()
	q.Set("apikey", c.apiKey)
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

// apiResponse is the raw OMDb payload. Numeric fields arrive as strings and may
// be the literal "N/A", so everything is decoded as a string and parsed.
type apiResponse struct {
	Title     string `json:"Title"`
	Year      string `json:"Year"`
	Type      string `json:"Type"`
	Metascore string `json:"Metascore"`
	Ratings   []struct {
		Source string `json:"Source"`
		Value  string `json:"Value"`
	} `json:"Ratings"`
	Response string `json:"Response"`
	Error    string `json:"Error"`
}

// GetByIMDbID looks a title up by IMDb ID (e.g. "tt0133093"). It returns
// ErrNotFound when OMDb has no such record.
func (c *Client) GetByIMDbID(ctx context.Context, imdbID string) (*Title, error) {
	l := logging.FromContext(ctx)
	if strings.TrimSpace(imdbID) == "" {
		return nil, fmt.Errorf("omdb: empty imdb id")
	}
	// safeURL never includes the api key so it is safe to embed in errors and logs.
	safeURL := fmt.Sprintf("%s?i=%s", c.BaseURL, url.QueryEscape(imdbID))

	fetch := func() (*Title, error) {
		if !c.circuitBreaker.canExecute() {
			return nil, ErrCircuitOpen
		}
		if err := c.rateLimiter.wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}

		resp, err := c.do(ctx, safeURL)
		if err != nil {
			c.circuitBreaker.recordFailure()
			return nil, &APIError{StatusCode: 0, Message: "transport error", URL: safeURL, Method: http.MethodGet}
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				l.Errorw("failed to close response body", zap.Error(err))
			}
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 500 {
				c.circuitBreaker.recordFailure()
			}
			return nil, &APIError{
				StatusCode: resp.StatusCode,
				Message:    string(body),
				URL:        safeURL,
				Method:     http.MethodGet,
			}
		}

		var raw apiResponse
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			c.circuitBreaker.recordFailure()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		c.circuitBreaker.recordSuccess()

		// OMDb signals "no such title" with HTTP 200 and Response:"False".
		if !strings.EqualFold(raw.Response, "True") {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, raw.Error)
		}

		return &Title{
			Title:     raw.Title,
			Year:      parseYear(raw.Year),
			Type:      raw.Type,
			Metascore: raw.metascore(),
		}, nil
	}

	var lastErr error
	for attempt := range 3 {
		result, err := fetch()
		if err == nil {
			return result, nil
		}
		lastErr = err

		// A missing title and an open breaker both fail identically on retry.
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrCircuitOpen) {
			return nil, err
		}

		l.Warnw("Retrying OMDb lookup", "attempt", attempt+1, "imdb_id", imdbID, zap.Error(err))
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}
	return nil, lastErr
}

// metascore reads the Metacritic score, preferring the top-level Metascore field
// and falling back to the "84/100" form in Ratings[]. Returns nil when absent,
// "N/A", or out of the 0-100 range, so callers can tell missing from zero.
func (r *apiResponse) metascore() *int {
	if n, ok := parseScore(r.Metascore); ok {
		return &n
	}
	for _, rating := range r.Ratings {
		if !strings.EqualFold(rating.Source, "Metacritic") {
			continue
		}
		value, _, _ := strings.Cut(rating.Value, "/")
		if n, ok := parseScore(value); ok {
			return &n
		}
	}
	return nil
}

// parseScore parses an OMDb score string, rejecting "N/A", blanks, and
// out-of-range values.
func parseScore(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

// parseYear parses OMDb's year field, which can be "1999", "2010–2015", or
// "2010–" for a running series. Returns 0 when unparseable.
func parseYear(s string) int {
	s = strings.TrimSpace(s)
	// Series ranges use an en dash; cut on both forms.
	for _, sep := range []string{"–", "-"} {
		if before, _, found := strings.Cut(s, sep); found {
			s = before
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
