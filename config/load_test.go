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

func TestLoad_BlogMarketing_Valid(t *testing.T) {
	t.Setenv("X", "test")
	t.Setenv("K", "test")
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(`
postgres:
  url_env: X
openai:
  api_key_env: K
  model: gpt-4.1
otel:
  service_name: t
  exporter_endpoint: localhost:4317
auth:
  username_env: U
  password_hash_env: H
  session_key_env: S
blog_marketing:
  feed_url: https://blog.example.com/feed.xml
  project_name: Marketing
  sync_interval: 15m
  post_time: "09:00"
  timezone: Europe/Warsaw
`), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "Marketing", cfg.BlogMarketing.ProjectName)
	require.Equal(t, "09:00", cfg.BlogMarketing.PostTime)
}

func TestLoad_BlogMarketing_BadTime(t *testing.T) {
	t.Setenv("X", "test")
	t.Setenv("K", "test")
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(`
postgres: {url_env: X}
openai: {api_key_env: K, model: gpt-4.1}
otel: {service_name: t, exporter_endpoint: localhost:4317}
auth: {username_env: U, password_hash_env: H, session_key_env: S}
blog_marketing:
  feed_url: https://blog.example.com/feed.xml
  project_name: Marketing
  post_time: "9am"
`), 0o600))
	_, err := Load(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "post_time")
}

func TestLoad_BlogMarketing_BadTimezone(t *testing.T) {
	t.Setenv("X", "test")
	t.Setenv("K", "test")
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(`
postgres: {url_env: X}
openai: {api_key_env: K, model: gpt-4.1}
otel: {service_name: t, exporter_endpoint: localhost:4317}
auth: {username_env: U, password_hash_env: H, session_key_env: S}
blog_marketing:
  feed_url: https://blog.example.com/feed.xml
  project_name: Marketing
  post_time: "09:00"
  timezone: Mars/Olympus_Mons
`), 0o600))
	_, err := Load(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timezone")
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "darek-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

func TestExecutionHistoryDefaults(t *testing.T) {
	t.Setenv("OAI", "x")
	t.Setenv("PG", "postgres://x")
	path := writeTempConfig(t, `
openai: {model: m, api_key_env: OAI}
postgres: {url_env: PG}
execution_history: {enabled: true}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionHistory.Retention != 720*time.Hour {
		t.Errorf("retention default: got %v want 720h", cfg.ExecutionHistory.Retention)
	}
	if cfg.ExecutionHistory.CleanupPeriod != 24*time.Hour {
		t.Errorf("cleanup_period default: got %v want 24h", cfg.ExecutionHistory.CleanupPeriod)
	}
}

func TestExecutionHistoryDisabledByDefault(t *testing.T) {
	t.Setenv("OAI", "x")
	t.Setenv("PG", "postgres://x")
	path := writeTempConfig(t, `
openai: {model: m, api_key_env: OAI}
postgres: {url_env: PG}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionHistory.Enabled {
		t.Error("enabled should default to false")
	}
}

func TestExecutionHistoryValidation_RetentionMustBePositive(t *testing.T) {
	t.Setenv("OAI", "x")
	t.Setenv("PG", "postgres://x")
	path := writeTempConfig(t, `
openai: {model: m, api_key_env: OAI}
postgres: {url_env: PG}
execution_history:
  enabled: true
  retention: -1h
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for negative retention")
	}
}
