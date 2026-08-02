package metacritic

import "testing"

func TestSlug(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"The Matrix":               "the-matrix",
		"Spider-Man: No Way Home":  "spider-man-no-way-home",
		"Schindler's List":         "schindlers-list",
		"Schindler’s List":         "schindlers-list", // curly apostrophe
		"Sex & the City":           "sex-and-the-city",
		"WALL·E":                   "wall-e",
		"  Leading and trailing  ": "leading-and-trailing",
		"Multiple   Spaces":        "multiple-spaces",
		"12 Angry Men":             "12-angry-men",
		"...And Justice for All":   "and-justice-for-all",
		"&":                        "and",
		"":                         "",
		"!!!":                      "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		section string
		title   string
		want    string
	}{
		{SectionMovie, "The Matrix", "https://www.metacritic.com/movie/the-matrix/"},
		{SectionTV, "Severance", "https://www.metacritic.com/tv/severance/"},
		{SectionMovie, "", ""},
		{SectionMovie, "!!!", ""},
		{"book", "The Matrix", ""}, // unknown section
	}
	for _, c := range cases {
		if got := URLFor(c.section, c.title); got != c.want {
			t.Errorf("URLFor(%q, %q) = %q, want %q", c.section, c.title, got, c.want)
		}
	}
}
