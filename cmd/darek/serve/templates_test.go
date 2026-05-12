package serve

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestParseTemplateBundle(t *testing.T) {
	b, err := parseTemplateBundle()
	if err != nil {
		t.Fatalf("parseTemplateBundle: %v", err)
	}

	wantPages := []string{
		"execution_detail.html",
		"executions_list.html",
		"index.html",
		"whatsapp.html",
	}
	gotPages := make([]string, 0, len(b.pageSets))
	for k := range b.pageSets {
		gotPages = append(gotPages, k)
	}
	sort.Strings(gotPages)
	if len(gotPages) != len(wantPages) {
		t.Fatalf("pageSets keys = %v, want %v", gotPages, wantPages)
	}
	for i, want := range wantPages {
		if gotPages[i] != want {
			t.Errorf("pageSets[%d] = %q, want %q", i, gotPages[i], want)
		}
	}

	wantPartials := []string{
		"_kind.html",
		"_notes.html",
		"_rating.html",
		"_row.html",
		"_tags.html",
		"_whatsapp_group_row.html",
	}
	for name, set := range b.pageSets {
		if set.Lookup("layout") == nil {
			t.Errorf("page %s: missing layout block", name)
		}
		if set.Lookup(name) == nil {
			t.Errorf("page %s: missing self-named template", name)
		}
		for _, p := range wantPartials {
			if set.Lookup(p) == nil {
				t.Errorf("page %s: missing partial %s", name, p)
			}
		}
	}

	if b.partials.Lookup("_row.html") == nil {
		t.Error("partials: missing _row.html")
	}
	if b.partials.Lookup("_kind.html") == nil {
		t.Error("partials: missing _kind.html")
	}
	if b.partials.Lookup("layout") != nil {
		t.Error("partials should NOT include layout")
	}

	if b.loginTmpl.Lookup("login.html") == nil {
		t.Error("loginTmpl missing login.html template")
	}
}

func TestIndexRendersWithChrome(t *testing.T) {
	a, _ := NewAuthConfig("test", []byte("ph"), make([]byte, 32), 0)
	s, err := New(nil, nil, nil, a, nil, nil, "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	rec := httptest.NewRecorder()
	vm := indexVM{
		Page:    s.page("queue", "darek — queue"),
		Path:    "/",
		Kinds:   []string{"article"},
		Ratings: []int{1, 2, 3, 4, 5},
	}
	if err := s.render(rec, "index.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<footer class="app-footer">`,
		`class="brand">darek`,
		`href="/all"`,
		`<title>darek — queue</title>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if !strings.Contains(body, `href="/" class="active"`) {
		t.Errorf("active nav not marked; body:\n%s", body)
	}
}

func TestExecutionsListRendersWithChrome(t *testing.T) {
	a, _ := NewAuthConfig("test", []byte("ph"), make([]byte, 32), 0)
	s, err := New(nil, nil, nil, a, nil, nil, "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	vm := executionsListVM{
		Page:     s.page("executions", "executions · darek"),
		Disabled: true,
	}
	rec := httptest.NewRecorder()
	if err := s.render(rec, "executions_list.html", vm); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<footer class="app-footer">`,
		`href="/all"`,
		`Execution history is disabled`,
		`<title>executions · darek</title>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
