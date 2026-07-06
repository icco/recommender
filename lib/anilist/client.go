// Package anilist is a minimal AniList GraphQL client for a user's public anime
// list and scores, used as a recommendation ranking signal.
package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultURL = "https://graphql.anilist.co"

// Client queries the public AniList GraphQL API. URL is overridable for tests.
type Client struct {
	URL        string
	httpClient *http.Client
}

// NewClient returns an AniList client.
func NewClient() *Client {
	return &Client{URL: defaultURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

// Entry is one rated anime from a user's list, score normalized to 0..10.
type Entry struct {
	Title string
	Year  int
	Score float64
}

const listQuery = `query($u:String){
  User(name:$u){ mediaListOptions { scoreFormat } }
  MediaListCollection(userName:$u, type:ANIME){ lists { entries {
    score
    media { seasonYear title { romaji english } }
  } } }
}`

// List returns the user's rated anime (score > 0) with scores normalized to 0..10.
func (c *Client) List(ctx context.Context, username string) ([]Entry, error) {
	reqBody, err := json.Marshal(map[string]any{
		"query":     listQuery,
		"variables": map[string]string{"u": username},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("anilist: HTTP %d: %s", resp.StatusCode, string(data))
	}

	var out struct {
		Data struct {
			User struct {
				MediaListOptions struct {
					ScoreFormat string `json:"scoreFormat"`
				} `json:"mediaListOptions"`
			} `json:"User"`
			MediaListCollection struct {
				Lists []struct {
					Entries []struct {
						Score float64 `json:"score"`
						Media struct {
							SeasonYear int `json:"seasonYear"`
							Title      struct {
								Romaji  string `json:"romaji"`
								English string `json:"english"`
							} `json:"title"`
						} `json:"media"`
					} `json:"entries"`
				} `json:"lists"`
			} `json:"MediaListCollection"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode anilist: %w", err)
	}

	format := out.Data.User.MediaListOptions.ScoreFormat
	var entries []Entry
	for _, l := range out.Data.MediaListCollection.Lists {
		for _, e := range l.Entries {
			if e.Score <= 0 {
				continue
			}
			title := e.Media.Title.English
			if title == "" {
				title = e.Media.Title.Romaji
			}
			if title == "" {
				continue
			}
			entries = append(entries, Entry{
				Title: title,
				Year:  e.Media.SeasonYear,
				Score: normalizeScore(e.Score, format),
			})
		}
	}
	return entries, nil
}

// normalizeScore maps AniList's per-user score format to a 0..10 scale.
func normalizeScore(score float64, format string) float64 {
	switch format {
	case "POINT_100":
		return score / 10.0
	case "POINT_5":
		return score * 2.0
	case "POINT_3":
		return score / 3.0 * 10.0
	default: // POINT_10, POINT_10_DECIMAL
		return score
	}
}
