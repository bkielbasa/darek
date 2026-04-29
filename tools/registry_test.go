package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeTool struct {
	name   string
	desc   string
	schema string
	exec   func(ctx context.Context, args json.RawMessage) (string, error)
}

func (f fakeTool) Name() string                                                   { return f.name }
func (f fakeTool) Description() string                                            { return f.desc }
func (f fakeTool) JSONSchema() json.RawMessage                                    { return json.RawMessage(f.schema) }
func (f fakeTool) Execute(ctx context.Context, a json.RawMessage) (string, error) { return f.exec(ctx, a) }

func TestRegistry_RegisterAndExecute(t *testing.T) {
	r, err := NewRegistry(2 * time.Second)
	require.NoError(t, err)
	require.NoError(t, r.Register(fakeTool{
		name: "echo", desc: "echo args", schema: `{"type":"object"}`,
		exec: func(_ context.Context, a json.RawMessage) (string, error) { return string(a), nil },
	}))
	out, err := r.Execute(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	require.NoError(t, err)
	require.Equal(t, `{"x":1}`, out)
}

func TestRegistry_Unknown(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	_, err := r.Execute(context.Background(), "nope", nil)
	require.ErrorIs(t, err, ErrUnknownTool)
}

func TestRegistry_DuplicateRegisterErrors(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	tool := fakeTool{name: "x", desc: "", schema: `{}`, exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }}
	require.NoError(t, r.Register(tool))
	require.Error(t, r.Register(tool))
}

func TestRegistry_TruncatesLongResults(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	big := strings.Repeat("a", MaxResultChars+1000)
	require.NoError(t, r.Register(fakeTool{
		name: "big", desc: "", schema: `{}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return big, nil },
	}))
	out, err := r.Execute(context.Background(), "big", nil)
	require.NoError(t, err)
	require.Less(t, len(out), len(big))
	require.Contains(t, out, "[truncated by darek")
}

func TestRegistry_PropagatesToolError(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	want := errors.New("boom")
	require.NoError(t, r.Register(fakeTool{
		name: "boom", desc: "", schema: `{}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", want },
	}))
	_, err := r.Execute(context.Background(), "boom", nil)
	require.ErrorIs(t, err, want)
}

func TestOpenAIToolDefs_Shape(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	require.NoError(t, r.Register(fakeTool{
		name: "f", desc: "d", schema: `{"type":"object","properties":{"q":{"type":"string"}}}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", nil },
	}))
	defs := r.OpenAIToolDefs()
	require.Len(t, defs, 1)
	require.Equal(t, "function", defs[0]["type"])
	fn := defs[0]["function"].(map[string]any)
	require.Equal(t, "f", fn["name"])
	require.Equal(t, "d", fn["description"])
	require.NotNil(t, fn["parameters"])
}
