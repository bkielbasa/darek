# Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the spine of `darek` — a Go CLI that holds an agent loop talking to OpenAI, with Postgres-backed cross-session memory and full OpenTelemetry observability (traces, token/cost metrics, structured logs). Later plans add Calendars, Todoist, and Mail behind the same agent/tool/observability seams.

**Architecture:** Flat Go layout. `cmd/darek` wires `agent`, `llm`, `tools`, `memory`, `obs`, `db`, `config`. Agent loop is hand-rolled, calls OpenAI Chat Completions with tool definitions, dispatches tool calls through a registry. Postgres holds notes (memory) + turns/messages (debug + token accounting). All work emits OTLP to a local OTEL Collector → Jaeger / Prometheus / Grafana stack, both runnable via docker-compose.

**Tech Stack:** Go 1.22, `pgx/v5`, `golang-migrate/migrate/v4` (file driver), official `github.com/openai/openai-go`, `go.opentelemetry.io/otel` (+ `otelslog`, `otelpgx`), `gopkg.in/yaml.v3`, `stretchr/testify`, `testcontainers/testcontainers-go`. Docker Compose for Postgres + observability stack.

**Out of scope for this plan:** integrations (Todoist, calendars, mail), pgvector embeddings (notes use tsvector only here), keyring secret backend, service mode, mail tables/columns. Those land in later plans.

---

## File Map

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Module declaration, dependencies. Module name: `darek`. |
| `Makefile` | `make test`, `make build`, `make up`, `make down`, `make obs-up`, `make obs-down`. |
| `.gitignore` | Standard Go + `.darek/` workspace + `secrets.env`. |
| `README.md` | Quickstart: clone → up → migrate → doctor → chat. |
| `docker-compose.yml` | Just Postgres for app data. |
| `docker-compose.observability.yml` | OTEL Collector + Jaeger + Prometheus + Grafana with provisioning. |
| `otel/collector.yaml` | Collector pipelines: OTLP in → Jaeger (traces) + Prom (metrics) + stdout (logs). |
| `otel/prometheus.yml` | Prom scrape config for Collector's `/metrics`. |
| `otel/grafana/datasources.yml` | Grafana datasource provisioning. |
| `otel/grafana/dashboards.yml` | Grafana dashboard provisioning manifest. |
| `otel/grafana/dashboards/agent_turns.json` | Dashboard. |
| `otel/grafana/dashboards/tokens_and_cost.json` | Dashboard. |
| `otel/grafana/dashboards/tool_latency.json` | Dashboard. |
| `config/types.go` | `Config` struct + sub-structs. |
| `config/load.go` | YAML load + env override + validate. |
| `config/load_test.go` | Tests. |
| `config/secret.go` | `ResolveSecret(ref string) (string, error)`. |
| `db/pool.go` | `Open(ctx, dsn) (*pgxpool.Pool, error)` with `otelpgx`. |
| `db/migrate.go` | Embed `migrations/*.sql`, run forward-only. |
| `db/migrate_test.go` | Integration test (testcontainers). |
| `db/migrations/0001_initial.up.sql` | `notes`, `turns`, `messages`. |
| `obs/otel.go` | Tracer, meter, OTLP exporter, slog handler init + shutdown. |
| `obs/otel_test.go` | Integration test that spans actually emit. |
| `obs/metrics.go` | Metric instrument constructors used everywhere. |
| `obs/redact.go` | Redactor for token-shape strings. |
| `obs/redact_test.go` | Unit tests. |
| `obs/logger.go` | `slog.Logger` with redactor + trace-id injection. |
| `llm/cost.go` | Per-model price table + `Cost(model, in, out)`. |
| `llm/cost_test.go` | Unit tests. |
| `llm/client.go` | OpenAI wrapper: retry, span, metrics, cost. |
| `llm/client_test.go` | Tests with `httptest.Server` stub OpenAI. |
| `tools/registry.go` | `Tool` interface, `Registry`, OpenAI-format tool defs. |
| `tools/registry_test.go` | Unit tests. |
| `memory/store.go` | Postgres-backed note CRUD (insert + tsvector recall). |
| `memory/store_test.go` | Integration test (testcontainers). |
| `memory/tools.go` | `RecallTool`, `SaveTool` implementing `tools.Tool`. |
| `memory/tools_test.go` | Tests. |
| `agent/prompt.go` | System prompt builder. |
| `agent/agent.go` | The loop: build messages → call LLM → dispatch tools → repeat. |
| `agent/agent_test.go` | Loop tests with stub LLM scripts. |
| `cmd/darek/main.go` | Subcommand dispatch (default chat / `migrate` / `doctor`). |
| `cmd/darek/chat.go` | Default chat command. |
| `cmd/darek/doctor.go` | Health check. |
| `internal/testutil/llmstub/server.go` | Scripted OpenAI HTTP server for tests. |
| `internal/testutil/pg/pg.go` | testcontainers helper. |

**Boundary rules to preserve:** `agent` imports only `llm`, `tools`, `memory`, `obs`. `tools/*` is the only place that imports integration code (none in this plan). `llm` is the only place that imports `openai-go`. `cmd/darek` is the only place that wires concrete tools into the registry.

---

## Conventions

- Module name: `darek`. Go 1.22.
- Errors: wrap with `fmt.Errorf("doing X: %w", err)`. No `errors.Wrap`. No `panic` outside `main`.
- Logging: `slog`, never `log` or `fmt.Println` for production paths. Test code may use `t.Log`.
- Tests: `testify/require` for fail-fast assertions; integration tests behind `//go:build integration` build tag and run with `make test-integration`.
- Commits: Conventional-Commit-ish prefixes (`feat:`, `fix:`, `chore:`, `test:`, `docs:`). Commit at the end of every task. No squashing within a task.

---

## Task 1 — Repo bootstrap

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `README.md`

- [ ] **Step 1: Initialize Go module**

```bash
go mod init darek
```

Expected: creates `go.mod` with `module darek` and `go 1.22`. If `go 1.22` is missing, append it.

- [ ] **Step 2: Write `.gitignore`**

```gitignore
# Build
/darek
/dist/

# Local config & secrets
secrets.env
.darek/
*.local.yaml

# Test artifacts
*.out
coverage.txt

# Editor
.idea/
.vscode/
*.swp

# OS
.DS_Store
```

- [ ] **Step 3: Write minimal `Makefile`**

```makefile
.PHONY: build test test-integration up down obs-up obs-down lint

GO ?= go
BIN ?= darek

build:
	$(GO) build -o $(BIN) ./cmd/darek

test:
	$(GO) test ./...

test-integration:
	$(GO) test -tags=integration -count=1 ./...

up:
	docker compose up -d

down:
	docker compose down

obs-up:
	docker compose -f docker-compose.observability.yml up -d

obs-down:
	docker compose -f docker-compose.observability.yml down

lint:
	$(GO) vet ./...
```

- [ ] **Step 4: Write `README.md` skeleton**

```markdown
# darek

Personal-assistant CLI. Talks to OpenAI, remembers things in Postgres, fully observable.

## Quickstart

(populated in Task 13)

## Layout

(populated in Task 13)
```

- [ ] **Step 5: Verify and commit**

```bash
go build ./... 2>&1 || true   # nothing to build yet, must not error on parse
git add .gitignore Makefile README.md go.mod
git commit -m "chore: bootstrap go module and Makefile"
```

Expected: clean commit, no untracked files surprising us.

---

## Task 2 — Config: types, load, validate

**Files:**
- Create: `config/types.go`, `config/load.go`, `config/load_test.go`, `config/testdata/minimal.yaml`

- [ ] **Step 1: Write `config/load_test.go` (RED)**

```go
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
	os.Unsetenv("DAREK_POSTGRES_URL")
	_, err := Load(filepath.Join("testdata", "minimal.yaml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "DAREK_POSTGRES_URL")
}
```

