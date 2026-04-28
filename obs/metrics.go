package obs

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	TokensInput   metric.Int64Counter
	TokensOutput  metric.Int64Counter
	TokensCached  metric.Int64Counter
	LLMLatency    metric.Float64Histogram
	LLMCostUSD    metric.Float64Counter
	ToolCalls     metric.Int64Counter
	ToolLatency   metric.Float64Histogram
	TurnDuration  metric.Float64Histogram
	TurnIters     metric.Int64Histogram
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
	metricsErr  error
)

func MetricsInstance() (*Metrics, error) {
	metricsOnce.Do(func() {
		m := otel.Meter("darek")
		mk := func(err *error) func(c metric.Int64Counter, e error) metric.Int64Counter {
			return func(c metric.Int64Counter, e error) metric.Int64Counter {
				if e != nil && *err == nil {
					*err = e
				}
				return c
			}
		}
		var err error
		i64 := mk(&err)
		f64hist := func(c metric.Float64Histogram, e error) metric.Float64Histogram {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		i64hist := func(c metric.Int64Histogram, e error) metric.Int64Histogram {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		f64ctr := func(c metric.Float64Counter, e error) metric.Float64Counter {
			if e != nil && err == nil {
				err = e
			}
			return c
		}
		metricsInst = &Metrics{
			TokensInput:  i64(m.Int64Counter("darek.tokens.input")),
			TokensOutput: i64(m.Int64Counter("darek.tokens.output")),
			TokensCached: i64(m.Int64Counter("darek.tokens.cached")),
			LLMLatency:   f64hist(m.Float64Histogram("darek.llm.latency", metric.WithUnit("s"))),
			LLMCostUSD:   f64ctr(m.Float64Counter("darek.llm.cost_usd", metric.WithUnit("USD"))),
			ToolCalls:    i64(m.Int64Counter("darek.tool.calls")),
			ToolLatency:  f64hist(m.Float64Histogram("darek.tool.latency", metric.WithUnit("s"))),
			TurnDuration: f64hist(m.Float64Histogram("darek.turn.duration", metric.WithUnit("s"))),
			TurnIters:    i64hist(m.Int64Histogram("darek.turn.iterations")),
		}
		metricsErr = err
	})
	return metricsInst, metricsErr
}
