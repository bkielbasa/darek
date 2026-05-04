package youtube

import (
	"os"
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
