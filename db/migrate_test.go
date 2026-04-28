//go:build integration

package db

import (
	"context"
	"testing"

	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestMigrate_CreatesNotesAndTurns(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, Migrate(context.Background(), pool))

	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('notes','turns','messages')`,
	).Scan(&n))
	require.Equal(t, 3, n)
}

func TestMigrate_Idempotent(t *testing.T) {
	_, pool := pg.Start(t)
	ctx := context.Background()
	require.NoError(t, Migrate(ctx, pool))
	require.NoError(t, Migrate(ctx, pool))
}
