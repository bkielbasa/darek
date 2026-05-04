package youtube

import (
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
