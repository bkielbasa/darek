package obs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsInstance_NoError(t *testing.T) {
	m, err := MetricsInstance()
	require.NoError(t, err)
	require.NotNil(t, m.TokensInput)
	require.NotNil(t, m.LLMCostUSD)
	require.NotNil(t, m.TurnDuration)
}
