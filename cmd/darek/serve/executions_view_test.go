package serve

import (
	"testing"
	"time"

	"darek/exechistory"

	"github.com/google/uuid"
)

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

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func TestBuildStepVMs_OffsetAndWidth(t *testing.T) {
	start := mustParse(t, "2026-05-12T10:00:00Z")
	exec := exechistory.Execution{
		SpanID:     "root",
		StartedAt:  start,
		EndedAt:    start.Add(1000 * time.Millisecond),
		DurationMS: 1000,
	}
	steps := []exechistory.Step{
		{SpanID: "a", ParentSpanID: "root", Name: "todoist.fetch",
			StartedAt: start, EndedAt: start.Add(300 * time.Millisecond), DurationMS: 300, Status: "ok"},
		{SpanID: "b", ParentSpanID: "root", Name: "freshrss.import",
			StartedAt: start.Add(300 * time.Millisecond), EndedAt: start.Add(700 * time.Millisecond), DurationMS: 400, Status: "ok"},
		{SpanID: "c", ParentSpanID: "root", Name: "openai.chat",
			StartedAt: start.Add(700 * time.Millisecond), EndedAt: start.Add(1000 * time.Millisecond), DurationMS: 300, Status: "error", Error: "boom"},
	}

	vms := buildStepVMs(exec, steps)
	if len(vms) != 3 {
		t.Fatalf("len(vms) = %d, want 3", len(vms))
	}
	cases := []struct {
		name             string
		wantOffsetMS     int64
		wantOffsetTenths int
		wantWidthTenths  int
		wantError        bool
	}{
		{"todoist.fetch", 0, 0, 300, false},
		{"freshrss.import", 300, 300, 400, false},
		{"openai.chat", 700, 700, 300, true},
	}
	for i, c := range cases {
		got := vms[i]
		if got.Name != c.name {
			t.Errorf("[%d] Name = %q, want %q", i, got.Name, c.name)
		}
		if got.OffsetMS != c.wantOffsetMS {
			t.Errorf("[%d %s] OffsetMS = %d, want %d", i, c.name, got.OffsetMS, c.wantOffsetMS)
		}
		if got.OffsetPct != c.wantOffsetTenths {
			t.Errorf("[%d %s] OffsetPct = %d, want %d", i, c.name, got.OffsetPct, c.wantOffsetTenths)
		}
		if got.WidthPct != c.wantWidthTenths {
			t.Errorf("[%d %s] WidthPct = %d, want %d", i, c.name, got.WidthPct, c.wantWidthTenths)
		}
		if got.IsError != c.wantError {
			t.Errorf("[%d %s] IsError = %v, want %v", i, c.name, got.IsError, c.wantError)
		}
		if got.Color == "" {
			t.Errorf("[%d %s] Color is empty", i, c.name)
		}
	}
}

func TestBuildStepVMs_ZeroDurationDoesNotPanic(t *testing.T) {
	start := mustParse(t, "2026-05-12T10:00:00Z")
	exec := exechistory.Execution{SpanID: "root", StartedAt: start, EndedAt: start, DurationMS: 0}
	steps := []exechistory.Step{
		{SpanID: "a", ParentSpanID: "root", Name: "x", StartedAt: start, EndedAt: start, DurationMS: 0, Status: "ok"},
	}
	vms := buildStepVMs(exec, steps)
	if len(vms) != 1 {
		t.Fatalf("len = %d, want 1", len(vms))
	}
	if vms[0].WidthPct != 0 || vms[0].OffsetPct != 0 {
		t.Errorf("zero-duration: got offset=%d width=%d, want 0/0", vms[0].OffsetPct, vms[0].WidthPct)
	}
}

