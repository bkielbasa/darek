package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_Minimal(t *testing.T) {
	t.Setenv("DAREK_POSTGRES_URL", "postgres://localhost/darek")
	t.Setenv("DAREK_OPENAI_API_KEY", "sk-test")

	cfg, err := Load(filepath.Join("testdata", "minimal.yaml"))
	require.NoError(t, err)
	require.Equal(t, "gpt-4.1", cfg.OpenAI.Model)
	require.Equal(t, "darek", cfg.OTEL.ServiceName)
	require.Equal(t, 10, cfg.Agent.MaxIterations)
	require.Equal(t, 60*time.Second, cfg.Agent.LLMTimeout)
}

func TestLoad_RequiresPostgresURL(t *testing.T) {
	t.Setenv("DAREK_OPENAI_API_KEY", "sk-test")
	os.Unsetenv("DAREK_POSTGRES_URL")
	_, err := Load(filepath.Join("testdata", "minimal.yaml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "DAREK_POSTGRES_URL")
}
