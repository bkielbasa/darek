package obs

import (
	"fmt"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
)

// StartRuntime registers Go runtime instrumentation (goroutines, GC, heap)
// against the global meter provider. Safe to call after obs.Init has set
// the meter provider.
func StartRuntime() error {
	if err := otelruntime.Start(); err != nil {
		return fmt.Errorf("runtime instrumentation: %w", err)
	}
	return nil
}