Add `config/testdata/minimal.yaml`:

```yaml
openai:
  model: gpt-4.1
  api_key_env: DAREK_OPENAI_API_KEY

postgres:
  url_env: DAREK_POSTGRES_URL

otel:
  service_name: darek
  exporter_endpoint: localhost:4317
  insecure: true

agent:
  max_iterations: 10
  llm_timeout: 60s
  tool_timeout: 30s
```

- [ ] **Step 2: Run test to confirm RED**

```bash
go test ./config/...
```

Expected: build fails (`Load` undefined). That's the failing test.

- [ ] **Step 3: Implement `config/types.go`**

```go
package config

import "time"

type Config struct {
	OpenAI   OpenAI   `yaml:"openai"`
	Postgres Postgres `yaml:"postgres"`
	OTEL     OTEL     `yaml:"otel"`
	Agent    Agent    `yaml:"agent"`
	Memory   Memory   `yaml:"memory"`
}

type OpenAI struct {
	Model      string `yaml:"model"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
}

type Postgres struct {
	URLEnv string `yaml:"url_env"`
}

type OTEL struct {
	ServiceName      string `yaml:"service_name"`
	ExporterEndpoint string `yaml:"exporter_endpoint"`
	Insecure         bool   `yaml:"insecure"`
}

type Agent struct {
	MaxIterations int           `yaml:"max_iterations"`
	LLMTimeout    time.Duration `yaml:"llm_timeout"`
	ToolTimeout   time.Duration `yaml:"tool_timeout"`
}

type Memory struct {
	Pgvector       bool   `yaml:"pgvector"`
	EmbeddingModel string `yaml:"embedding_model"`
}
```

- [ ] **Step 4: Implement `config/load.go`**

```go
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(c *Config) {
	if c.Agent.MaxIterations == 0 {
		c.Agent.MaxIterations = 10
	}
	if c.Agent.LLMTimeout == 0 {
		c.Agent.LLMTimeout = 60 * time.Second
	}
	if c.Agent.ToolTimeout == 0 {
		c.Agent.ToolTimeout = 30 * time.Second
	}
	if c.OTEL.ServiceName == "" {
		c.OTEL.ServiceName = "darek"
	}
}

func validate(c *Config) error {
	if c.OpenAI.Model == "" {
		return fmt.Errorf("openai.model is required")
	}
	if c.OpenAI.APIKeyEnv == "" {
		return fmt.Errorf("openai.api_key_env is required")
	}
	if os.Getenv(c.OpenAI.APIKeyEnv) == "" {
		return fmt.Errorf("env var %s (openai.api_key_env) is empty", c.OpenAI.APIKeyEnv)
	}
	if c.Postgres.URLEnv == "" {
		return fmt.Errorf("postgres.url_env is required")
	}
	if os.Getenv(c.Postgres.URLEnv) == "" {
		return fmt.Errorf("env var %s (postgres.url_env) is empty", c.Postgres.URLEnv)
	}
	return nil
}
```

- [ ] **Step 5: Add deps and run tests (GREEN)**

```bash
go get gopkg.in/yaml.v3
go get github.com/stretchr/testify/require
go mod tidy
go test ./config/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add config/ go.mod go.sum
git commit -m "feat(config): YAML loader with env-backed required fields"
```

---

## Task 3 — Secret resolver

**Files:**
- Create: `config/secret.go`, `config/secret_test.go`

- [ ] **Step 1: Write `config/secret_test.go` (RED)**

```go
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
```

- [ ] **Step 2: Run test (RED)**

```bash
go test ./config/...
```

Expected: fails — `ResolveSecret` undefined.

- [ ] **Step 3: Implement `config/secret.go`**

```go
package config

import (
	"fmt"
	"os"
	"strings"
)

// ResolveSecret turns a config-space reference into the actual secret value.
// Supported schemes:
//   - "env:NAME"  → value of $NAME
//   - "NAME"      → shorthand for env:NAME
// Reserved for later: "keyring:..." (returns ErrUnsupportedScheme today).
func ResolveSecret(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty secret reference")
	}
	scheme, rest, hasColon := strings.Cut(ref, ":")
	if !hasColon {
		scheme, rest = "env", ref
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("env var %s is empty", rest)
		}
		return v, nil
	default:
		return "", fmt.Errorf("unsupported secret scheme %q (only env:NAME for now)", scheme)
	}
}
```

- [ ] **Step 4: Run tests (GREEN)**

```bash
go test ./config/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/secret.go config/secret_test.go
git commit -m "feat(config): env-backed secret resolver"
```

---

## Task 4 — Postgres compose, pool, migrations, schema

**Files:**
- Create: `docker-compose.yml`, `db/pool.go`, `db/migrate.go`, `db/migrate_test.go`, `migrations/0001_initial.up.sql`, `internal/testutil/pg/pg.go`

- [ ] **Step 1: Write `docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: darek-postgres
    environment:
      POSTGRES_USER: darek
      POSTGRES_PASSWORD: darek
      POSTGRES_DB: darek
    ports:
      - "5432:5432"
    volumes:
      - darek_pg:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "darek"]
      interval: 2s
      timeout: 2s
      retries: 20

volumes:
  darek_pg:
```

- [ ] **Step 2: Write `db/migrations/0001_initial.up.sql`**

(The migrations directory lives under `db/` because `//go:embed` is package-relative.)

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE notes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    body        text NOT NULL,
    tags        text[] NOT NULL DEFAULT '{}',
    source      text NOT NULL DEFAULT 'user',
    search      tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(body, '') || ' ' || coalesce(array_to_string(tags, ' '), ''))
    ) STORED
);
CREATE INDEX notes_search_gin ON notes USING gin(search);
CREATE INDEX notes_tags_gin   ON notes USING gin(tags);

CREATE TABLE turns (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    user_input    text NOT NULL,
    final_output  text,
    trace_id      text,
    iterations    integer NOT NULL DEFAULT 0,
    input_tokens  integer NOT NULL DEFAULT 0,
    output_tokens integer NOT NULL DEFAULT 0,
    cost_usd      numeric(10,6) NOT NULL DEFAULT 0
);
CREATE INDEX turns_started_at ON turns (started_at DESC);

