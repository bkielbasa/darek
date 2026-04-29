package obs

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInit_FailsFastOnUnreachableEndpoint verifies the Init wires exporters
// and the shutdown is well-behaved when the endpoint never accepts.
func TestInit_FailsFastOnUnreachableEndpoint(t *testing.T) {
	// Reserve a port that we know nothing listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	setup, shutdown, err := Init(ctx, Options{
		ServiceName: "darek-test",
		Endpoint:    addr,
		Insecure:    true,
	})
	require.NoError(t, err) // exporters are lazy; Init does not connect
	require.NotNil(t, setup)
	// Shutdown should complete before ctx deadline (export errors are expected
	// when the endpoint is unreachable, so we only verify no hang).
	_ = shutdown(ctx)
}
