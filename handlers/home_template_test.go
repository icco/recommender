package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/icco/recommender/handlers/templates"
	"github.com/icco/recommender/models"
)

// renderHome renders the home page the way the handler does, so template
// changes are exercised rather than just compiled.
func renderHome(t *testing.T, recs []models.Recommendation) string {
	t.Helper()
	tmpl, err := templates.ParseTemplates(baseTemplate, "home.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, baseTemplate, recs); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return b.String()
}

func TestHomeRendersMetacriticScoreAndLink(t *testing.T) {
	t.Parallel()
	score := 73
	out := renderHome(t, []models.Recommendation{{
		Title: "The Matrix", Type: models.TypeMovie, Year: 1999, Rating: 8.7,
		Genre: "Action", Runtime: 136, Date: time.Now(),
		Metascore: &score, MetacriticURL: "https://www.metacritic.com/movie/the-matrix/",
	}})

	if !strings.Contains(out, "73") {
		t.Errorf("Metascore not rendered:\n%s", out)
	}
	if !strings.Contains(out, `href="https://www.metacritic.com/movie/the-matrix/"`) {
		t.Errorf("Metacritic link not rendered:\n%s", out)
	}
	// 73 is in Metacritic's "favorable" band.
	if !strings.Contains(out, "bg-green-600") {
		t.Error("favorable score should use the green band")
	}
}

func TestHomeMetacriticBands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		score int
		class string
	}{
		{95, "bg-green-600"},
		{61, "bg-green-600"},
		{60, "bg-yellow-500"},
		{40, "bg-yellow-500"},
		{39, "bg-red-600"},
		{0, "bg-red-600"},
	}
	for _, c := range cases {
		score := c.score
		out := renderHome(t, []models.Recommendation{{
			Title: "T", Type: models.TypeMovie, Year: 2000, Date: time.Now(), Metascore: &score,
		}})
		if !strings.Contains(out, c.class) {
			t.Errorf("score %d should render %s", c.score, c.class)
		}
	}
}

// A zero Metascore is a real score and must render; only a nil one is hidden.
func TestHomeRendersZeroMetascore(t *testing.T) {
	t.Parallel()
	zero := 0
	out := renderHome(t, []models.Recommendation{{
		Title: "Panned", Type: models.TypeMovie, Year: 2000, Date: time.Now(), Metascore: &zero,
	}})
	if !strings.Contains(out, "bg-red-600") {
		t.Errorf("zero Metascore should still render a badge:\n%s", out)
	}
}

func TestHomeOmitsMetacriticWhenUnscored(t *testing.T) {
	t.Parallel()
	out := renderHome(t, []models.Recommendation{{
		Title: "Unscored", Type: models.TypeTVShow, Year: 2022, Date: time.Now(),
	}})
	if strings.Contains(out, "Metacritic") {
		t.Errorf("unscored title should show no Metacritic row:\n%s", out)
	}
}

// A score with no usable slug still shows the badge, just without a link.
func TestHomeRendersScoreWithoutLink(t *testing.T) {
	t.Parallel()
	score := 80
	out := renderHome(t, []models.Recommendation{{
		Title: "No Link", Type: models.TypeTVShow, Year: 2022, Date: time.Now(), Metascore: &score,
	}})
	if !strings.Contains(out, "Metacritic") {
		t.Error("badge should render without a link")
	}
	if strings.Contains(out, "href=\"https://www.metacritic.com") {
		t.Error("no link should be emitted when MetacriticURL is empty")
	}
}
