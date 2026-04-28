.PHONY: build test test-integration up down obs-up obs-down lint

GO ?= go
BIN ?= darek

build:
	$(GO) build -o $(BIN) ./cmd/darek

test:
	$(GO) test ./...

test-integration:
	TESTCONTAINERS_RYUK_DISABLED=true $(GO) test -tags=integration -count=1 ./...

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
