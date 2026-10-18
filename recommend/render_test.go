package recommend

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func sample() []Suggestion {
	return []Suggestion{
		{Candidate: Candidate{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Title: "Rust ownership", URL: "https://e.com/rust?a=1&b=2", Kind: "article"}, Reason: "matches your systems bent"},
		{Candidate: Candidate{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), URL: "https://e.com/vid", Kind: "video"}, Reason: "short talk"},
	}
}

func TestRenderText(t *testing.T) {
	require.Empty(t, RenderText(nil, ""))

	out := RenderText(sample(), "")
	require.Contains(t, out, "Reading suggestions")
	require.Contains(t, out, "Rust ownership (article)")
	require.Contains(t, out, "matches your systems bent")
	require.Contains(t, out, "https://e.com/rust?a=1&b=2")
	require.Contains(t, out, "https://e.com/vid (video)")
	require.NotContains(t, out, "Rate:", "no rate base → no rating links")
}

func TestRenderText_RatingLinks(t *testing.T) {
	out := RenderText(sample(), "https://darek.example.com/")
	require.Contains(t, out, "Rate:")
	require.Contains(t, out, "1 https://darek.example.com/r/11111111-1111-1111-1111-111111111111/1")
	require.Contains(t, out, "5 https://darek.example.com/r/11111111-1111-1111-1111-111111111111/5")
	require.Contains(t, out, "3 https://darek.example.com/r/22222222-2222-2222-2222-222222222222/3")
}

func TestRenderText_RatingLinksSkippedWithoutID(t *testing.T) {
	ss := []Suggestion{{Candidate: Candidate{Title: "No id", URL: "https://e.com/x", Kind: "article"}}}
	require.NotContains(t, RenderText(ss, "https://darek.example.com"), "Rate:")
}

func TestRenderHTML_Escapes(t *testing.T) {
	require.Empty(t, RenderHTML(nil, ""))

	ss := []Suggestion{{Candidate: Candidate{Title: `<script>x</script>`, URL: "https://e.com/?a=1&b=2", Kind: "article"}, Reason: "a & b"}}
	out := RenderHTML(ss, "")
	require.NotContains(t, out, "<script>")
	require.Contains(t, out, "&lt;script&gt;")
	require.Contains(t, out, "https://e.com/?a=1&amp;b=2")
	require.Contains(t, out, "a &amp; b")
	require.NotContains(t, out, "Rate:", "no rate base → no rating links")
}

func TestRenderHTML_Stars(t *testing.T) {
	out := RenderHTML(sample(), "https://darek.example.com")
	require.Equal(t, 10, strings.Count(out, ">★</a>"), "5 stars per suggestion")
	for n := 1; n <= 5; n++ {
		require.Contains(t, out, `href="https://darek.example.com/r/11111111-1111-1111-1111-111111111111/`+strconv.Itoa(n)+`"`)
	}
	require.Contains(t, out, starColor)
}

func TestRenderHTML_StarsEscapedBase(t *testing.T) {
	ss := sample()[:1]
	out := RenderHTML(ss, `https://darek.example.com/?a=1&b=2`)
	require.Contains(t, out, "https://darek.example.com/?a=1&amp;b=2/r/11111111-1111-1111-1111-111111111111/1")
	require.NotContains(t, out, `?a=1&b=2/r`)
}

func TestRenderHTML_StarsSkippedWithoutID(t *testing.T) {
	ss := []Suggestion{{Candidate: Candidate{Title: "No id", URL: "https://e.com/x", Kind: "article"}}}
	require.NotContains(t, RenderHTML(ss, "https://darek.example.com"), "Rate:")
}