func TestBuildStepVMs_NegativeOffsetIsClamped(t *testing.T) {
	start := mustParse(t, "2026-05-12T10:00:00Z")
	exec := exechistory.Execution{SpanID: "root", StartedAt: start, EndedAt: start.Add(1000 * time.Millisecond), DurationMS: 1000}
	steps := []exechistory.Step{
		{SpanID: "a", ParentSpanID: "root", Name: "x",
			StartedAt: start.Add(-100 * time.Millisecond), EndedAt: start.Add(50 * time.Millisecond), DurationMS: 150, Status: "ok"},
	}
	vms := buildStepVMs(exec, steps)
	// Negative offset clamps to 0; the bar's width remains the step's
	// scaled duration. For a 150ms step against a 1000ms execution, that's
	// 150 tenths-of-a-percent.
	if vms[0].OffsetPct != 0 {
		t.Errorf("OffsetPct = %d, want 0 (clamped)", vms[0].OffsetPct)
	}
	if vms[0].WidthPct != 150 {
		t.Errorf("WidthPct = %d, want 150", vms[0].WidthPct)
	}
	if vms[0].OffsetMS != 0 {
		t.Errorf("OffsetMS = %d, want 0 (clamped — tooltip shows 0 not negative)", vms[0].OffsetMS)
	}
}

func TestBuildStepVMs_WidthClampedToLane(t *testing.T) {
	start := mustParse(t, "2026-05-12T10:00:00Z")
	exec := exechistory.Execution{SpanID: "root", StartedAt: start, EndedAt: start.Add(1000 * time.Millisecond), DurationMS: 1000}
	steps := []exechistory.Step{
		{SpanID: "a", ParentSpanID: "root", Name: "x",
			StartedAt: start.Add(800 * time.Millisecond), EndedAt: start.Add(1500 * time.Millisecond), DurationMS: 700, Status: "ok"},
	}
	vms := buildStepVMs(exec, steps)
	// Step ends past the execution: OffsetPct stays at the real start,
	// WidthPct is clamped so OffsetPct+WidthPct == 1000 (full remaining lane).
	if vms[0].OffsetPct != 800 {
		t.Errorf("OffsetPct = %d, want 800", vms[0].OffsetPct)
	}
	if vms[0].WidthPct != 200 {
		t.Errorf("WidthPct = %d, want 200 (clamped from 700)", vms[0].WidthPct)
	}
}

func TestBuildTicks_FivePoints(t *testing.T) {
	ticks := buildTicks(1000)
	if len(ticks) != 5 {
		t.Fatalf("len(ticks) = %d, want 5", len(ticks))
	}
	wantPcts := []int{0, 250, 500, 750, 1000}
	wantLabels := []string{"0ms", "250ms", "500ms", "750ms", "1.0s"}
	wantLefts := []string{"0%", "25%", "50%", "75%", "100%"}
	for i, tk := range ticks {
		if tk.Pct != wantPcts[i] {
			t.Errorf("ticks[%d].Pct = %d, want %d", i, tk.Pct, wantPcts[i])
		}
		if tk.Label != wantLabels[i] {
			t.Errorf("ticks[%d].Label = %q, want %q", i, tk.Label, wantLabels[i])
		}
		if tk.Left != wantLefts[i] {
			t.Errorf("ticks[%d].Left = %q, want %q", i, tk.Left, wantLefts[i])
		}
	}
}

func TestBuildTicks_ZeroDuration(t *testing.T) {
	ticks := buildTicks(0)
	if len(ticks) != 5 {
		t.Fatalf("len(ticks) = %d, want 5", len(ticks))
	}
	for i, tk := range ticks {
		if tk.Label != "0ms" {
			t.Errorf("ticks[%d].Label = %q, want \"0ms\"", i, tk.Label)
		}
	}
}

