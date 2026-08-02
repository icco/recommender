package recommend

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/icco/recommender/lib/metacritic"
	"github.com/icco/recommender/models"
	"google.golang.org/genai"
)

// metacriticSection maps a recommendation type to the metacritic.com URL section.
func metacriticSection(recType string) string {
	if recType == models.TypeTVShow {
		return metacritic.SectionTV
	}
	return metacritic.SectionMovie
}

type pick struct {
	ID          uint   `json:"id"`
	Explanation string `json:"explanation"`
}

type pickResponse struct {
	Movies  []pick `json:"movies"`
	TVShows []pick `json:"tvshows"`
	Books   []pick `json:"books"`
}

// parsePickResponse decodes the model's JSON. Unknown fields are ignored.
func parsePickResponse(raw string) (pickResponse, error) {
	var pr pickResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &pr); err != nil {
		return pr, fmt.Errorf("parse pick response: %w", err)
	}
	return pr, nil
}

// pickSchema is the Gemini response schema: two arrays of {id, explanation}.
func pickSchema() *genai.Schema {
	item := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"id":          {Type: genai.TypeInteger},
			"explanation": {Type: genai.TypeString},
		},
		Required: []string{"id", "explanation"},
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"movies":  {Type: genai.TypeArray, Items: item},
			"tvshows": {Type: genai.TypeArray, Items: item},
			"books":   {Type: genai.TypeArray, Items: item},
		},
		Required: []string{"movies", "tvshows", "books"},
	}
}

func candByID(shortlist []candidate) map[uint]candidate {
	m := make(map[uint]candidate, len(shortlist))
	for _, c := range shortlist {
		m[c.ID] = c
	}
	return m
}

func toRec(c candidate, explanation string, date time.Time) models.Recommendation {
	rec := models.Recommendation{
		Title: c.Title, Type: c.Type, Year: c.Year, Rating: c.Rating,
		Genre: strings.Join(c.Genres, ", "), PosterURL: c.PosterURL, Runtime: c.Runtime,
		Explanation: explanation, Date: date, Metascore: c.Metascore,
		Author: c.Author, SourceURL: c.SourceURL,
	}
	// Only link titles Metacritic scored: best proxy for the slug resolving.
	// Books are excluded outright — Metacritic has no book section, so a slug
	// built from a book title would resolve to an unrelated film.
	if c.Metascore != nil && c.Type != models.TypeBook {
		rec.MetacriticURL = metacritic.URLFor(metacriticSection(c.Type), c.Title)
	}
	if c.TMDbID != nil {
		rec.TMDbID = *c.TMDbID
	}
	switch c.Type {
	case models.TypeMovie:
		id := c.ID
		rec.MovieID = &id
	case models.TypeTVShow:
		id := c.ID
		rec.TVShowID = &id
	case models.TypeBook:
		id := c.ID
		rec.BookID = &id
	}
	return rec
}

func hasGenre(c candidate, want string) bool {
	for _, g := range c.Genres {
		if strings.Contains(strings.ToLower(g), want) {
			return true
		}
	}
	return false
}

// selectMovies fills up to `target` slots (comedy, action/drama, rewatch, wildcard)
// from valid picks, padding from the shortlist if short. Unknown IDs are ignored;
// the rewatch slot requires ViewCount>0. Caller sets Date.
func selectMovies(picks []pick, shortlist []candidate, target int) []models.Recommendation {
	byID := candByID(shortlist)
	used := make(map[uint]bool)
	var out []models.Recommendation

	take := func(c candidate, expl string) {
		used[c.ID] = true
		out = append(out, toRec(c, expl, time.Time{}))
	}

	// Ordered list of valid movie picks with their explanations.
	type vc struct {
		c    candidate
		expl string
	}
	var valid []vc
	for _, p := range picks {
		c, ok := byID[p.ID]
		if !ok || c.Type != models.TypeMovie {
			continue
		}
		valid = append(valid, vc{c, p.Explanation})
	}

	fillRole := func(match func(candidate) bool) {
		if len(out) >= target {
			return
		}
		for _, v := range valid {
			if used[v.c.ID] {
				continue
			}
			if match(v.c) {
				take(v.c, v.expl)
				return
			}
		}
	}

	fillRole(func(c candidate) bool { return hasGenre(c, "comedy") })
	fillRole(func(c candidate) bool { return hasGenre(c, "action") || hasGenre(c, "drama") })
	fillRole(func(c candidate) bool { return c.ViewCount > 0 }) // rewatch
	// Wildcards from remaining valid picks.
	for _, v := range valid {
		if len(out) >= target {
			break
		}
		if used[v.c.ID] {
			continue
		}
		take(v.c, v.expl)
	}
	// Pad from ranked shortlist if still short (e.g. model returned too few).
	for _, c := range shortlist {
		if len(out) >= target {
			break
		}
		if c.Type != models.TypeMovie || used[c.ID] {
			continue
		}
		take(c, "")
	}
	return out
}

// selectByType fills up to `target` slots of one type from valid picks, in the
// order the model returned them, then pads from the ranked shortlist if the model
// returned too few. Unknown or wrong-type IDs are ignored. Caller sets Date.
//
// Neither TV shows nor books use role slots the way movies do: TV candidates are
// already filtered to unwatched, and books have no rewatch or genre roles to fill.
func selectByType(picks []pick, shortlist []candidate, recType string, target int) []models.Recommendation {
	byID := candByID(shortlist)
	used := make(map[uint]bool)
	var out []models.Recommendation
	for _, p := range picks {
		if len(out) >= target {
			break
		}
		c, ok := byID[p.ID]
		if !ok || c.Type != recType || used[c.ID] {
			continue
		}
		used[c.ID] = true
		out = append(out, toRec(c, p.Explanation, time.Time{}))
	}
	for _, c := range shortlist {
		if len(out) >= target {
			break
		}
		if c.Type != recType || used[c.ID] {
			continue
		}
		used[c.ID] = true
		out = append(out, toRec(c, "", time.Time{}))
	}
	return out
}

// selectTVShows fills up to `target` TV slots. All candidates here are already
// unwatched (loadCandidates filters).
func selectTVShows(picks []pick, shortlist []candidate, target int) []models.Recommendation {
	return selectByType(picks, shortlist, models.TypeTVShow, target)
}

// selectBooks fills up to `target` book slots from the Goodreads to-read shelf.
func selectBooks(picks []pick, shortlist []candidate, target int) []models.Recommendation {
	return selectByType(picks, shortlist, models.TypeBook, target)
}
