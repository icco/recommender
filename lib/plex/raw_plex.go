package plex

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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

// fetchLibraryItemsViaJSON lists all items in a library section using the same path as
// plexgo's Content.ListContent: GET /library/sections/{id}/all (see Plex support docs).
// We avoid unmarshaling into plexgo structs because many PMS builds return 0/1 instead of
// JSON booleans, which breaks strict decoding.
func (c *Client) fetchLibraryItemsViaJSON(ctx context.Context, libraryKey string) ([]PlexItem, error) {
	const pageSize = 200
	start := 0
	var allItems []PlexItem
	for page := 0; page < 500; page++ {
		q := url.Values{}
		q.Set("X-Plex-Container-Start", strconv.Itoa(start))
		q.Set("X-Plex-Container-Size", strconv.Itoa(pageSize))
		path := "/library/sections/" + url.PathEscape(libraryKey) + "/all?" + q.Encode()

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
		if len(metaMaps) == 0 {
			break
		}
		for _, m := range metaMaps {
			allItems = append(allItems, mapMetadataToPlexItem(m))
		}

		totalSize, hasTotal := mediaContainerTotalSize(mc)
		nextStart := start + len(metaMaps)
		if hasTotal && nextStart >= totalSize {
			break
		}
		if len(metaMaps) < pageSize {
			break
		}
		start = nextStart
	}
	return allItems, nil
}

// mediaContainerTotalSize reads totalSize from a Plex MediaContainer when it is a positive integer.
// totalSize=0 is treated as absent so we still use per-page heuristics for empty vs unknown totals.
func mediaContainerTotalSize(mc map[string]any) (n int, ok bool) {
	if mc == nil {
		return 0, false
	}
	v, found := anyToInt(mc["totalSize"])
	if !found || v <= 0 {
		return 0, false
	}
	return v, true
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
