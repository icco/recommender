package plex

import (
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
)

// parseGUIDs extracts imdb/tmdb/tvdb identifiers from Plex GUID URIs like
// "imdb://tt0133093", "tmdb://603", "tvdb://12345". Unknown schemes are ignored.
func parseGUIDs(guids []string) (imdb string, tmdb *int, tvdb string) {
	for _, g := range guids {
		switch {
		case strings.HasPrefix(g, "imdb://"):
			imdb = strings.TrimPrefix(g, "imdb://")
		case strings.HasPrefix(g, "tmdb://"):
			if n, err := strconv.Atoi(strings.TrimPrefix(g, "tmdb://")); err == nil {
				tmdb = &n
			}
		case strings.HasPrefix(g, "tvdb://"):
			tvdb = strings.TrimPrefix(g, "tvdb://")
		}
	}
	return imdb, tmdb, tvdb
}

// joinGenres returns a comma-separated, order-preserving, de-duplicated list of
// genre tags. Empty when there are none.
func joinGenres(tags []components.Tag) string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.Tag == "" {
			continue
		}
		if _, ok := seen[t.Tag]; ok {
			continue
		}
		seen[t.Tag] = struct{}{}
		out = append(out, t.Tag)
	}
	return strings.Join(out, ", ")
}
