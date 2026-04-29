package obs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedact_BearerHeader(t *testing.T) {
	in := "Authorization: Bearer abcDEF12345_xyz"
	require.NotContains(t, Redact(in), "abcDEF12345_xyz")
	require.Contains(t, Redact(in), "[REDACTED]")
}

func TestRedact_OpenAIKey(t *testing.T) {
	in := "key=sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	require.NotContains(t, Redact(in), "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890")
}

func TestRedact_JWT(t *testing.T) {
	in := "tok eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.S3cretSig"
	require.NotContains(t, Redact(in), "S3cretSig")
}

func TestRedact_PassThrough(t *testing.T) {
	in := "the quick brown fox"
	require.Equal(t, in, Redact(in))
}
