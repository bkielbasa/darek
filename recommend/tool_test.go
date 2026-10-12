package recommend

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTool_NameAndSchema(t *testing.T) {
	tr := NewTool(nil, nil, Options{})
	require.Equal(t, "links.recommend", tr.Name())
	require.NotEmpty(t, tr.Description())

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tr.JSONSchema(), &schema))
	require.Equal(t, "object", schema["type"])
}

func TestTool_NotConfigured(t *testing.T) {
	tr := NewTool(nil, nil, Options{})
	_, err := tr.Execute(context.Background(), nil)
	require.Error(t, err)
}
