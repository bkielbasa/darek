package serve

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var StaticFS embed.FS

// templateBundle holds the parsed templates the server renders from.
//   - pageSets: one *template.Template per full-page file. Each set contains
//     layout.html, every _*.html partial, and that one page file — so it can
//     render the page chrome plus the page body without {{define}} collisions
//     between pages.
//   - partials: every _*.html parsed together, for HTMX fragment renders.
//   - loginTmpl: login.html alone (unauthenticated page, no nav).
type templateBundle struct {
	pageSets  map[string]*template.Template
	partials  *template.Template
	loginTmpl *template.Template
}

// parseTemplateBundle reads templates/*.html from the embed.FS and classifies
// each file: layout.html is the base, login.html is special, _*.html are
// partials, and any other file is a page.
func parseTemplateBundle() (*templateBundle, error) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("read templates dir: %w", err)
	}

	var layoutPath, loginPath string
	var partialPaths, pagePaths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		full := path.Join("templates", e.Name())
		switch {
		case e.Name() == "layout.html":
			layoutPath = full
		case e.Name() == "login.html":
			loginPath = full
		case strings.HasPrefix(e.Name(), "_"):
			partialPaths = append(partialPaths, full)
		default:
			pagePaths = append(pagePaths, full)
		}
	}
	if layoutPath == "" {
		return nil, fmt.Errorf("templates: layout.html missing")
	}
	if loginPath == "" {
		return nil, fmt.Errorf("templates: login.html missing")
	}

	b := &templateBundle{
		pageSets: make(map[string]*template.Template, len(pagePaths)),
	}

	// One set per page: layout + all partials + the page itself.
	for _, p := range pagePaths {
		files := append([]string{layoutPath}, partialPaths...)
		files = append(files, p)
		t, err := template.ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", p, err)
		}
		b.pageSets[path.Base(p)] = t
	}

	// Partials only (for HTMX fragment renders).
	if len(partialPaths) > 0 {
		t, err := template.ParseFS(templatesFS, partialPaths...)
		if err != nil {
			return nil, fmt.Errorf("parse partials: %w", err)
		}
		b.partials = t
	} else {
		b.partials = template.New("partials") // empty set, never executed
	}

	// Login template (standalone).
	lt, err := template.ParseFS(templatesFS, loginPath)
	if err != nil {
		return nil, fmt.Errorf("parse login: %w", err)
	}
	b.loginTmpl = lt

	return b, nil
}

// parseTemplates is the legacy flat parser. Removed in a later task once
// every handler renders via render/renderPartial.
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