func TestBuildExecutionRowVMs_WidthScaledByMax(t *testing.T) {
	rows := []exechistory.Execution{
		{ID: uuid.New(), Kind: "sync", Name: "n1", StartedAt: time.Now(), DurationMS: 100, Status: "ok"},
		{ID: uuid.New(), Kind: "sync", Name: "n2", StartedAt: time.Now(), DurationMS: 400, Status: "ok"},
	}
	vms, max := buildExecutionRowVMs(rows)
	if max != 400 {
		t.Errorf("max = %d, want 400", max)
	}
	if vms[0].WidthPct != 250 {
		t.Errorf("rows[0].WidthPct = %d, want 250", vms[0].WidthPct)
	}
	if vms[1].WidthPct != 1000 {
		t.Errorf("rows[1].WidthPct = %d, want 1000", vms[1].WidthPct)
	}
	if vms[0].Color == "" {
		t.Error("rows[0].Color is empty")
	}
}

func TestBuildExecutionRowVMs_EmptyHasZeroMax(t *testing.T) {
	vms, max := buildExecutionRowVMs(nil)
	if max != 0 {
		t.Errorf("max = %d, want 0", max)
	}
	if len(vms) != 0 {
		t.Errorf("len(vms) = %d, want 0", len(vms))
	}
}

func TestBuildExecutionRowVMs_AllZeroDurationsRenderNoWidth(t *testing.T) {
	rows := []exechistory.Execution{
		{ID: uuid.New(), Kind: "sync", Name: "n", StartedAt: time.Now(), DurationMS: 0, Status: "ok"},
		{ID: uuid.New(), Kind: "sync", Name: "n", StartedAt: time.Now(), DurationMS: 0, Status: "ok"},
	}
	vms, max := buildExecutionRowVMs(rows)
	if max != 0 {
		t.Errorf("max = %d, want 0", max)
	}
	for i, vm := range vms {
		if vm.WidthPct != 0 {
			t.Errorf("rows[%d].WidthPct = %d, want 0", i, vm.WidthPct)
		}
	}
}

func TestBuildExecutionRowVMs_ErrorStatus(t *testing.T) {
	rows := []exechistory.Execution{
		{ID: uuid.New(), Kind: "sync", Name: "n", StartedAt: time.Now(), DurationMS: 100, Status: "error"},
	}
	vms, _ := buildExecutionRowVMs(rows)
	if !vms[0].IsError {
		t.Error("IsError = false, want true")
	}
}

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		usd  float64
		want string
	}{
		{0.0, "$0.0000"},
		{0.0001, "$0.0001"},
		{0.0123, "$0.0123"},
		{0.99999, "$1.0000"}, // %.4f rounds up to 1.0000
		{1.0, "$1.00"},
		{12.5, "$12.50"},
		{100.499, "$100.50"},
	}
	for _, tc := range tests {
		if got := formatUSD(tc.usd); got != tc.want {
			t.Errorf("formatUSD(%g) = %q, want %q", tc.usd, got, tc.want)
		}
	}
}

func TestFormatTokensLine(t *testing.T) {
	if got := formatTokensLine(100, 50, 0); got != "100 in · 50 out" {
		t.Errorf("got %q, want \"100 in · 50 out\"", got)
	}
	if got := formatTokensLine(100, 50, 10); got != "100 in · 50 out · 10 cached" {
		t.Errorf("got %q, want \"100 in · 50 out · 10 cached\"", got)
	}
	if got := formatTokensLine(0, 0, 0); got != "0 in · 0 out" {
		t.Errorf("zero case: got %q", got)
	}
}

func TestBuildExecutionRowVMs_CostString(t *testing.T) {
	rows := []exechistory.Execution{
		{ID: uuid.New(), Kind: "sync", DurationMS: 100, Status: "ok", TotalCostUSD: 0},
		{ID: uuid.New(), Kind: "sync", DurationMS: 100, Status: "ok", TotalCostUSD: 0.0123},
	}
	vms, _ := buildExecutionRowVMs(rows)
	if vms[0].CostUSD != "" {
		t.Errorf("rows[0].CostUSD = %q, want empty", vms[0].CostUSD)
	}
	if vms[1].CostUSD != "$0.0123" {
		t.Errorf("rows[1].CostUSD = %q, want $0.0123", vms[1].CostUSD)
	}
}