CREATE TABLE messages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id     uuid NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    ord         integer NOT NULL,
    role        text NOT NULL,
    content     text,
    tool_name   text,
    tool_args   jsonb,
    tool_result text,
    UNIQUE (turn_id, ord)
);
CREATE INDEX messages_turn_id ON messages (turn_id);
```

- [ ] **Step 3: Implement `db/pool.go`**

```go
package db

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}
```

- [ ] **Step 4: Implement `db/migrate.go`**

```go
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	driver, err := pgx.WithInstance(conn.Conn().Config(), &pgx.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations sub fs: %w", err)
	}
	src, err := iofs.New(sub, ".")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("migrate new: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
```


- [ ] **Step 5: Write `internal/testutil/pg/pg.go`**

```go
//go:build integration

package pg

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func Start(t *testing.T) (dsn string, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("darek"),
		postgres.WithUsername("darek"),
		postgres.WithPassword("darek"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	dsn, err = c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	pool, err = pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(fmt.Errorf("ping: %w", err))
	}
	return dsn, pool
}
```

- [ ] **Step 6: Write `db/migrate_test.go`**

```go
//go:build integration

package db

import (
	"context"
	"testing"

	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestMigrate_CreatesNotesAndTurns(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, Migrate(context.Background(), pool))

	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('notes','turns','messages')`,
	).Scan(&n))
	require.Equal(t, 3, n)
}

func TestMigrate_Idempotent(t *testing.T) {
	_, pool := pg.Start(t)
	ctx := context.Background()
	require.NoError(t, Migrate(ctx, pool))
	require.NoError(t, Migrate(ctx, pool))
}
```

- [ ] **Step 7: Add deps and run integration tests**

```bash
go get github.com/jackc/pgx/v5
go get github.com/jackc/pgx/v5/pgxpool
go get github.com/exaring/otelpgx
go get github.com/golang-migrate/migrate/v4
go get github.com/testcontainers/testcontainers-go
go get github.com/testcontainers/testcontainers-go/modules/postgres
go mod tidy
make test-integration
```

Expected: both DB tests pass. Docker must be running.

- [ ] **Step 8: Commit**

```bash
git add docker-compose.yml db/ internal/testutil/pg/ go.mod go.sum
git commit -m "feat(db): pgx pool + embedded migrations + initial schema"
```

---

## Task 5 — OTEL setup (traces, metrics, logs)

**Files:**
- Create: `obs/otel.go`, `obs/otel_test.go`

- [ ] **Step 1: Write `obs/otel.go`**

```go
package obs

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Setup struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
}

type Options struct {
	ServiceName string
	Endpoint    string // host:port for OTLP gRPC
	Insecure    bool
}

func Init(ctx context.Context, opt Options) (*Setup, func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(opt.ServiceName)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resource: %w", err)
	}

	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(opt.Endpoint)}
	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(opt.Endpoint)}
	logOpts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(opt.Endpoint)}
	if opt.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
		logOpts = append(logOpts, otlploggrpc.WithInsecure())
	}

	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("metric exporter: %w", err)
	}
	logExp, err := otlploggrpc.New(ctx, logOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("log exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	global.SetLoggerProvider(lp)

	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, fn := range []func(context.Context) error{
			tp.Shutdown, mp.Shutdown, lp.Shutdown,
		} {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("otel shutdown: %v", errs)
		}
		return nil
	}
	return &Setup{TracerProvider: tp, MeterProvider: mp, LoggerProvider: lp}, shutdown, nil
}
```

- [ ] **Step 2: Add deps**

```bash
go get go.opentelemetry.io/otel
go get go.opentelemetry.io/otel/sdk
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc
go get go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc
go get go.opentelemetry.io/otel/log/global
go get go.opentelemetry.io/otel/sdk/log
go mod tidy
```

- [ ] **Step 3: Write `obs/otel_test.go` (smoke test)**

```go
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
	// Shutdown should not hang past ctx deadline.
	require.NoError(t, shutdown(ctx))
}
```

- [ ] **Step 4: Run tests (GREEN)**

```bash
go test ./obs/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add obs/otel.go obs/otel_test.go go.mod go.sum
git commit -m "feat(obs): OTEL traces+metrics+logs init via OTLP/gRPC"
```

---

## Task 6 — Observability stack + redactor + logger

**Files:**
- Create: `docker-compose.observability.yml`, `otel/collector.yaml`, `otel/prometheus.yml`, `otel/grafana/datasources.yml`, `otel/grafana/dashboards.yml`, `otel/grafana/dashboards/*.json` (3), `obs/redact.go`, `obs/redact_test.go`, `obs/logger.go`

- [ ] **Step 1: Write `docker-compose.observability.yml`**

```yaml
services:
  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.103.0
    container_name: darek-otelcol
    command: ["--config=/etc/otelcol/config.yaml"]
    volumes:
      - ./otel/collector.yaml:/etc/otelcol/config.yaml:ro
    ports:
      - "4317:4317"   # OTLP gRPC
      - "8889:8889"   # Prometheus exporter

  jaeger:
    image: jaegertracing/all-in-one:1.57
    container_name: darek-jaeger
    environment:
      COLLECTOR_OTLP_ENABLED: "true"
    ports:
      - "16686:16686" # UI
      - "14317:4317"  # OTLP gRPC (mapped to avoid clash with otelcol)

  prometheus:
    image: prom/prometheus:v2.53.0
    container_name: darek-prometheus
    volumes:
      - ./otel/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana:11.1.0
    container_name: darek-grafana
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: Admin
      GF_SECURITY_ADMIN_PASSWORD: admin
    volumes:
      - ./otel/grafana/datasources.yml:/etc/grafana/provisioning/datasources/datasources.yml:ro
      - ./otel/grafana/dashboards.yml:/etc/grafana/provisioning/dashboards/dashboards.yml:ro
      - ./otel/grafana/dashboards:/var/lib/grafana/dashboards:ro
    ports:
      - "3000:3000"
```

- [ ] **Step 2: Write `otel/collector.yaml`**

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

processors:
  batch: {}

exporters:
  otlp/jaeger:
    endpoint: jaeger:4317
    tls:
      insecure: true
  prometheus:
    endpoint: 0.0.0.0:8889
  debug:
    verbosity: basic

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/jaeger]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [prometheus]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [debug]
```

- [ ] **Step 3: Write `otel/prometheus.yml`**

```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: otel-collector
    static_configs:
      - targets: ["otel-collector:8889"]
```

- [ ] **Step 4: Write `otel/grafana/datasources.yml`**

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
  - name: Jaeger
    type: jaeger
    access: proxy
    url: http://jaeger:16686
```

- [ ] **Step 5: Write `otel/grafana/dashboards.yml`**

```yaml
apiVersion: 1
providers:
  - name: 'darek'
    folder: 'darek'
    type: file
    options:
      path: /var/lib/grafana/dashboards
```

- [ ] **Step 6: Write `otel/grafana/dashboards/agent_turns.json`**

```json
{
  "title": "darek — agent turns",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "stat",
      "title": "Turns/min",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(rate(darek_turn_duration_seconds_count[1m]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Turn duration p50/p95",
      "datasource": "Prometheus",
      "targets": [
        {"expr": "histogram_quantile(0.50, sum by (le) (rate(darek_turn_duration_seconds_bucket[5m])))", "legendFormat": "p50"},
        {"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_turn_duration_seconds_bucket[5m])))", "legendFormat": "p95"}
      ],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 5}
    },
    {
      "type": "timeseries",
      "title": "Iterations per turn",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le) (rate(darek_turn_iterations_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 5}
    }
  ]
}
```

- [ ] **Step 7: Write `otel/grafana/dashboards/tokens_and_cost.json`**

```json
{
  "title": "darek — tokens & cost",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "Input tokens/s by model",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_tokens_input_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Output tokens/s by model",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_tokens_output_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    },
    {
      "type": "stat",
      "title": "USD spent (1h)",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum(increase(darek_llm_cost_usd_total[1h]))"}],
      "gridPos": {"h": 5, "w": 6, "x": 0, "y": 8}
    },
    {
      "type": "timeseries",
      "title": "Cost rate by model",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (model) (rate(darek_llm_cost_usd_total[5m]))"}],
      "gridPos": {"h": 8, "w": 18, "x": 6, "y": 8}
    }
  ]
}
```

- [ ] **Step 8: Write `otel/grafana/dashboards/tool_latency.json`**

```json
{
  "title": "darek — tool latency",
  "schemaVersion": 39,
  "version": 1,
  "panels": [
    {
      "type": "timeseries",
      "title": "Tool calls/s by name",
      "datasource": "Prometheus",
      "targets": [{"expr": "sum by (tool) (rate(darek_tool_calls_total[1m]))"}],
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0}
    },
    {
      "type": "timeseries",
      "title": "Tool latency p95 by name",
      "datasource": "Prometheus",
      "targets": [{"expr": "histogram_quantile(0.95, sum by (le, tool) (rate(darek_tool_latency_seconds_bucket[5m])))"}],
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0}
    }
  ]
}
```

- [ ] **Step 9: Write `obs/redact.go`**

```go
package obs

import "regexp"

var (
	// Bearer / sk- / xoxb- / ghp_ etc. — anything that looks like a credential.
	redactPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]{8,}`),
		regexp.MustCompile(`(?i)\b(sk|xoxb|ghp|ghs|gho|github_pat|api[_-]?key)[-_=:][A-Za-z0-9._\-+/=]{8,}`),
		// JWT-ish: three base64 segments.
		regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`),
	}
)

func Redact(s string) string {
	out := s
	for _, re := range redactPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
```

