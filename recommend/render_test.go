package recommend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func sample() []Suggestion {
	return []Suggestion{
		{Candidate: Candidate{Title: "Rust ownership", URL: "https://e.com/rust?a=1&b=2", Kind: "article"}, Reason: "matches your systems bent"},
		{Candidate: Candidate{URL: "https://e.com/vid", Kind: "video"}, Reason: "short talk"},
	}
}

func TestRenderText(t *testing.T) {
	require.Empty(t, RenderText(nil))

	out := RenderText(sample())
	require.Contains(t, out, "Reading suggestions")
	require.Contains(t, out, "Rust ownership (article)")
	require.Contains(t, out, "matches your systems bent")
	require.Contains(t, out, "https://e.com/rust?a=1&b=2")
	require.Contains(t, out, "https://e.com/vid (video)")
}

func TestRenderHTML_Escapes(t *testing.T) {
	require.Empty(t, RenderHTML(nil))

	ss := []Suggestion{{Candidate: Candidate{Title: `<script>x</script>`, URL: "https://e.com/?a=1&b=2", Kind: "article"}, Reason: "a & b"}}
	out := RenderHTML(ss)
	require.NotContains(t, out, "<script>")
	require.Contains(t, out, "&lt;script&gt;")
	require.Contains(t, out, "https://e.com/?a=1&amp;b=2")
	require.Contains(t, out, "a &amp; b")
}
