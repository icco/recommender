package templates

import (
	"html/template"

	"github.com/icco/recommender/models"
)

// ParseTemplates parses HTML templates from the embedded filesystem.
// It takes a variadic list of template file paths and returns a parsed template
// or an error if parsing fails.
func ParseTemplates(files ...string) (*template.Template, error) {
	funcMap := template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"subtract": func(a, b int) int {
			return a - b
		},
		"ofType": ofType,
	}

	return template.New("").Funcs(funcMap).ParseFS(FS, files...)
}

// ofType selects one tier's recommendations, so a page can skip an empty tier's
// heading rather than render a bare "Books" over nothing when Goodreads is off.
func ofType(recType string, recs []models.Recommendation) []models.Recommendation {
	var out []models.Recommendation
	for _, rec := range recs {
		if rec.Type == recType {
			out = append(out, rec)
		}
	}
	return out
}
