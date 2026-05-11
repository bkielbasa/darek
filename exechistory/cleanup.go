package exechistory

import (
	"context"
	"log/slog"
	"time"

	"darek/config"
	"darek/db"
)

// RunCleanup periodically deletes executions older than cfg.Retention.
// Blocks until ctx is canceled. Errors are logged and the loop continues.
func RunCleanup(ctx context.Context, pool *db.Pool, cfg config.ExecutionHistory, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(cfg.CleanupPeriod)
	defer t.Stop()
	// Initial pass on start.
	if deleted, err := runCleanupOnce(ctx, pool, cfg.Retention); err != nil {
		log.Warn("exechistory cleanup", "err", err)
	} else if deleted > 0 {
		log.Info("exechistory cleanup", "deleted", deleted)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deleted, err := runCleanupOnce(ctx, pool, cfg.Retention)
			if err != nil {
				log.Warn("exechistory cleanup", "err", err)
				continue
			}
			if deleted > 0 {
				log.Info("exechistory cleanup", "deleted", deleted)
			}
		}
	}
}

func runCleanupOnce(ctx context.Context, pool *db.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM executions WHERE started_at < now() - $1::interval`,
		retention.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