- [ ] **Step 10: Write `obs/redact_test.go`**

```go
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
```

- [ ] **Step 11: Write `obs/logger.go`**

```go
package obs

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a JSON slog.Logger that:
//   - emits to stdout
//   - mirrors records to OTEL via the global LoggerProvider
//   - injects trace_id/span_id from ctx when called via LoggerCtx
//   - redacts known credential shapes before write
func NewLogger(serviceName string) *slog.Logger {
	stdout := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindString {
				a.Value = slog.StringValue(Redact(a.Value.String()))
			}
			return a
		},
	})
	otelh := otelslog.NewHandler(serviceName)
	return slog.New(multiHandler{stdout, otelh})
}

type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	for _, h := range m {
		if err := h.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
```

- [ ] **Step 12: Add deps and run unit tests**

```bash
go get go.opentelemetry.io/contrib/bridges/otelslog
go mod tidy
go test ./obs/...
```

Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add docker-compose.observability.yml otel/ obs/redact.go obs/redact_test.go obs/logger.go go.mod go.sum
git commit -m "feat(obs): docker-compose stack, dashboards, redactor, slog logger"
```

---

## Task 7 — Metrics + cost calc

**Files:**
- Create: `obs/metrics.go`, `obs/metrics_test.go`, `llm/cost.go`, `llm/cost_test.go`

- [ ] **Step 1: Write `obs/metrics.go`**

```go
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
```

- [ ] **Step 2: Write `obs/metrics_test.go`**

```go
package obs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsInstance_NoError(t *testing.T) {
	m, err := MetricsInstance()
	require.NoError(t, err)
	require.NotNil(t, m.TokensInput)
	require.NotNil(t, m.LLMCostUSD)
	require.NotNil(t, m.TurnDuration)
}
```

- [ ] **Step 3: Write `llm/cost.go`**

```go
package llm

// Prices in USD per 1M tokens. Update alongside model changes.
type modelPrice struct{ inUSDPerM, outUSDPerM, cachedInUSDPerM float64 }

var pricing = map[string]modelPrice{
	"gpt-4.1":         {inUSDPerM: 2.00, outUSDPerM: 8.00, cachedInUSDPerM: 0.50},
	"gpt-4.1-mini":    {inUSDPerM: 0.40, outUSDPerM: 1.60, cachedInUSDPerM: 0.10},
	"gpt-4.1-nano":    {inUSDPerM: 0.10, outUSDPerM: 0.40, cachedInUSDPerM: 0.025},
	// Add models here as they're adopted.
}

// Cost returns USD cost for one chat call. Unknown model → 0 (logged elsewhere).
func Cost(model string, inputTokens, outputTokens, cachedInputTokens int) float64 {
	p, ok := pricing[model]
	if !ok {
		return 0
	}
	billableIn := inputTokens - cachedInputTokens
	if billableIn < 0 {
		billableIn = 0
	}
	return (float64(billableIn)*p.inUSDPerM +
		float64(cachedInputTokens)*p.cachedInUSDPerM +
		float64(outputTokens)*p.outUSDPerM) / 1_000_000.0
}

func KnownModel(model string) bool {
	_, ok := pricing[model]
	return ok
}
```

- [ ] **Step 4: Write `llm/cost_test.go`**

```go
package llm

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCost_GPT41(t *testing.T) {
	got := Cost("gpt-4.1", 1_000_000, 1_000_000, 0)
	require.InDelta(t, 10.00, got, 1e-9)
}

func TestCost_CachedInputDiscount(t *testing.T) {
	got := Cost("gpt-4.1", 1_000_000, 0, 1_000_000)
	require.InDelta(t, 0.50, got, 1e-9)
}

func TestCost_UnknownModelZero(t *testing.T) {
	require.Equal(t, 0.0, Cost("unknown-model", 1_000_000, 1_000_000, 0))
}

func TestCost_PartialCached(t *testing.T) {
	// 100k cached + 900k uncached input + 0 output, gpt-4.1
	got := Cost("gpt-4.1", 1_000_000, 0, 100_000)
	want := (900_000*2.00 + 100_000*0.50) / 1_000_000.0
	require.True(t, math.Abs(got-want) < 1e-9)
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./obs/... ./llm/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add obs/metrics.go obs/metrics_test.go llm/cost.go llm/cost_test.go
git commit -m "feat(obs,llm): metrics instruments and per-model cost calc"
```

---

## Task 8 — OpenAI LLM client

**Files:**
- Create: `llm/client.go`, `llm/client_test.go`, `internal/testutil/llmstub/server.go`

- [ ] **Step 1: Write `internal/testutil/llmstub/server.go`**

```go
package llmstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Reply describes one canned chat-completions response, returned in order of script.
type Reply struct {
	StatusCode int                    // 0 → 200
	Body       map[string]interface{} // raw JSON to return
}

type Server struct {
	*httptest.Server
	mu      sync.Mutex
	script  []Reply
	calls   []map[string]interface{}
}

func New(t *testing.T, script ...Reply) *Server {
	t.Helper()
	s := &Server{script: append([]Reply{}, script...)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Calls() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]interface{}, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.calls = append(s.calls, body)
	if len(s.script) == 0 {
		s.mu.Unlock()
		http.Error(w, "no scripted reply", http.StatusInternalServerError)
		return
	}
	reply := s.script[0]
	s.script = s.script[1:]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if reply.StatusCode != 0 {
		w.WriteHeader(reply.StatusCode)
	}
	_ = json.NewEncoder(w).Encode(reply.Body)
}
```

- [ ] **Step 2: Write `llm/client.go`**

```go
package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"darek/obs"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Client struct {
	c       *openai.Client
	model   string
	timeout time.Duration
	m       *obs.Metrics
	tracer  trace.Tracer
}

type Options struct {
	APIKey  string
	BaseURL string  // optional
	Model   string
	Timeout time.Duration
}

func New(opt Options) (*Client, error) {
	if opt.APIKey == "" {
		return nil, errors.New("api key required")
	}
	if opt.Model == "" {
		return nil, errors.New("model required")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 60 * time.Second
	}
	clientOpts := []option.RequestOption{option.WithAPIKey(opt.APIKey)}
	if opt.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opt.BaseURL))
	}
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &Client{
		c:       openai.NewClient(clientOpts...),
		model:   opt.Model,
		timeout: opt.Timeout,
		m:       m,
		tracer:  otel.Tracer("darek/llm"),
	}, nil
}

// Chat is the only method the agent uses. The agent passes raw OpenAI types so
// it owns prompting, tool-call parsing, and message-history shaping.
func (cl *Client) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	ctx, cancel := context.WithTimeout(ctx, cl.timeout)
	defer cancel()

	ctx, span := cl.tracer.Start(ctx, "chat",
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", "openai"),
			attribute.String("gen_ai.request.model", cl.model),
		),
	)
	defer span.End()

	start := time.Now()
	params.Model = openai.F(cl.model)
	resp, err := cl.c.Chat.Completions.New(ctx, params)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	cl.m.LLMLatency.Record(ctx, dur,
		metric.WithAttributes(attribute.String("model", cl.model), attribute.String("outcome", outcome)),
	)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	in := int(resp.Usage.PromptTokens)
	out := int(resp.Usage.CompletionTokens)
	cached := int(resp.Usage.PromptTokensDetails.CachedTokens)
	cost := Cost(cl.model, in, out, cached)

	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", in),
		attribute.Int("gen_ai.usage.output_tokens", out),
		attribute.Int("darek.tokens.cached", cached),
		attribute.Float64("darek.llm.cost_usd", cost),
	)
	mAttr := metric.WithAttributes(attribute.String("model", cl.model))
	cl.m.TokensInput.Add(ctx, int64(in), mAttr)
	cl.m.TokensOutput.Add(ctx, int64(out), mAttr)
	cl.m.TokensCached.Add(ctx, int64(cached), mAttr)
	cl.m.LLMCostUSD.Add(ctx, cost, mAttr)
	return resp, nil
}

