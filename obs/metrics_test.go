package obs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsInstance_NoError(t *testing.T) {
	ResetMetricsForTest()
	m, err := MetricsInstance()
	require.NoError(t, err)
	require.NotNil(t, m.TokensInput)
	require.NotNil(t, m.LLMCostUSD)
	require.NotNil(t, m.TurnDuration)
}

func TestMetricsInstance_HasNewInstruments(t *testing.T) {
	ResetMetricsForTest()
	m, err := MetricsInstance()
	require.NoError(t, err)
	require.NotNil(t, m.DepRequests, "DepRequests not initialized")
	require.NotNil(t, m.DepLatency, "DepLatency not initialized")
	require.NotNil(t, m.AgentMaxItersHit, "AgentMaxItersHit not initialized")
	require.NotNil(t, m.MailEnvelopesSynced, "MailEnvelopesSynced not initialized")
	require.NotNil(t, m.MailBodiesFetched, "MailBodiesFetched not initialized")
	require.NotNil(t, m.MailAttachmentsFetched, "MailAttachmentsFetched not initialized")
	require.NotNil(t, m.MailSent, "MailSent not initialized")
	require.NotNil(t, m.MemoryNotesSaved, "MemoryNotesSaved not initialized")
	require.NotNil(t, m.MemoryNotesRecalled, "MemoryNotesRecalled not initialized")
	require.NotNil(t, m.LinksEvents, "LinksEvents not initialized")
	require.NotNil(t, m.LinksIngest, "LinksIngest not initialized")
	require.NotNil(t, m.FreshRSSSyncDuration, "FreshRSSSyncDuration not initialized")
	require.NotNil(t, m.LinksAnalyze, "LinksAnalyze not initialized")
}
