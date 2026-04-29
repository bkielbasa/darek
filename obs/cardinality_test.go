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

// allowedDeps is the closed set of dep names. Any new dep must be added here
// AND have an entry in allowedOps.
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
	for _, kv := range attrs {
		key := string(kv.Key)
		if _, ok := allowedAttrKeys[key]; !ok {
			t.Errorf("%s: unexpected label %q (only dep/op/outcome allowed)", metricName, key)
			continue
		}
		v := kv.Value.AsString()
		switch key {
		case "dep":
			if _, ok := allowedDeps[v]; !ok {
				t.Errorf("%s: unknown dep %q", metricName, v)
			}
		case "outcome":
			if v != "ok" && v != "error" {
				t.Errorf("%s: outcome must be ok|error, got %q", metricName, v)
			}
		}
	}
}
