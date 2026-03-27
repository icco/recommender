package plex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
)

// plexRatingKey accepts JSON string or number (Plex sometimes varies).
type plexRatingKey string

func (k *plexRatingKey) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*k = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*k = plexRatingKey(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*k = plexRatingKey(n.String())
		return nil
	}
	var i int64
	if err := json.Unmarshal(b, &i); err == nil {
		*k = plexRatingKey(strconv.FormatInt(i, 10))
		return nil
	}
	return fmt.Errorf("ratingKey: unsupported JSON %s", string(b))
}

// sectionListMetadata is a minimal Plex metadata row for GET …/library/sections/{id}/all.
// Newer PMS can send 0/1 for fields that plexgo models as *bool (e.g. search, secondary),
// which breaks encoding/json; we only decode fields the cache needs.
type sectionListMetadata struct {
	RatingKey  plexRatingKey `json:"ratingKey"`
	Key        string        `json:"key"`
	Title      string        `json:"title"`
	Type       string        `json:"type"`
	Year       *int          `json:"year,omitempty"`
	Rating     *float32      `json:"rating,omitempty"`
	Summary    *string       `json:"summary,omitempty"`
	Thumb      *string       `json:"thumb,omitempty"`
	Art        *string       `json:"art,omitempty"`
	Duration   *int          `json:"duration,omitempty"`
	AddedAt    int64         `json:"addedAt"`
	UpdatedAt  *int64        `json:"updatedAt,omitempty"`
	ViewCount  *int          `json:"viewCount,omitempty"`
	Genre      []struct {
		Tag string `json:"tag"`
	} `json:"Genre,omitempty"`
	LeafCount  *int `json:"leafCount,omitempty"`
	ChildCount *int `json:"childCount,omitempty"`
}

func sectionMetadataToPlexItem(md sectionListMetadata) PlexItem {
	var genres []components.Tag
	for _, g := range md.Genre {
		genres = append(genres, components.Tag{Tag: g.Tag})
	}
	rk := string(md.RatingKey)
	var rating *float64
	if md.Rating != nil {
		x := float64(*md.Rating)
		rating = &x
	}
	summary := ""
	if md.Summary != nil {
		summary = *md.Summary
	}
	return PlexItem{
		RatingKey:  rk,
		Key:        md.Key,
		Title:      md.Title,
		Type:       md.Type,
		Year:       md.Year,
		Rating:     rating,
		Summary:    summary,
		Thumb:      md.Thumb,
		Art:        md.Art,
		Duration:   md.Duration,
		AddedAt:    md.AddedAt,
		UpdatedAt:  md.UpdatedAt,
		ViewCount:  md.ViewCount,
		Genre:      genres,
		LeafCount:  md.LeafCount,
		ChildCount: md.ChildCount,
	}
}

// listSectionContentAll pages GET /library/sections/{id}/all with a tolerant JSON decode.
// It does not use plexgo's full Metadata type (PMS can send numeric booleans on movie rows).
func (c *Client) listSectionContentAll(ctx context.Context, sectionID string) ([]PlexItem, error) {
	const pageSize = 200
	start := 0
	var all []PlexItem
	base := strings.TrimRight(c.plexURL, "/")

	for range 500 {
		u, err := url.JoinPath(base, "library", "sections", sectionID, "all")
		if err != nil {
			return nil, fmt.Errorf("build section URL: %w", err)
		}
		q := url.Values{}
		q.Set("X-Plex-Container-Start", strconv.Itoa(start))
		q.Set("X-Plex-Container-Size", strconv.Itoa(pageSize))
		full := u + "?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Token", c.plexToken)
		req.Header.Set("User-Agent", "recommender")

		httpResp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(httpResp.Body)
		if cerr := httpResp.Body.Close(); cerr != nil {
			c.logger.Debug("close Plex list response body", slog.Any("error", cerr))
		}
		if readErr != nil {
			return nil, readErr
		}
		if httpResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("plex list section %s: HTTP %d: %s", sectionID, httpResp.StatusCode, strings.TrimSpace(string(body)))
		}

		var payload struct {
			MediaContainer *struct {
				TotalSize *int64               `json:"totalSize,omitempty"`
				Metadata  []sectionListMetadata `json:"Metadata,omitempty"`
			} `json:"MediaContainer"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("error unmarshaling json response body: %w", err)
		}
		if payload.MediaContainer == nil {
			break
		}
		mdList := payload.MediaContainer.Metadata
		if len(mdList) == 0 {
			break
		}
		for i := range mdList {
			all = append(all, sectionMetadataToPlexItem(mdList[i]))
		}
		n := len(mdList)
		total := int64(0)
		if payload.MediaContainer.TotalSize != nil {
			total = *payload.MediaContainer.TotalSize
		}
		start += n
		if total > 0 && int64(start) >= total {
			break
		}
		if n < pageSize {
			break
		}
	}
	return all, nil
}
