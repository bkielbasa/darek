//go:build integration

package memory

import (
	"context"
	"encoding/json"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestSaveTool_AndRecallTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(db.Wrap(pool))
	ctx := context.Background()

	out, err := SaveTool{Store: s}.Execute(ctx, json.RawMessage(`{"body":"prefer concise replies","tags":["style"]}`))
	require.NoError(t, err)
	require.Contains(t, out, "saved note")

	out, err = RecallTool{Store: s}.Execute(ctx, json.RawMessage(`{"query":"concise"}`))
	require.NoError(t, err)
	require.Contains(t, out, "concise replies")
}
