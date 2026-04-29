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

func TestMetricsInstance_HasNewInstruments(t *testing.T) {
	m, err := MetricsInstance()
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if m.DepRequests == nil {
		t.Error("DepRequests not initialized")
	}
	if m.DepLatency == nil {
		t.Error("DepLatency not initialized")
	}
	if m.AgentMaxItersHit == nil {
		t.Error("AgentMaxItersHit not initialized")
	}
	if m.MailEnvelopesSynced == nil {
		t.Error("MailEnvelopesSynced not initialized")
	}
	if m.MailBodiesFetched == nil {
		t.Error("MailBodiesFetched not initialized")
	}
	if m.MailAttachmentsFetched == nil {
		t.Error("MailAttachmentsFetched not initialized")
	}
	if m.MailSent == nil {
		t.Error("MailSent not initialized")
	}
	if m.MemoryNotesSaved == nil {
		t.Error("MemoryNotesSaved not initialized")
	}
	if m.MemoryNotesRecalled == nil {
		t.Error("MemoryNotesRecalled not initialized")
	}
	if m.LinksEvents == nil {
		t.Error("LinksEvents not initialized")
	}
}
