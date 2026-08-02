// Package metacritic builds links to metacritic.com pages. Metacritic has no
// public API and returns no canonical URL through OMDb, so page URLs are
// derived from the title slug, which is how metacritic.com addresses titles.
package metacritic

import (
	"strings"
	"unicode"
)

const baseURL = "https://www.metacritic.com"

// Media section values for URLFor.
const (
	SectionMovie = "movie"
	SectionTV    = "tv"
)

// URLFor returns the metacritic.com page URL for a title in the given section
// ("movie" or "tv"). It returns "" when the title yields an empty slug.
//
// The slug is derived, not looked up: Metacritic exposes no id we can join on.
// It resolves for the large majority of titles, but disambiguated pages (a
// remake sharing its original's name, which Metacritic suffixes with a year)
// will not match. Callers should only publish this link for titles that have a
// Metascore, which at least confirms Metacritic covers the title.
func URLFor(section, title string) string {
	slug := Slug(title)
	if slug == "" {
		return ""
	}
	if section != SectionMovie && section != SectionTV {
		return ""
	}
	return baseURL + "/" + section + "/" + slug + "/"
}

// Slug converts a title to Metacritic's URL form: lowercase, apostrophes
// dropped, "&" spelled out, and every other run of non-alphanumerics collapsed
// to a single hyphen.
func Slug(title string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r == '\'' || r == '’':
			// Apostrophes vanish rather than becoming separators:
			// "Schindler's List" -> "schindlers-list".
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
		case r == '&':
			// One separator regardless of whether whitespace preceded it, so
			// "Sex & the City" -> "sex-and-the-city", not "sex--and".
			if b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteString("and")
			pendingHyphen = true
		default:
			pendingHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}
