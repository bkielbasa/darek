package serve

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var StaticFS embed.FS

// parseTemplates parses the layout + every named template file under
// templates/. Each handler renders by name (e.g. "index.html").
func parseTemplates() (*template.Template, error) {
	files, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob templates: %w", err)
	}
	t, err := template.ParseFS(templatesFS, files...)
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return t, nil
}
