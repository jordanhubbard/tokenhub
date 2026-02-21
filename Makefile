.PHONY: build run test test-race test-integration test-e2e vet lint docker clean docs docs-serve release release-major release-minor release-patch builder setup bootstrap

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

# ──── Host setup ────

# setup aligns the host Docker CLI to the best available configuration.
# On macOS with Homebrew-installed Docker, stale Docker Desktop symlinks in
# ~/.docker/cli-plugins/ shadow the working Homebrew binaries.  This target
# detects and repairs that situation.
setup:
	@scripts/setup-docker.sh

# ──── Builder image (cached) ────

builder: setup
	@$(DOCKER_BUILD) -q -t $(BUILDER_IMAGE) -f Dockerfile.dev . >/dev/null

# ──── Build ────

build: builder
	$(DOCKER_RUN) go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub

# ──── Run ────

run: docker
	docker compose up -d
	@$(MAKE) -s bootstrap
	docker compose logs -f tokenhub

# ──── Bootstrap ────

bootstrap:
	@if [ -f bootstrap.local ]; then \
		max=50; attempt=0; \
		while [ $$attempt -lt $$max ]; do \
			if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then \
				echo "TokenHub is healthy, running bootstrap.local..."; \
				chmod +x bootstrap.local && ./bootstrap.local; \
				break; \
			fi; \
			attempt=$$((attempt + 1)); \
			sleep 2; \
		done; \
		if [ $$attempt -ge $$max ]; then \
			echo "WARNING: TokenHub did not become healthy in 100s, skipping bootstrap.local"; \
			echo "         Run 'make bootstrap' manually once TokenHub is ready."; \
		fi; \
	else \
		echo "No bootstrap.local found (copy bootstrap.local.example to create one)"; \
	fi

# ──── Tests ────

test: builder
	$(DOCKER_RUN) go test ./...

test-race: builder
	$(DOCKER_RUN) go test -race ./...

test-coverage: builder
	$(DOCKER_RUN) go test -race -coverprofile=coverage.out ./...

test-integration: docker
	@bash tests/integration.sh

test-e2e:
	@bash tests/e2e-temporal.sh

# ──── Code quality ────

vet: builder
	$(DOCKER_RUN) go vet ./...

lint: builder
	$(DOCKER_RUN) golangci-lint run --concurrency=1

# ──── Docker ────

docker: setup
	$(DOCKER_BUILD) -t tokenhub:$(VERSION) .

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
