//go:build integration

package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTool_Query(t *testing.T) {
	s, ctx := newMemStore(t)
	_, err := s.SaveMemories(ctx, []Candidate{
		{Kind: "agreement", Summary: "Berlin trip in May", DedupeKey: "berlin"},
		{Kind: "person_fact", Summary: "Anna likes tea", People: []string{"Anna"}, DedupeKey: "anna-tea"},
	}, "G", []string{"x"})
	require.NoError(t, err)

	out, err := ListTool{Store: s}.Execute(ctx, []byte(`{"query":"Berlin"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Berlin trip in May")

	out, err = ListTool{Store: s}.Execute(ctx, []byte(`{"kind":"person_fact","status":"active"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Anna likes tea")

	out, err = ListTool{Store: s}.Execute(ctx, []byte(`{"query":"nothing here"}`))
	require.NoError(t, err)
	require.Contains(t, out, "no matching memories")
}