func (cl *Client) Model() string { return cl.model }
```

- [ ] **Step 3: Write `llm/client_test.go`**

```go
package llm

import (
	"context"
	"testing"

	"darek/internal/testutil/llmstub"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/require"
)

func TestChat_HappyPath(t *testing.T) {
	server := llmstub.New(t, llmstub.Reply{
		Body: map[string]interface{}{
			"id":      "chatcmpl-1",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4.1",
			"choices": []map[string]interface{}{{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "hello world",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
				"prompt_tokens_details": map[string]interface{}{"cached_tokens": 0},
			},
		},
	})

	cl, err := New(Options{
		APIKey:  "test",
		BaseURL: server.URL,
		Model:   "gpt-4.1",
	})
	require.NoError(t, err)

	resp, err := cl.Chat(context.Background(), openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hi"),
		}),
	})
	require.NoError(t, err)
	require.Equal(t, "hello world", resp.Choices[0].Message.Content)
	require.Equal(t, int64(10), resp.Usage.PromptTokens)
}

func TestChat_PropagatesError(t *testing.T) {
	server := llmstub.New(t, llmstub.Reply{
		StatusCode: 500,
		Body:       map[string]interface{}{"error": map[string]interface{}{"message": "boom"}},
	})
	cl, err := New(Options{APIKey: "test", BaseURL: server.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	_, err = cl.Chat(context.Background(), openai.ChatCompletionNewParams{
		Messages: openai.F([]openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")}),
	})
	require.Error(t, err)
}
```

- [ ] **Step 4: Add deps and run tests**

```bash
go get github.com/openai/openai-go
go mod tidy
go test ./llm/...
```

Expected: PASS. (The OpenAI SDK has built-in retry on 429/5xx; we get retries for free.)

- [ ] **Step 5: Commit**

```bash
git add llm/client.go llm/client_test.go internal/testutil/llmstub/ go.mod go.sum
git commit -m "feat(llm): OpenAI client wrapper with OTEL spans, token metrics, cost"
```

---

## Task 9 — Tool interface and registry

**Files:**
- Create: `tools/registry.go`, `tools/registry_test.go`

- [ ] **Step 1: Write `tools/registry.go`**

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"darek/obs"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const MaxResultChars = 20_000

var ErrUnknownTool = errors.New("unknown tool")

type Tool interface {
	Name() string
	Description() string
	JSONSchema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	mu     sync.RWMutex
	byName map[string]Tool
	tracer trace.Tracer
	m      *obs.Metrics
	timeout time.Duration
}

func NewRegistry(toolTimeout time.Duration) (*Registry, error) {
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, err
	}
	return &Registry{
		byName:  map[string]Tool{},
		tracer:  otel.Tracer("darek/tools"),
		m:       m,
		timeout: toolTimeout,
	}, nil
}

func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[t.Name()]; ok {
		return fmt.Errorf("tool %q already registered", t.Name())
	}
	r.byName[t.Name()] = t
	return nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// Execute looks up `name`, runs it with timeout, OTEL span, and metrics.
// On tool error, returns the error (caller decides whether to surface to the model).
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	ctx, span := r.tracer.Start(ctx, "tool.execute",
		trace.WithAttributes(
			attribute.String("tool.name", name),
			attribute.Int("tool.args_chars", len(args)),
		),
	)
	defer span.End()

	start := time.Now()
	res, err := t.Execute(ctx, args)
	dur := time.Since(start).Seconds()

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	if len(res) > MaxResultChars {
		res = res[:MaxResultChars] + "\n\n[truncated by darek; original was longer]"
	}
	span.SetAttributes(attribute.Int("tool.result_chars", len(res)))

	attrs := metric.WithAttributes(
		attribute.String("tool", name),
		attribute.String("outcome", outcome),
	)
	r.m.ToolCalls.Add(ctx, 1, attrs)
	r.m.ToolLatency.Record(ctx, dur, metric.WithAttributes(attribute.String("tool", name)))
	return res, err
}

// OpenAIToolDefs returns the tool list in the shape OpenAI Chat Completions wants.
// We keep the raw shape as map[string]any so this package doesn't import openai-go.
func (r *Registry) OpenAIToolDefs() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]map[string]any, 0, len(r.byName))
	for _, t := range r.byName {
		var schema any
		_ = json.Unmarshal(t.JSONSchema(), &schema)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  schema,
			},
		})
	}
	return out
}
```

- [ ] **Step 2: Write `tools/registry_test.go`**

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeTool struct {
	name    string
	desc    string
	schema  string
	exec    func(ctx context.Context, args json.RawMessage) (string, error)
}

func (f fakeTool) Name() string                                                 { return f.name }
func (f fakeTool) Description() string                                          { return f.desc }
func (f fakeTool) JSONSchema() json.RawMessage                                  { return json.RawMessage(f.schema) }
func (f fakeTool) Execute(ctx context.Context, a json.RawMessage) (string, error) { return f.exec(ctx, a) }

func TestRegistry_RegisterAndExecute(t *testing.T) {
	r, err := NewRegistry(2 * time.Second)
	require.NoError(t, err)
	require.NoError(t, r.Register(fakeTool{
		name: "echo", desc: "echo args", schema: `{"type":"object"}`,
		exec: func(_ context.Context, a json.RawMessage) (string, error) { return string(a), nil },
	}))
	out, err := r.Execute(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	require.NoError(t, err)
	require.Equal(t, `{"x":1}`, out)
}

func TestRegistry_Unknown(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	_, err := r.Execute(context.Background(), "nope", nil)
	require.ErrorIs(t, err, ErrUnknownTool)
}

func TestRegistry_DuplicateRegisterErrors(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	tool := fakeTool{name: "x", desc: "", schema: `{}`, exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }}
	require.NoError(t, r.Register(tool))
	require.Error(t, r.Register(tool))
}

func TestRegistry_TruncatesLongResults(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	big := strings.Repeat("a", MaxResultChars+1000)
	require.NoError(t, r.Register(fakeTool{
		name: "big", desc: "", schema: `{}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return big, nil },
	}))
	out, err := r.Execute(context.Background(), "big", nil)
	require.NoError(t, err)
	require.Less(t, len(out), len(big))
	require.Contains(t, out, "[truncated by darek")
}

func TestRegistry_PropagatesToolError(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	want := errors.New("boom")
	require.NoError(t, r.Register(fakeTool{
		name: "boom", desc: "", schema: `{}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", want },
	}))
	_, err := r.Execute(context.Background(), "boom", nil)
	require.ErrorIs(t, err, want)
}

func TestOpenAIToolDefs_Shape(t *testing.T) {
	r, _ := NewRegistry(time.Second)
	require.NoError(t, r.Register(fakeTool{
		name: "f", desc: "d", schema: `{"type":"object","properties":{"q":{"type":"string"}}}`,
		exec: func(_ context.Context, _ json.RawMessage) (string, error) { return "", nil },
	}))
	defs := r.OpenAIToolDefs()
	require.Len(t, defs, 1)
	require.Equal(t, "function", defs[0]["type"])
	fn := defs[0]["function"].(map[string]any)
	require.Equal(t, "f", fn["name"])
	require.Equal(t, "d", fn["description"])
	require.NotNil(t, fn["parameters"])
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./tools/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/registry.go tools/registry_test.go
git commit -m "feat(tools): tool interface, registry, OpenAI tool defs, OTEL"
```

---

## Task 10 — Memory store + tools

**Files:**
- Create: `memory/store.go`, `memory/store_test.go`, `memory/tools.go`, `memory/tools_test.go`

- [ ] **Step 1: Write `memory/store.go`**

```go
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Note struct {
	ID        uuid.UUID
	CreatedAt time.Time
	Body      string
	Tags      []string
	Source    string
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Save(ctx context.Context, body string, tags []string, source string) (uuid.UUID, error) {
	if body == "" {
		return uuid.Nil, fmt.Errorf("body required")
	}
	if source == "" {
		source = "user"
	}
	if tags == nil {
		tags = []string{}
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notes (body, tags, source)
		VALUES ($1, $2, $3) RETURNING id
	`, body, tags, source).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert note: %w", err)
	}
	return id, nil
}

// Recall returns up to limit notes ranked by tsvector match against query.
// Empty query → most recent notes.
func (s *Store) Recall(ctx context.Context, query string, limit int) ([]Note, error) {
	if limit <= 0 {
		limit = 5
	}
	var rows = s.pool.Query
	var (
		out  []Note
		cur  pgxRows
		err  error
	)
	if query == "" {
		cur, err = rows(ctx, `
			SELECT id, created_at, body, tags, source
			FROM notes
			ORDER BY created_at DESC
			LIMIT $1
		`, limit)
	} else {
		cur, err = rows(ctx, `
			SELECT id, created_at, body, tags, source
			FROM notes
			WHERE search @@ plainto_tsquery('simple', $1)
			ORDER BY ts_rank(search, plainto_tsquery('simple', $1)) DESC, created_at DESC
			LIMIT $2
		`, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer cur.Close()
	for cur.Next() {
		var n Note
		if err := cur.Scan(&n.ID, &n.CreatedAt, &n.Body, &n.Tags, &n.Source); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, n)
	}
	return out, cur.Err()
}

// Internal alias to keep the Recall body readable.
type pgxRows = interface {
	Next() bool
	Scan(dst ...any) error
	Close()
	Err() error
}
```

- [ ] **Step 2: Write `memory/store_test.go`**

```go
//go:build integration

package memory

import (
	"context"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestStore_SaveAndRecall(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(pool)
	ctx := context.Background()

	_, err := s.Save(ctx, "I'm tracking a Berlin trip in May", []string{"travel"}, "user")
	require.NoError(t, err)
	_, err = s.Save(ctx, "Birthday dinner reservation 7pm", []string{"family"}, "user")
	require.NoError(t, err)

	got, err := s.Recall(ctx, "Berlin", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Body, "Berlin")
}

func TestStore_RecallEmpty_ReturnsRecent(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(pool)
	ctx := context.Background()
	_, _ = s.Save(ctx, "first", nil, "user")
	_, _ = s.Save(ctx, "second", nil, "user")

	got, err := s.Recall(ctx, "", 5)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "second", got[0].Body)
}
```

- [ ] **Step 3: Write `memory/tools.go`**

```go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type RecallTool struct{ Store *Store }

func (RecallTool) Name() string        { return "memory.recall" }
func (RecallTool) Description() string { return "Recall personal notes saved in earlier conversations. Returns up to N notes." }
func (RecallTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string","description":"keywords to search for; empty for most recent"},
			"limit":{"type":"integer","minimum":1,"maximum":20,"default":5}
		},
		"required":[]
	}`)
}
func (rt RecallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	notes, err := rt.Store.Recall(ctx, p.Query, p.Limit)
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return "no matching notes", nil
	}
	var b strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&b, "[%d] %s — %s\n", i+1, n.CreatedAt.Format("2006-01-02"), n.Body)
		if len(n.Tags) > 0 {
			fmt.Fprintf(&b, "    tags: %s\n", strings.Join(n.Tags, ", "))
		}
	}
	return b.String(), nil
}

type SaveTool struct{ Store *Store }

func (SaveTool) Name() string        { return "memory.save" }
func (SaveTool) Description() string { return "Save a note for future conversations. Use when the user shares a fact you should remember." }
func (SaveTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"body":{"type":"string","description":"the note content"},
			"tags":{"type":"array","items":{"type":"string"},"description":"optional tags"}
		},
		"required":["body"]
	}`)
}
func (st SaveTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Body string   `json:"body"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.Body == "" {
		return "", fmt.Errorf("body required")
	}
	id, err := st.Store.Save(ctx, p.Body, p.Tags, "agent_save")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("saved note %s", id), nil
}
```

- [ ] **Step 4: Write `memory/tools_test.go`**

```go
//go:build integration

