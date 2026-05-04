package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExtractVideoID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{"watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"watch with extra params", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLxxx&t=42", "dQw4w9WgXcQ", false},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"youtu.be with timestamp", "https://youtu.be/dQw4w9WgXcQ?t=42", "dQw4w9WgXcQ", false},
		{"shorts", "https://www.youtube.com/shorts/abcDEF12345", "abcDEF12345", false},
		{"embed", "https://www.youtube.com/embed/abcDEF12345?rel=0", "abcDEF12345", false},
		{"http scheme", "http://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"no scheme", "youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"not youtube", "https://example.com/watch?v=dQw4w9WgXcQ", "", true},
		{"garbage", "not a url", "", true},
		{"empty", "", "", true},
		{"watch missing v", "https://www.youtube.com/watch?list=PLxxx", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractVideoID(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParsePlayerResponse_Happy(t *testing.T) {
	b, err := os.ReadFile("testdata/watch_with_captions.html")
	require.NoError(t, err)

	pr, err := parsePlayerResponse(string(b))
	require.NoError(t, err)
	require.Equal(t, "Test Video", pr.VideoDetails.Title)
	require.Equal(t, "Test Channel", pr.VideoDetails.Author)
	require.Equal(t, "433", pr.VideoDetails.LengthSeconds)
	require.Len(t, pr.Captions.Tracklist.CaptionTracks, 2)
	require.Equal(t, "en", pr.Captions.Tracklist.CaptionTracks[0].LanguageCode)
	require.Equal(t, "", pr.Captions.Tracklist.CaptionTracks[0].Kind)
	require.Equal(t, "asr", pr.Captions.Tracklist.CaptionTracks[1].Kind)
}

func TestParsePlayerResponse_Private(t *testing.T) {
	b, err := os.ReadFile("testdata/watch_private.html")
	require.NoError(t, err)

	_, err = parsePlayerResponse(string(b))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not accessible")
}

func TestPickTrack(t *testing.T) {
	en := captionTrack{LanguageCode: "en"}
	enAuto := captionTrack{LanguageCode: "en", Kind: "asr"}
	es := captionTrack{LanguageCode: "es"}
	fr := captionTrack{LanguageCode: "fr"}

	cases := []struct {
		name    string
		tracks  []captionTrack
		lang    string
		want    captionTrack
		wantErr string // substring match
	}{
		{"empty", nil, "", captionTrack{}, "no captions available"},
		{"empty with lang", nil, "en", captionTrack{}, "no captions available"},
		{"manual en preferred over auto", []captionTrack{enAuto, en}, "", en, ""},
		{"only auto en", []captionTrack{enAuto}, "", enAuto, ""},
		{"first when no en", []captionTrack{fr, es}, "", fr, ""},
		{"explicit es", []captionTrack{en, es}, "es", es, ""},
		{"explicit en exact", []captionTrack{enAuto, en}, "en", enAuto, ""}, // first match wins on explicit
		{"explicit missing", []captionTrack{en}, "fr", captionTrack{}, `language "fr" not available; have: en`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickTrack(tc.tracks, tc.lang)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseJSON3(t *testing.T) {
	b, err := os.ReadFile("testdata/transcript.json3")
	require.NoError(t, err)

	got, err := parseJSON3(b)
	require.NoError(t, err)
	require.Equal(t, "Hello, world. This is a test. Multiple spaces.", got)
}

func TestParseJSON3_Empty(t *testing.T) {
	got, err := parseJSON3([]byte(`{"events":[]}`))
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestParseJSON3_Bad(t *testing.T) {
	_, err := parseJSON3([]byte(`{not json`))
	require.Error(t, err)
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{7*time.Minute + 13*time.Second, "7m 13s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{1 * time.Hour, "1h 00m 00s"},
		{1*time.Hour + 42*time.Minute + 9*time.Second, "1h 42m 09s"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			require.Equal(t, tc.want, formatDuration(tc.in))
		})
	}
}

// newFakeYouTube serves /watch from a fixture HTML, rewriting embedded
// caption baseUrl hosts to point at the test server, and serves /timedtext
// from the JSON3 fixture. Returns the server (caller must Close).
//
// The fixtures hard-code https://example.invalid as the caption host. This
// helper substitutes the running httptest server's URL at request time, so
// the watch-page response points the client back at the same fake server
// for the transcript fetch.
func newFakeYouTube(t *testing.T, watchFixture string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			b, err := os.ReadFile("testdata/" + watchFixture)
			require.NoError(t, err)
			body := strings.ReplaceAll(string(b), "https://example.invalid", srv.URL)
			_, _ = w.Write([]byte(body))
		case "/timedtext":
			require.Equal(t, "json3", r.URL.Query().Get("fmt"))
			b, err := os.ReadFile("testdata/transcript.json3")
			require.NoError(t, err)
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	})
	srv.Start()
	return srv
}

func TestFetch_HappyPath(t *testing.T) {
	srv := newFakeYouTube(t, "watch_with_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	res, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.NoError(t, err)
	require.Equal(t, "Test Video", res.Title)
	require.Equal(t, "Test Channel", res.Channel)
	require.Equal(t, 433*time.Second, res.Duration)
	require.Equal(t, "Hello, world. This is a test. Multiple spaces.", res.Text)
}

func TestFetch_NoCaptions(t *testing.T) {
	srv := newFakeYouTube(t, "watch_no_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no captions available")
}

func TestFetch_VideoNotAccessible(t *testing.T) {
	srv := newFakeYouTube(t, "watch_private.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not accessible")
}

func TestFetch_LanguageNotAvailable(t *testing.T) {
	srv := newFakeYouTube(t, "watch_with_captions.html")
	defer srv.Close()

	c := NewClient(srv.Client())
	c.base = srv.URL

	_, err := c.Fetch(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "fr")
	require.Error(t, err)
	require.Contains(t, err.Error(), `language "fr" not available`)
}

func TestFetch_BadURL(t *testing.T) {
	c := NewClient(nil)
	_, err := c.Fetch(context.Background(), "https://example.com/foo", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid YouTube URL")
}
