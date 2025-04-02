package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"log/slog"
)

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

type SearchResult struct {
	Results []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		ReleaseDate string  `json:"release_date"`
		PosterPath  string  `json:"poster_path"`
		VoteAverage float64 `json:"vote_average"`
	} `json:"results"`
}

type TVSearchResult struct {
	Results []struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		FirstAirDate string  `json:"first_air_date"`
		PosterPath   string  `json:"poster_path"`
		VoteAverage  float64 `json:"vote_average"`
	} `json:"results"`
}

func NewClient(apiKey string, logger *slog.Logger) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    "https://api.themoviedb.org/3",
		httpClient: &http.Client{},
		logger:     logger,
	}
}

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

	var result TVSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (c *Client) GetPosterURL(posterPath string) string {
	if posterPath == "" {
		return ""
	}
	return fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", posterPath)
}
