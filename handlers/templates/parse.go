package templates

import "html/template"

// ParseTemplates parses HTML templates from the embedded filesystem.
// It takes a variadic list of template file paths and returns a parsed template
// or an error if parsing fails.
func ParseTemplates(files ...string) (*template.Template, error) {
	return template.ParseFS(FS, files...)
}
