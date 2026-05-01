package digest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWindow_UTC(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 30, 0, 0, time.UTC)
	from, to := Window(now)
	require.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), from)
	require.Equal(t, time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), to)
}

func TestWindow_OffsetTZ(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 5, 1, 23, 30, 0, 0, loc)
	from, to := Window(now)
	require.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, loc), from)
	require.Equal(t, time.Date(2026, 5, 4, 0, 0, 0, 0, loc), to)
}