package memory

import (
	"context"
	"encoding/json"
	"testing"

	"darek/db"
	"darek/internal/testutil/pg"

	"github.com/stretchr/testify/require"
)

func TestSaveTool_AndRecallTool(t *testing.T) {
	_, pool := pg.Start(t)
	require.NoError(t, db.Migrate(context.Background(), pool))

	s := NewStore(pool)
	ctx := context.Background()

	out, err := SaveTool{Store: s}.Execute(ctx, json.RawMessage(`{"body":"prefer concise replies","tags":["style"]}`))
	require.NoError(t, err)
	require.Contains(t, out, "saved note")

	out, err = RecallTool{Store: s}.Execute(ctx, json.RawMessage(`{"query":"concise"}`))
	require.NoError(t, err)
	require.Contains(t, out, "concise replies")
}
```

- [ ] **Step 5: Run tests**

```bash
go get github.com/google/uuid
go mod tidy
go test ./memory/...
make test-integration
```

Expected: unit tests in memory pkg empty (none non-tagged) → PASS; integration → PASS.

- [ ] **Step 6: Commit**

```bash
git add memory/ go.mod go.sum
git commit -m "feat(memory): notes store, recall+save tools, tsvector ranking"
```

---

## Task 11 — Agent loop

**Files:**
- Create: `agent/prompt.go`, `agent/agent.go`, `agent/agent_test.go`

- [ ] **Step 1: Write `agent/prompt.go`**

```go
package agent

import (
	"fmt"
	"strings"
	"time"
)

func BuildSystemPrompt(today time.Time, toolNames []string) string {
	var sb strings.Builder
	sb.WriteString("You are darek, a personal assistant CLI.\n")
	fmt.Fprintf(&sb, "Today is %s.\n\n", today.Format("2006-01-02 (Monday)"))
	sb.WriteString("Be concise. Prefer plain prose to bullet lists unless listing items.\n")
	sb.WriteString("When the user shares a fact you should remember across sessions, call memory.save.\n")
	sb.WriteString("When recalling personal context, call memory.recall.\n")
	sb.WriteString("\nAvailable tools: ")
	sb.WriteString(strings.Join(toolNames, ", "))
	sb.WriteString(".\n")
	return sb.String()
}
```

- [ ] **Step 2: Write `agent/agent.go`**

```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"darek/llm"
	"darek/obs"
	"darek/tools"

	"github.com/openai/openai-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Agent struct {
	llm           *llm.Client
	tools         *tools.Registry
	maxIters      int
	tracer        trace.Tracer
	m             *obs.Metrics
}

type Options struct {
	LLM           *llm.Client
	Tools         *tools.Registry
	MaxIterations int
}

func New(opt Options) (*Agent, error) {
	if opt.LLM == nil || opt.Tools == nil {
		return nil, errors.New("llm and tools required")
	}
	if opt.MaxIterations <= 0 {
		opt.MaxIterations = 10
	}
	m, err := obs.MetricsInstance()
	if err != nil {
		return nil, err
	}
	return &Agent{
		llm:      opt.LLM,
		tools:    opt.Tools,
		maxIters: opt.MaxIterations,
		tracer:   otel.Tracer("darek/agent"),
		m:        m,
	}, nil
}

type TurnResult struct {
	Output     string
	Iterations int
}

