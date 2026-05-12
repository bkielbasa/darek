package serve

import "testing"

func TestKindColor_KnownPrefixIsStable(t *testing.T) {
	a := kindColor("todoist.fetch")
	b := kindColor("todoist.upsert")
	if a != b {
		t.Errorf("same prefix should produce same color: %q vs %q", a, b)
	}
}

func TestKindColor_DistinctKnownPrefixesAreDistinct(t *testing.T) {
	got := map[string]string{
		"todoist":  kindColor("todoist.fetch"),
		"freshrss": kindColor("freshrss.import"),
		"openai":   kindColor("openai.chat"),
	}
	seen := map[string]string{}
	for prefix, color := range got {
		if other, dup := seen[color]; dup {
			t.Errorf("prefix %q and %q both produced color %q", prefix, other, color)
		}
		seen[color] = prefix
	}
}

func TestKindColor_OpenAIAndLLMShareColor(t *testing.T) {
	if kindColor("openai.chat") != kindColor("llm.summarize") {
		t.Error("openai and llm should share a palette index")
	}
}

func TestKindColor_NoDotUsesWholeStringAsKey(t *testing.T) {
	a := kindColor("sync")
	b := kindColor("sync")
	if a == "" {
		t.Error("expected non-empty color")
	}
	if a != b {
		t.Errorf("not stable: %q vs %q", a, b)
	}
}

func TestKindColor_UnknownPrefixIsStableViaHash(t *testing.T) {
	a := kindColor("mailgun.send")
	b := kindColor("mailgun.send")
	if a != b {
		t.Errorf("not stable for unknown: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("expected non-empty color for unknown")
	}
}

func TestFormatMS(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0ms"},
		{1, "1ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{59999, "60.0s"},
		{60000, "1m 0s"},
		{61000, "1m 1s"},
		{90000, "1m 30s"},
		{3600000, "60m 0s"},
	}
	for _, tc := range tests {
		if got := formatMS(tc.ms); got != tc.want {
			t.Errorf("formatMS(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
