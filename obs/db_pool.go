package obs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// PoolStatProvider is anything that exposes a *pgxpool.Stat. *db.Pool satisfies it.
type PoolStatProvider interface {
	Stat() *pgxpool.Stat
}

// RegisterPoolGauges registers async gauges that observe pool stats on every
// metric collection. Returns an error if the gauges or callback can't be
// created. Safe to call once per pool; calling multiple times for the same
// pool will register duplicate callbacks.
func RegisterPoolGauges(p PoolStatProvider) error {
	m := otel.Meter("darek")
	acquired, err := m.Int64ObservableGauge("darek.db.pool.acquired")
	if err != nil {
		return fmt.Errorf("acquired gauge: %w", err)
	}
	idle, err := m.Int64ObservableGauge("darek.db.pool.idle")
	if err != nil {
		return fmt.Errorf("idle gauge: %w", err)
	}
	total, err := m.Int64ObservableGauge("darek.db.pool.total")
	if err != nil {
		return fmt.Errorf("total gauge: %w", err)
	}
	_, err = m.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			s := p.Stat()
			o.ObserveInt64(acquired, int64(s.AcquiredConns()))
			o.ObserveInt64(idle, int64(s.IdleConns()))
			o.ObserveInt64(total, int64(s.TotalConns()))
			return nil
		},
		acquired, idle, total,
	)
	if err != nil {
		return fmt.Errorf("register callback: %w", err)
	}
	return nil
}
