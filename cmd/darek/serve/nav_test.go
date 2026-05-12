package serve

import "testing"

func TestEnabledNavItems_FiltersByEnabledFn(t *testing.T) {
	items := []NavItem{
		{Key: "a", Label: "A", Path: "/a"},
		{Key: "b", Label: "B", Path: "/b", Enabled: func(*Server) bool { return false }},
		{Key: "c", Label: "C", Path: "/c", Enabled: func(*Server) bool { return true }},
	}
	got := filterNavItems(items, nil)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Key != "a" || got[1].Key != "c" {
		t.Errorf("got keys = [%s %s], want [a c]", got[0].Key, got[1].Key)
	}
}

func TestDefaultNavItems_ContainsExpectedKeys(t *testing.T) {
	keys := map[string]bool{}
	for _, n := range navItems {
		keys[n.Key] = true
	}
	for _, want := range []string{"queue", "archive", "whatsapp", "executions"} {
		if !keys[want] {
			t.Errorf("navItems missing key %q", want)
		}
	}
}
