package recommend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCentroid(t *testing.T) {
	require.Nil(t, centroid(nil))
	require.Nil(t, centroid([][]float32{}))

	c := centroid([][]float32{{1, 0}, {0, 1}, {1, 0}})
	require.Len(t, c, 2)
	require.InDelta(t, 1.0, float64(c[0]*c[0]+c[1]*c[1]), 1e-5, "normalized")
	require.Greater(t, c[0], c[1])
}

func TestTextQuery(t *testing.T) {
	got := textQuery([]string{
		"Kubernetes networking deep dive",
		"kubernetes operators",
	}, 8)
	require.Equal(t, "kubernetes | networking | deep | dive | operators", got)
}

func TestTextQuery_CapsAndSkipsShort(t *testing.T) {
	got := textQuery([]string{"go a is the"}, 8)
	require.Equal(t, "the", got, "tokens shorter than 3 dropped")
}
