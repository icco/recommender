package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"log/slog"
)

// Client represents a TMDb API client that handles communication with The Movie Database API.
// It provides methods for searching movies and TV shows and retrieving their metadata.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
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
// It initializes the HTTP client and sets up the base URL for TMDb API requests.
func NewClient(apiKey string, logger *slog.Logger) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    "https://api.themoviedb.org/3",
		httpClient: &http.Client{},
		logger:     logger,
	}
}

// SearchMovie searches for movies on TMDb using the provided title and year.
// It returns a list of matching movies with their metadata.
func (c *Client) SearchMovie(ctx context.Context, title string, year int) (*SearchResult, error) {
	url := fmt.Sprintf("%s/search/movie?api_key=%s&query=%s&year=%d",
		c.baseURL, c.apiKey, strings.ReplaceAll(title, " ", "+"), year)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.logger.Error("failed to close response body", "error", err)
		}
	}()

	var result SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// SearchTVShow searches for TV shows on TMDb using the provided title and year.
// It returns a list of matching TV shows with their metadata.
func (c *Client) SearchTVShow(ctx context.Context, title string, year int) (*TVSearchResult, error) {
	url := fmt.Sprintf("%s/search/tv?api_key=%s&query=%s&first_air_date_year=%d",
		c.baseURL, c.apiKey, strings.ReplaceAll(title, " ", "+"), year)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.logger.Error("failed to close response body", "error", err)
		}
	}()

	// Check response status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TMDb API returned non-200 status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var result TVSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetPosterURL generates the full URL for a movie or TV show poster using the poster path.
// It returns an empty string if the poster path is empty.
func (c *Client) GetPosterURL(posterPath string) string {
	if posterPath == "" {
		return ""
	}
	return fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", posterPath)
}
