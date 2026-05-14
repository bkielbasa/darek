package serve

import "context"

// NavItem is one entry in the primary navigation. Enabled, if non-nil, gates
// whether the item is shown — used to hide menu entries whose feature isn't
// wired on this server.
type NavItem struct {
	Key     string
	Label   string
	Path    string
	Enabled func(*Server) bool
}

// navItems is the single source of truth for the primary nav. Add a new
// top-level page by appending one entry here.
var navItems = []NavItem{
	{Key: "queue", Label: "queue", Path: "/"},
	{Key: "archive", Label: "archive", Path: "/all"},
	{Key: "whatsapp", Label: "whatsapp", Path: "/whatsapp",
		Enabled: func(s *Server) bool { return s.whatsApp != nil }},
	{Key: "executions", Label: "executions", Path: "/executions",
		Enabled: func(s *Server) bool { return s.executions != nil }},
}

// Page is the chrome-side view-model carried by every full-page render.
// Pages embed this as a named field (vm.Page = ...) so templates can read
// it as .Page.Nav / .Page.ActiveKey / .Page.Footer.
type Page struct {
	Title     string
	ActiveKey string
	Nav       []NavItem
	Footer    FooterInfo
	Subject   string
}

// FooterInfo is the data the layout's footer renders. Populated by
// (*Server).footerInfo().
type FooterInfo struct {
	Brand    string
	Version  string
	LastSync string
}

// filterNavItems returns the subset of items whose Enabled func is nil or
// returns true for s.
func filterNavItems(items []NavItem, s *Server) []NavItem {
	out := make([]NavItem, 0, len(items))
	for _, n := range items {
		if n.Enabled == nil || n.Enabled(s) {
			out = append(out, n)
		}
	}
	return out
}

// enabledNav returns the nav slice already filtered for this server. The
// filter is computed once in serve.New (s.enabledNavItems) so renders don't
// re-evaluate gating closures on every request.
func (s *Server) enabledNav() []NavItem {
	return s.enabledNavItems
}

// page assembles the chrome VM for a render. activeKey must match a NavItem.Key.
func (s *Server) page(ctx context.Context, activeKey, title string) Page {
	return Page{
		Title:     title,
		ActiveKey: activeKey,
		Nav:       s.enabledNav(),
		Footer:    s.footerInfo(),
		Subject:   SubjectFromContext(ctx),
	}
}
