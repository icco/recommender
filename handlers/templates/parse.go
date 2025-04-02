package templates

import "html/template"

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
	}

	return template.New("").Funcs(funcMap).ParseFS(FS, files...)
}
