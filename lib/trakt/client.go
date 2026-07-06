// Package trakt is a minimal Trakt API client: OAuth device flow, token
// refresh, and the sync endpoints the recommender uses as ranking signals.
package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "https://api.trakt.tv"
	apiVersion     = "2"
)

// Client talks to the Trakt API. BaseURL is overridable for tests.
type Client struct {
	clientID     string
	clientSecret string
	BaseURL      string
	httpClient   *http.Client
}

// NewClient returns a Trakt client for the given API app credentials.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		BaseURL:      defaultBaseURL,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// IDs holds the external identifiers Trakt returns for a title.
type IDs struct {
	Trakt int    `json:"trakt"`
	IMDb  string `json:"imdb"`
	TMDb  int    `json:"tmdb"`
	TVDb  int    `json:"tvdb"`
}

// Media is a movie or show entry within a sync row.
type Media struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   IDs    `json:"ids"`
}

// SyncRow is one item from a sync/* endpoint. Exactly one of Movie/Show is set;
// Rating is populated for ratings endpoints.
type SyncRow struct {
	Rating int    `json:"rating"`
	Movie  *Media `json:"movie"`
	Show   *Media `json:"show"`
}

// DeviceCode is the response from the device-code endpoint.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is an OAuth token set.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
}

// ExpiresAt is when the access token expires.
func (t Token) ExpiresAt() time.Time {
	return time.Unix(t.CreatedAt, 0).Add(time.Duration(t.ExpiresIn) * time.Second)
}

func (c *Client) postJSON(ctx context.Context, path string, body, out any) (int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/"+path, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("trakt %s: HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// RequestDeviceCode starts the OAuth device flow.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	if _, err := c.postJSON(ctx, "oauth/device/code", map[string]string{"client_id": c.clientID}, &dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

// PollForToken polls once for the token; HTTP 400 means "authorization pending".
// Returns (nil, nil) when still pending so the caller can wait and retry.
func (c *Client) PollForToken(ctx context.Context, deviceCode string) (*Token, error) {
	var tok Token
	status, err := c.postJSON(ctx, "oauth/device/token", map[string]string{
		"code": deviceCode, "client_id": c.clientID, "client_secret": c.clientSecret,
	}, &tok)
	if status == http.StatusBadRequest {
		return nil, nil // pending
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// RefreshToken exchanges a refresh token for a new token set.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*Token, error) {
	var tok Token
	if _, err := c.postJSON(ctx, "oauth/token", map[string]string{
		"refresh_token": refreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "refresh_token",
	}, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Sync GETs a sync/* endpoint (e.g. "sync/watched/movies") with the access token.
func (c *Client) Sync(ctx context.Context, accessToken, path string) ([]SyncRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("trakt-api-version", apiVersion)
	req.Header.Set("trakt-api-key", c.clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("trakt %s: HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	var rows []SyncRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return rows, nil
}
