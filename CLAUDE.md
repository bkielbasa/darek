# darek

Go module for importing/syncing data (Todoist, FreshRSS, IMAP, ICS) with OpenTelemetry instrumentation and Postgres storage.

## Commands

- `make build` — build `./cmd/darek`
- `make test` — unit tests
- `make test-integration` — integration tests (uses testcontainers; Ryuk disabled for OrbStack)
- `make lint` — `go vet ./...`

## Layout

Flat Go layout. Tools live under top-level `tools/`, not `pkg/`.
