package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRenderText_Followups(t *testing.T) {
	due := time.Date(2026, 10, 18, 19, 0, 0, 0, time.Local)
	v := FollowupView{
		Items: []Memory{
			{Kind: "event", Summary: "Dinner with Marek", DueAt: &due, Groups: []string{"Family"}},
			{Kind: "agreement", Summary: "Send the deploy plan", DueAt: &due, Groups: []string{"Work"}},
		},
	}
	got := RenderText(v)
	require.Contains(t, got, "Follow-ups")
	require.Contains(t, got, "Dinner with Marek (Family)")
	require.Contains(t, got, "Send the deploy plan (Work)")
	require.NotContains(t, got, "People")
}

func TestRenderText_Empty(t *testing.T) {
	require.Equal(t, "", RenderText(FollowupView{}))
}

func TestRenderText_Overdue(t *testing.T) {
	past := time.Now().Add(-72 * time.Hour)
	v := FollowupView{Items: []Memory{{Kind: "event", Summary: "Pay the invoice", DueAt: &past}}}
	require.Contains(t, RenderText(v), "overdue since")
}

func TestRenderHTML_EscapesAndEmpty(t *testing.T) {
	require.Equal(t, "", RenderHTML(FollowupView{}))
	v := FollowupView{Items: []Memory{{Kind: "event", Summary: `<script>alert(1)</script>`}}}
	got := RenderHTML(v)
	require.NotContains(t, got, "<script>")
	require.Contains(t, got, "&lt;script&gt;")
	require.NotContains(t, got, "People")
}
