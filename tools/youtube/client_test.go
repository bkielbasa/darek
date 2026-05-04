package youtube

import (
	"os"
	"testing"

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
