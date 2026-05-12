.PHONY: build test test-integration up down obs-up obs-down lint docker

GO ?= go
BIN ?= darek

DOCKER_REPO      ?= bartlomiejklimczak/darek
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

build:
	$(GO) build -o $(BIN) ./cmd/darek

test:
	$(GO) test ./...

# OrbStack's Docker context does not run Ryuk's reaper container reliably;
# disable it for integration tests. t.Cleanup handles container teardown.
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

# docker fetches existing v* tags from Docker Hub, computes the next
# patch-incremented tag, and pushes a multi-arch image with that tag.
# Falls back to v0.1.0 if the repo has no v* tags yet.
docker:
	@command -v jq >/dev/null 2>&1 || { echo "jq required"; exit 1; }
	@latest=$$(curl -fsSL "https://hub.docker.com/v2/repositories/$(DOCKER_REPO)/tags?page_size=100" \
	    | jq -r '.results[].name' \
	    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' \
	    | sed 's/^v//' \
	    | sort -t. -k1,1n -k2,2n -k3,3n \
	    | tail -n1); \
	if [ -z "$$latest" ]; then \
	    next="v0.1.0"; \
	else \
	    next=$$(echo "$$latest" | awk -F. '{printf "v%d.%d.%d\n", $$1, $$2, $$3+1}'); \
	fi; \
	echo "Building $(DOCKER_REPO):$$next for $(DOCKER_PLATFORMS)"; \
	docker buildx build \
	    --platform $(DOCKER_PLATFORMS) \
	    -t $(DOCKER_REPO):$$next \
	    --push .
