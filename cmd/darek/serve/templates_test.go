package serve

import (
	"sort"
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

	for name, set := range b.pageSets {
		if set.Lookup("layout") == nil {
			t.Errorf("page %s: missing layout block", name)
		}
		if set.Lookup(name) == nil {
			t.Errorf("page %s: missing self-named template", name)
		}
		if set.Lookup("_row.html") == nil {
			t.Errorf("page %s: missing _row.html partial", name)
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

	if b.loginTmpl == nil {
		t.Fatal("loginTmpl is nil")
	}
}
