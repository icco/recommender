package plex

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/operations"
)

func (c *Client) plexGET(ctx context.Context, path string) ([]byte, error) {
	base := strings.TrimRight(c.plexURL, "/")
	reqURL := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.plexToken)

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.logger.Error("failed to close Plex response body", slog.Any("error", cerr))
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plex HTTP %d: %s", resp.StatusCode, truncateForErr(body, 200))
	}
	return body, nil
}

func truncateForErr(b []byte, max int) string {
	s := string(b)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// fetchLibrarySectionsViaJSON uses a tolerant JSON decode so PMS builds that return numeric
// flags (0/1) instead of booleans still work — plexgo's strict structs fail on those responses.
func (c *Client) fetchLibrarySectionsViaJSON(ctx context.Context) (*operations.GetSectionsResponse, error) {
	body, err := c.plexGET(ctx, "/library/sections/all")
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode sections JSON: %w", err)
	}
	mc, ok := root["MediaContainer"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Plex sections response: missing MediaContainer")
	}
	dirRaw, ok := mc["Directory"]
	if !ok {
		return &operations.GetSectionsResponse{
			StatusCode: http.StatusOK,
			Object: &operations.GetSectionsResponseBody{
				MediaContainer: &operations.GetSectionsMediaContainer{
					Directory: nil,
				},
			},
		}, nil
	}
	entries := asMapSlice(dirRaw)
	sections := make([]components.LibrarySection, 0, len(entries))
	for _, m := range entries {
		key := flexString(m, "key")
		if key == "" {
			continue
		}
		title := flexString(m, "title")
		typeStr := flexString(m, "type")
		k, t := key, title
		sections = append(sections, components.LibrarySection{
			Key:      &k,
			Title:    &t,
			Type:     components.MediaTypeString(typeStr),
			Language: flexString(m, "language"),
			UUID:     flexString(m, "uuid"),
		})
	}
	return &operations.GetSectionsResponse{
		StatusCode: http.StatusOK,
		Object: &operations.GetSectionsResponseBody{
			MediaContainer: &operations.GetSectionsMediaContainer{
				Directory: sections,
			},
		},
	}, nil
}

func (c *Client) fetchLibraryItemsViaJSON(ctx context.Context, libraryKey string) ([]PlexItem, error) {
	path := "/library/sections/" + url.PathEscape(libraryKey)
	body, err := c.plexGET(ctx, path)
	if err != nil {
		return nil, err
	}
	root, err := decodeJSONObject(body)
	if err != nil {
		return nil, fmt.Errorf("decode library details JSON: %w", err)
	}
	mc, ok := root["MediaContainer"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Plex library response: missing MediaContainer")
	}
	metaMaps := itemsFromMediaContainer(mc)
	items := make([]PlexItem, 0, len(metaMaps))
	for _, m := range metaMaps {
		pi := mapMetadataToPlexItem(m)
		items = append(items, pi)
	}
	return items, nil
}

func mapMetadataToPlexItem(m map[string]any) PlexItem {
	ratingKey := flexString(m, "ratingKey")
	key := flexString(m, "key")
	title := flexString(m, "title")
	typ := flexString(m, "type")
	summary := flexString(m, "summary")

	var rating *float64
	if f, ok := anyToFloat64(m["rating"]); ok && f != 0 {
		rating = &f
	}

	var thumb *string
	if t := flexString(m, "thumb"); t != "" {
		thumb = &t
	}
	var art *string
	if a := flexString(m, "art"); a != "" {
		art = &a
	}

	var year *int
	if y, ok := anyToInt(m["year"]); ok && y != 0 {
		year = &y
	}

	var duration *int
	if d, ok := anyToInt(m["duration"]); ok && d != 0 {
		duration = &d
	}

	var childCount *int
	if d, ok := anyToInt(m["childCount"]); ok && d != 0 {
		childCount = &d
	}

	var viewCount *int
	if vc, ok := anyToInt(m["viewCount"]); ok {
		viewCount = &vc
	}

	var leafCount *int
	if lc, ok := anyToInt(m["leafCount"]); ok && lc != 0 {
		leafCount = &lc
	}

	addedAt := flexInt64Required(m, "addedAt")
	var updatedAt *int64
	if u := flexInt64Ptr(m, "updatedAt"); u != nil {
		updatedAt = u
	}

	return PlexItem{
		RatingKey:  ratingKey,
		Key:        key,
		Title:      title,
		Type:       typ,
		Year:       year,
		Rating:     rating,
		Summary:    summary,
		Thumb:      thumb,
		Art:        art,
		Duration:   duration,
		AddedAt:    addedAt,
		UpdatedAt:  updatedAt,
		ViewCount:  viewCount,
		Genre:      flexGenreTags(m),
		LeafCount:  leafCount,
		ChildCount: childCount,
	}
}
