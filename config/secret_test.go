package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveSecret_FromEnv(t *testing.T) {
	t.Setenv("FOO_TOKEN", "abc123")
	v, err := ResolveSecret("env:FOO_TOKEN")
	require.NoError(t, err)
	require.Equal(t, "abc123", v)
}

func TestResolveSecret_BareNameMeansEnv(t *testing.T) {
	t.Setenv("FOO_TOKEN", "xyz")
	v, err := ResolveSecret("FOO_TOKEN")
	require.NoError(t, err)
	require.Equal(t, "xyz", v)
}

func TestResolveSecret_MissingEnv(t *testing.T) {
	_, err := ResolveSecret("env:UNSET_FOO_TOKEN")
	require.Error(t, err)
	require.Contains(t, err.Error(), "UNSET_FOO_TOKEN")
}

func TestResolveSecret_UnknownScheme(t *testing.T) {
	_, err := ResolveSecret("file:/etc/secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")
}
