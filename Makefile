.PHONY: build package run test test-race test-integration test-e2e vet lint clean docs docs-serve release release-major release-minor release-patch builder setup

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

BUILDER_IMAGE := tokenhub-builder
DOCKER_RUN    := docker run --rm \
	-v $(CURDIR):/src \
	-v tokenhub-gomod:/go/pkg/mod \
	-v tokenhub-gocache:/root/.cache/go-build \
	-w /src \
	$(BUILDER_IMAGE)

# Use buildx if available, fall back to legacy builder.
# --load ensures the image is available to the local docker daemon.
DOCKER_BUILD := $(shell docker buildx version >/dev/null 2>&1 && echo "docker buildx build --load" || echo "docker build")

# Host port the tokenhub container exposes (must match docker-compose.yaml).
TOKENHUB_PORT ?= 8090

# ──── Host setup ────

# setup aligns the host Docker CLI to the best available configuration.
# On macOS with Homebrew-installed Docker, stale Docker Desktop symlinks in
# ~/.docker/cli-plugins/ shadow the working Homebrew binaries.
# No-op on Linux.
setup:
	@scripts/setup-docker.sh

# ──── Builder image (cached) ────

builder: setup
	@$(DOCKER_BUILD) -q -t $(BUILDER_IMAGE) -f Dockerfile.dev . >/dev/null

# ──── Build ────

build: builder
	$(DOCKER_RUN) go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	$(DOCKER_RUN) go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl

# ──── Package ────

# package builds the production container image and tags it as both
# tokenhub:<version> (for pinning) and tokenhub:latest (for compose).
package: setup
	$(DOCKER_BUILD) -t tokenhub:$(VERSION) -t tokenhub:latest .

# ──── Run ────

run: package
	docker compose up -d
	docker compose logs -f tokenhub

# ──── Tests ────

test: builder
	$(DOCKER_RUN) go test ./...

test-race: builder
	$(DOCKER_RUN) go test -race ./...

test-coverage: builder
	$(DOCKER_RUN) go test -race -coverprofile=coverage.out ./...

test-integration: package
	@bash tests/integration.sh

test-e2e: package
	@bash tests/e2e-temporal.sh

# ──── Code quality ────

vet: builder
	$(DOCKER_RUN) go vet ./...

lint: builder
	$(DOCKER_RUN) golangci-lint run --concurrency=1

# ──── Docs ────

docs: builder
	$(DOCKER_RUN) sh -c "cd docs && mdbook build"

docs-serve: builder
	docker run --rm -v $(CURDIR):/src -w /src -p 3000:3000 $(BUILDER_IMAGE) \
		sh -c "cd docs && mdbook serve -n 0.0.0.0"

# ──── Release ────
#   make release              # Bump patch version (x.y.Z)
#   make release-minor        # Bump minor version (x.Y.0)
#   make release-major        # Bump major version (X.0.0)
#   BATCH=yes make release    # Non-interactive mode

release:
	@./scripts/release.sh patch

release-minor:
	@./scripts/release.sh minor

release-major:
	@./scripts/release.sh major

release-patch: release

# ──── Clean ────

clean:
	rm -rf bin/ docs/book/ coverage.out