func (a *Agent) RunTurn(ctx context.Context, userInput string) (*TurnResult, error) {
	ctx, span := a.tracer.Start(ctx, "darek.turn",
		trace.WithAttributes(attribute.Int("user_input_chars", len(userInput))),
	)
	defer span.End()
	start := time.Now()

	system := BuildSystemPrompt(time.Now(), a.tools.Names())
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(system),
		openai.UserMessage(userInput),
	}
	toolDefs := buildToolParams(a.tools.OpenAIToolDefs())

	var (
		final string
		iters int
	)
	for iters = 0; iters < a.maxIters; iters++ {
		resp, err := a.llm.Chat(ctx, openai.ChatCompletionNewParams{
			Messages: openai.F(msgs),
			Tools:    openai.F(toolDefs),
		})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("llm: %w", err)
		}
		choice := resp.Choices[0]
		msg := choice.Message
		if len(msg.ToolCalls) == 0 {
			final = msg.Content
			break
		}
		msgs = append(msgs, msg.ToParam())
		for _, tc := range msg.ToolCalls {
			result, err := a.tools.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			payload := result
			if err != nil {
				payload = fmt.Sprintf("error: %s", err.Error())
			}
			msgs = append(msgs, openai.ToolMessage(tc.ID, payload))
		}
	}

	if iters == a.maxIters {
		err := fmt.Errorf("hit max iterations (%d) without final answer", a.maxIters)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	dur := time.Since(start).Seconds()
	a.m.TurnDuration.Record(ctx, dur, metric.WithAttributes(attribute.String("outcome", "ok")))
	a.m.TurnIters.Record(ctx, int64(iters+1))
	span.SetAttributes(attribute.Int("iterations", iters+1))
	return &TurnResult{Output: final, Iterations: iters + 1}, nil
}

// buildToolParams converts the registry's generic tool defs into OpenAI's typed params.
func buildToolParams(defs []map[string]any) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(defs))
	for _, d := range defs {
		fn := d["function"].(map[string]any)
		params := openai.FunctionDefinitionParam{
			Name:        openai.F(fn["name"].(string)),
			Description: openai.F(fn["description"].(string)),
			Parameters:  openai.F(openai.FunctionParameters(fn["parameters"].(map[string]any))),
		}
		out = append(out, openai.ChatCompletionToolParam{
			Type:     openai.F(openai.ChatCompletionToolTypeFunction),
			Function: openai.F(params),
		})
	}
	return out
}
```

- [ ] **Step 3: Write `agent/agent_test.go`**

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darek/internal/testutil/llmstub"
	"darek/llm"
	"darek/tools"

	"github.com/stretchr/testify/require"
)

type stubTool struct {
	name string
	out  string
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub" }
func (s stubTool) JSONSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return s.out, nil
}

func TestAgent_NoToolCalls_ReturnsAnswerInOneIter(t *testing.T) {
	srv := llmstub.New(t, llmstub.Reply{Body: assistantReply("hello there", nil)})
	cl, err := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	reg, _ := tools.NewRegistry(time.Second)
	a, err := New(Options{LLM: cl, Tools: reg, MaxIterations: 5})
	require.NoError(t, err)

	res, err := a.RunTurn(context.Background(), "hi")
	require.NoError(t, err)
	require.Equal(t, "hello there", res.Output)
	require.Equal(t, 1, res.Iterations)
}

func TestAgent_ToolCallThenFinal(t *testing.T) {
	srv := llmstub.New(t,
		llmstub.Reply{Body: assistantReply("", []toolCall{{ID: "c1", Name: "echo", Args: `{"q":"x"}`}})},
		llmstub.Reply{Body: assistantReply("done", nil)},
	)
	cl, err := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	require.NoError(t, err)
	reg, _ := tools.NewRegistry(time.Second)
	require.NoError(t, reg.Register(stubTool{name: "echo", out: "echoed"}))

	a, _ := New(Options{LLM: cl, Tools: reg, MaxIterations: 5})
	res, err := a.RunTurn(context.Background(), "do it")
	require.NoError(t, err)
	require.Equal(t, "done", res.Output)
	require.Equal(t, 2, res.Iterations)
}

func TestAgent_HitsMaxIterations(t *testing.T) {
	// Always returns a tool call → never converges.
	loop := llmstub.Reply{Body: assistantReply("", []toolCall{{ID: "c1", Name: "echo", Args: `{}`}})}
	srv := llmstub.New(t, loop, loop, loop)
	cl, _ := llm.New(llm.Options{APIKey: "k", BaseURL: srv.URL, Model: "gpt-4.1"})
	reg, _ := tools.NewRegistry(time.Second)
	require.NoError(t, reg.Register(stubTool{name: "echo", out: "x"}))

	a, _ := New(Options{LLM: cl, Tools: reg, MaxIterations: 2})
	_, err := a.RunTurn(context.Background(), "loop")
	require.Error(t, err)
	require.Contains(t, err.Error(), "max iterations")
}

// --- helpers ---

type toolCall struct {
	ID, Name, Args string
}

func assistantReply(content string, calls []toolCall) map[string]interface{} {
	msg := map[string]interface{}{"role": "assistant", "content": content}
	if len(calls) > 0 {
		tc := make([]map[string]interface{}, 0, len(calls))
		for _, c := range calls {
			tc = append(tc, map[string]interface{}{
				"id":   c.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      c.Name,
					"arguments": c.Args,
				},
			})
		}
		msg["tool_calls"] = tc
		msg["content"] = ""
	}
	finishReason := "stop"
	if len(calls) > 0 {
		finishReason = "tool_calls"
	}
	return map[string]interface{}{
		"id":      "c-1",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4.1",
		"choices": []map[string]interface{}{{
			"index": 0, "message": msg, "finish_reason": finishReason,
		}},
		"usage": map[string]interface{}{
			"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			"prompt_tokens_details": map[string]interface{}{"cached_tokens": 0},
		},
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./agent/...
```

Expected: all three tests PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/
git commit -m "feat(agent): tool-calling loop with max-iterations cap and OTEL turn span"
```

---

## Task 12 — `cmd/darek` wiring (chat / migrate / doctor)

**Files:**
- Create: `cmd/darek/main.go`, `cmd/darek/chat.go`, `cmd/darek/doctor.go`, `config/testdata/config.example.yaml`

- [ ] **Step 1: Write `cmd/darek/main.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	cfgPath := os.Getenv("DAREK_CONFIG")
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".darek", "config.yaml")
	}

	switch cmd {
	case "migrate":
		return runMigrate(ctx, cfgPath)
	case "doctor":
		return runDoctor(ctx, cfgPath)
	case "", "chat":
		return runChat(ctx, cfgPath, strings.Join(args, " "))
	default:
		return fmt.Errorf("unknown subcommand %q (try: chat, migrate, doctor)", cmd)
	}
}
```

- [ ] **Step 2: Write `cmd/darek/chat.go`**

```go
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"

	"darek/agent"
	"darek/config"
	"darek/db"
	"darek/llm"
	"darek/memory"
	"darek/obs"
	"darek/tools"
)

func runChat(ctx context.Context, cfgPath, userInput string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil {
		return fmt.Errorf("openai key: %w", err)
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return fmt.Errorf("postgres dsn: %w", err)
	}

	_, otelShutdown, err := obs.Init(ctx, obs.Options{
		ServiceName: cfg.OTEL.ServiceName,
		Endpoint:    cfg.OTEL.ExporterEndpoint,
		Insecure:    cfg.OTEL.Insecure,
	})
	if err != nil {
		return fmt.Errorf("otel init: %w", err)
	}
	defer func() { _ = otelShutdown(context.Background()) }()
	logger := obs.NewLogger(cfg.OTEL.ServiceName)

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		return err
	}

	reg, err := tools.NewRegistry(cfg.Agent.ToolTimeout)
	if err != nil {
		return err
	}
	store := memory.NewStore(pool)
	if err := reg.Register(memory.RecallTool{Store: store}); err != nil {
		return err
	}
	if err := reg.Register(memory.SaveTool{Store: store}); err != nil {
		return err
	}

	a, err := agent.New(agent.Options{
		LLM: llmClient, Tools: reg, MaxIterations: cfg.Agent.MaxIterations,
	})
	if err != nil {
		return err
	}

	if userInput == "" {
		userInput, err = readStdin()
		if err != nil {
			return err
		}
	}
	if userInput == "" {
		return errors.New("empty input (pass a prompt or pipe stdin)")
	}

	res, err := a.RunTurn(ctx, userInput)
	if err != nil {
		return err
	}
	fmt.Println(res.Output)
	logger.Info("turn complete", "iterations", res.Iterations)
	return nil
}

