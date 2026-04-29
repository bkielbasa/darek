package obs_test

import (
	"context"
	"testing"

	"darek/obs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// allowedDeps + allowedOps form the closed enum of label values for darek.dep.*
// metrics. When new dep/op pairs are wired in (e.g. when Tasks 12/13 land new
// Todoist/FreshRSS ops), update both maps — this test will fail otherwise.
var allowedDeps = map[string]struct{}{
	"openai_chat":     {},
	"google_calendar": {},
	"todoist":         {},
	"freshrss":        {},
	"ical":            {},
	"imap":            {},
	"smtp":            {},
	"postgres":        {},
}

var allowedOps = map[string]map[string]struct{}{
	"openai_chat":     {"chat": {}},
	"google_calendar": {"list_events": {}},
	"todoist":         {"list_tasks": {}, "create_task": {}, "complete_task": {}, "update_task": {}},
	"freshrss":        {"login": {}, "list": {}, "get": {}, "mark": {}, "token": {}},
	"ical":            {"fetch": {}},
	"imap":            {"sync_folder": {}, "fetch_body": {}, "fetch_attachment": {}, "append": {}},
	"smtp":            {"send": {}},
	"postgres":        {"query": {}, "exec": {}, "tx_begin": {}},
}

var allowedAttrKeys = map[string]struct{}{"dep": {}, "op": {}, "outcome": {}}

func TestDep_OnlyAllowedLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	obs.ResetMetricsForTest()

	for dep, ops := range allowedOps {
		for op := range ops {
			_ = obs.Dep(context.Background(), dep, op, func(ctx context.Context) error { return nil })
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "darek.dep.requests" && m.Name != "darek.dep.latency" {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					checkAttrs(t, dp.Attributes.ToSlice(), m.Name)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					checkAttrs(t, dp.Attributes.ToSlice(), m.Name)
				}
			}
		}
	}
}

func checkAttrs(t *testing.T, attrs []attribute.KeyValue, metricName string) {
	t.Helper()
	values := map[string]string{}
	for _, kv := range attrs {
		key := string(kv.Key)
		if _, ok := allowedAttrKeys[key]; !ok {
			t.Errorf("%s: unexpected label %q (only dep/op/outcome allowed)", metricName, key)
			continue
		}
		values[key] = kv.Value.AsString()
	}
	dep, op, outcome := values["dep"], values["op"], values["outcome"]
	if _, ok := allowedDeps[dep]; !ok {
		t.Errorf("%s: unknown dep %q (update allowedDeps in cardinality_test.go)", metricName, dep)
	}
	if ops, ok := allowedOps[dep]; ok {
		if _, ok := ops[op]; !ok {
			t.Errorf("%s: unknown op %q for dep %q (update allowedOps in cardinality_test.go)", metricName, op, dep)
		}
	}
	if outcome != "ok" && outcome != "error" {
		t.Errorf("%s: outcome must be ok|error, got %q", metricName, outcome)
	}
}
