package links

import (
	"strings"
	"testing"
)

func TestTruncURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "https://example.com/a", "https://example.com/a"},
		{"exactly 256", strings.Repeat("a", 256), strings.Repeat("a", 256)},
		{"257 chars", strings.Repeat("a", 257), strings.Repeat("a", 256)},
		{"way over", strings.Repeat("x", 1024), strings.Repeat("x", 256)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncURL(tc.in)
			if got != tc.want {
				t.Errorf("truncURL(len=%d) = %q (len=%d), want %q (len=%d)",
					len(tc.in), got, len(got), tc.want, len(tc.want))
			}
		})
	}
}
