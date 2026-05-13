package links

import "go.opentelemetry.io/otel"

// tracer is the package-private OpenTelemetry tracer for span emission.
// Tracer name starts with "darek/" so exechistory.Recorder picks up its
// spans automatically.
var tracer = otel.Tracer("darek/links")

// truncURL caps a URL at 256 bytes for use as a span attribute, so a
// runaway-long URL can't blow up the per-span attribute payload. Darek's
// sources emit ASCII URLs in practice, so byte truncation is safe; if
// multi-byte UTF-8 mid-rune slicing becomes a concern, switch to
// utf8.RuneCountInString without changing call sites.
func truncURL(s string) string {
	const max = 256
	if len(s) <= max {
		return s
	}
	return s[:max]
}
