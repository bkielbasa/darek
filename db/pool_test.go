//go:build integration

package db_test

import (
	"context"
	"testing"
	"time"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestPool_QueryRowExecBegin(t *testing.T) {
	dsn, _ := pg.Start(t)
	pool, err := db.Open(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// QueryRow
	var got int
	require.NoError(t, pool.QueryRow(ctx, "SELECT 1").Scan(&got))
	require.Equal(t, 1, got)

	// Exec
	tag, err := pool.Exec(ctx, "CREATE TEMP TABLE t (id int)")
	require.NoError(t, err)
	_ = tag

	// Begin (records tx_begin)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	// Query (multi-row)
	rows, err := pool.Query(ctx, "SELECT generate_series(1, 3)")
	require.NoError(t, err)
	count := 0
	for rows.Next() {
		count++
	}
	rows.Close()
	require.Equal(t, 3, count)
}
