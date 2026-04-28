package llm

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCost_GPT41(t *testing.T) {
	got := Cost("gpt-4.1", 1_000_000, 1_000_000, 0)
	require.InDelta(t, 10.00, got, 1e-9)
}

func TestCost_CachedInputDiscount(t *testing.T) {
	got := Cost("gpt-4.1", 1_000_000, 0, 1_000_000)
	require.InDelta(t, 0.50, got, 1e-9)
}

func TestCost_UnknownModelZero(t *testing.T) {
	require.Equal(t, 0.0, Cost("unknown-model", 1_000_000, 1_000_000, 0))
}

func TestCost_PartialCached(t *testing.T) {
	// 100k cached + 900k uncached input + 0 output, gpt-4.1
	got := Cost("gpt-4.1", 1_000_000, 0, 100_000)
	want := (900_000*2.00 + 100_000*0.50) / 1_000_000.0
	require.True(t, math.Abs(got-want) < 1e-9)
}