func readStdin() (string, error) {
	st, _ := os.Stdin.Stat()
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var b []byte
	for sc.Scan() {
		b = append(b, sc.Bytes()...)
		b = append(b, '\n')
	}
	return string(b), sc.Err()
}
```

- [ ] **Step 3: Write `cmd/darek/doctor.go` (and migrate)**

```go
package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"darek/config"
	"darek/db"
	"darek/llm"
)

func runMigrate(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
}

type check struct {
	name string
	ok   bool
	msg  string
}

func runDoctor(ctx context.Context, cfgPath string) error {
	results := []check{}
	add := func(name string, err error, okMsg string) {
		if err != nil {
			results = append(results, check{name: name, ok: false, msg: err.Error()})
		} else {
			results = append(results, check{name: name, ok: true, msg: okMsg})
		}
	}

	cfg, err := config.Load(cfgPath)
	add("config", err, fmt.Sprintf("loaded %s", cfgPath))
	if err == nil {
		dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
		add("postgres dsn", err, "resolved")
		if err == nil {
			pool, err := db.Open(ctx, dsn)
			add("postgres connect", err, "connected")
			if err == nil {
				_, err = pool.Exec(ctx, "SELECT 1")
				add("postgres query", err, "ok")
				pool.Close()
			}
		}
		key, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
		add("openai key", err, "present")
		if err == nil {
			cl, err := llm.New(llm.Options{APIKey: key, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model, Timeout: 5 * time.Second})
			add("openai client construct", err, fmt.Sprintf("model=%s known=%v", cfg.OpenAI.Model, llm.KnownModel(cfg.OpenAI.Model)))
			_ = cl
		}
		conn, err := net.DialTimeout("tcp", cfg.OTEL.ExporterEndpoint, 2*time.Second)
		add("otel exporter reachable", err, cfg.OTEL.ExporterEndpoint)
		if err == nil {
			conn.Close()
		}
	}

	hasFail := false
	for _, c := range results {
		mark := "OK "
		if !c.ok {
			mark = "FAIL"
			hasFail = true
		}
		fmt.Printf("[%s] %-30s %s\n", mark, c.name, strings.TrimSpace(c.msg))
	}
	if hasFail {
		return fmt.Errorf("doctor: one or more checks failed")
	}
	return nil
}
```

- [ ] **Step 4: Write example config + add config note to README**

`config/testdata/config.example.yaml`:

```yaml
openai:
  model: gpt-4.1
  api_key_env: DAREK_OPENAI_API_KEY

postgres:
  url_env: DAREK_POSTGRES_URL

otel:
  service_name: darek
  exporter_endpoint: localhost:4317
  insecure: true

agent:
  max_iterations: 10
  llm_timeout: 60s
  tool_timeout: 30s

memory:
  pgvector: false
  embedding_model: text-embedding-3-small
```

- [ ] **Step 5: Build and smoke-test**

```bash
make build
./darek --help 2>&1 || true   # we have no help yet; should print "unknown subcommand" or similar
```

Expected: binary builds.

- [ ] **Step 6: Commit**

```bash
git add cmd/ config/testdata/config.example.yaml
git commit -m "feat(cmd): chat / migrate / doctor subcommands"
```

---

## Task 13 — README quickstart + acceptance

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Write the full `README.md`**

```markdown
# darek

A Go personal-assistant CLI. Talks to OpenAI, remembers things in Postgres, fully observable.
First plan ships **memory only** — calendars, Todoist, mail land in subsequent plans.

## Quickstart

```bash
# 1. Spin up Postgres + observability stack
make up
make obs-up

# 2. Configure
mkdir -p ~/.darek
cp config/testdata/config.example.yaml ~/.darek/config.yaml
cat > ~/.darek/secrets.env <<'EOF'
DAREK_OPENAI_API_KEY=sk-...
DAREK_POSTGRES_URL=postgres://darek:darek@localhost:5432/darek?sslmode=disable
EOF
chmod 600 ~/.darek/secrets.env

# 3. Build & migrate
make build
set -a; source ~/.darek/secrets.env; set +a
./darek migrate

# 4. Health check
./darek doctor

# 5. Talk to it
./darek "remember I'm tracking a Berlin trip in May"
./darek "what trips am I tracking?"
```

Open Grafana at <http://localhost:3000> (anonymous admin) and look at the `darek` folder.
Open Jaeger at <http://localhost:16686>.

## Layout

```
cmd/darek/  CLI entry
agent/      tool-calling loop
llm/        OpenAI wrapper + cost calc
tools/      tool interface + registry
memory/     Postgres-backed notes + recall/save tools
obs/        OTEL setup, metrics, redactor, slog
db/         pgx pool + embedded migrations
config/     YAML loader + secret resolver
otel/       collector, prom, grafana provisioning
```

## Make targets

- `make build` — build the CLI
- `make test` — unit tests
- `make test-integration` — run with `-tags=integration` (needs Docker)
- `make up` / `make down` — Postgres
- `make obs-up` / `make obs-down` — OTEL Collector + Jaeger + Prom + Grafana

## Roadmap

- Plan 2: Calendars (Google + iCal)
- Plan 3: Todoist (read + write)
- Plan 4: Mail receive (IMAP sync, search, body/attachment fetch)
- Plan 5: Mail send (confirm-before-send, IMAP APPEND to Sent)
```

- [ ] **Step 2: Run full test suite**

```bash
make test
make test-integration
```

Expected: all PASS.

- [ ] **Step 3: Manual acceptance — memory works end-to-end**

```bash
make build
make up && make obs-up
set -a; source ~/.darek/secrets.env; set +a
./darek migrate
./darek doctor

./darek "remember I'm tracking a Berlin trip in May"
./darek "what trips am I tracking?"
```

Expected: second invocation surfaces the Berlin note via `memory.recall`. Open Jaeger, confirm a `darek.turn` trace exists with `chat` and `tool.execute` children. Open Grafana → `darek — tokens & cost`, see non-zero values.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: README quickstart, layout, make targets"
```

---

## Acceptance criteria for this plan

1. `make test` and `make test-integration` both pass on a clean checkout (Docker required).
2. `darek doctor` reports green for config, postgres, openai key, otel exporter.
3. `darek migrate` applies migrations idempotently.
4. `darek "remember <fact>"` saves a note via `memory.save`; a fresh invocation retrieves it via `memory.recall`.
5. Jaeger shows a `darek.turn` trace per CLI invocation with nested `chat` and `tool.execute` spans, including `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens` attributes.
6. Grafana dashboards show non-zero values for tokens, cost, tool latency, turn duration.
7. No secret values appear in stdout JSON logs (verified by grepping a redacted Bearer header).

## Future plans

- **Plan 2 — Calendars:** `tools/calendar/` with `CalendarSource` interface, Google OAuth subcommand, iCal feed reader, `calendar.list_events` tool.
- **Plan 3 — Todoist:** `tools/todoist/` with REST client, `list_tasks` / `create_task` / `complete_task` / `update_task` tools, multi-step E2E test (calendar+todoist).
- **Plan 4 — Mail receive:** `tools/mail/imap`, sync command, mail schema migration, `mail.search` / `mail.get_body` / `mail.get_attachment` tools.
- **Plan 5 — Mail send:** `tools/mail/smtp`, `Confirmer` interface + CLI prompt, RFC 5322 builder with reply threading, IMAP `APPEND` to Sent folder, `mail.send` tool.
