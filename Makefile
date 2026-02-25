.PHONY: all build install package run start stop restart admin-key logs test test-race test-integration test-e2e vet lint clean docs docs-serve release release-major release-minor release-patch builder setup _write-env help

INSTALL_DIR ?= $(HOME)/.local/bin
MAN_DIR     ?= $(HOME)/.local/share/man

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

BUILDER_IMAGE := tokenhub-builder
GOLANG_IMAGE  := golang:1.24-bookworm

# Used by build/test/vet — only needs Go, no extra tools.
COMPILE_RUN := docker run --rm \
	-v $(CURDIR):/src \
	-v tokenhub-gomod:/go/pkg/mod \
	-v tokenhub-gocache:/root/.cache/go-build \
	-w /src \
	$(GOLANG_IMAGE)

# Used by lint/docs — needs the full builder image with golangci-lint and mdbook.
DOCKER_RUN  := docker run --rm \
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

# ──── Help ────

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

# ──── Host setup ────

setup: ## Align host Docker CLI to best available configuration
	@scripts/setup-docker.sh

# ──── Builder image (cached) ────

builder: setup ## Build the development container image (cached)
	@if docker image inspect $(BUILDER_IMAGE) >/dev/null 2>&1; then \
		true; \
	else \
		echo "Building builder image (first time)..."; \
		$(DOCKER_BUILD) -t $(BUILDER_IMAGE) -f Dockerfile.dev .; \
	fi

# ──── Build ────

build: ## Compile tokenhub and tokenhubctl binaries
	@echo "Compiling tokenhub..."
	@$(COMPILE_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	@echo "Compiling tokenhubctl..."
	@$(COMPILE_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@echo "Build complete: bin/tokenhub bin/tokenhubctl"

# ──── Install ────

install: ## Build natively and install to ~/.local/bin (requires Go 1.24+)
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@mkdir -p $(INSTALL_DIR)
	cp bin/tokenhub bin/tokenhubctl $(INSTALL_DIR)/
	@mkdir -p $(MAN_DIR)/man1
	cp man/man1/tokenhubctl.1 $(MAN_DIR)/man1/
	@echo "Installed tokenhub and tokenhubctl to $(INSTALL_DIR)"
	@echo "Installed man page to $(MAN_DIR)/man1 (run: man tokenhubctl)"

# ──── Package ────

package: setup ## Build production container image
	$(DOCKER_BUILD) -t tokenhub:$(VERSION) -t tokenhub:latest .

deploy: build ## Compile and inject binary into running image, then restart (no internet required)
	@echo "Injecting bin/tokenhub into tokenhub:latest..."
	@printf 'FROM tokenhub:latest\nUSER root\nCOPY bin/tokenhub /tokenhub\nCOPY scripts/docker-entrypoint.sh /entrypoint.sh\nRUN chmod +x /entrypoint.sh\nENTRYPOINT ["/entrypoint.sh"]\n' | docker build -t tokenhub:latest -f - .
	@$(MAKE) -s restart

# ──── Lifecycle ────

run: package ## Build, start container, and follow logs
	docker compose up -d
	docker compose logs -f tokenhub

start: ## Start tokenhub container
	docker compose up -d tokenhub
	@$(MAKE) -s _write-env

admin-key: start ## Print the admin API key
	@grep '^TOKENHUB_ADMIN_TOKEN=' $(HOME)/.tokenhub/env | cut -d= -f2-

stop: ## Stop tokenhub container
	docker compose stop tokenhub

restart: ## Restart tokenhub container
	docker compose stop tokenhub
	docker compose up -d tokenhub
	@$(MAKE) -s _write-env

# Read /data/env from the running container, prepend TOKENHUB_URL, and write
# ~/.tokenhub/env on the host. tokenhubctl auto-sources this file on startup
# so no shell profile changes are needed.
_write-env:
	@echo "Waiting for tokenhub to start..."
	@for i in $$(seq 1 30); do \
		env=$$(docker compose exec -T tokenhub cat /data/env 2>/dev/null); \
		if echo "$$env" | grep -q "TOKENHUB_API_KEY"; then \
			mkdir -p $(HOME)/.tokenhub; \
			{ printf 'TOKENHUB_URL=http://localhost:%s\n' "$(TOKENHUB_PORT)"; printf '%s\n' "$$env"; } \
				> $(HOME)/.tokenhub/env; \
			chmod 600 $(HOME)/.tokenhub/env; \
			echo "State written to ~/.tokenhub/env"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "Warning: could not write ~/.tokenhub/env — run: tokenhubctl admin-token"

logs: ## Follow tokenhub container logs
	docker compose logs -f tokenhub

# ──── Tests ────

test: ## Run unit tests in container
	$(COMPILE_RUN) go test -buildvcs=false ./...

test-race: ## Run tests with race detector (native)
	go test -race ./...

test-coverage: ## Run tests with coverage report
	go test -race -coverprofile=coverage.out ./...

test-integration: package ## Run integration tests
	@bash tests/integration.sh

test-e2e: package ## Run end-to-end tests
	@bash tests/e2e-temporal.sh

# ──── Code quality ────

vet: ## Run go vet
	$(COMPILE_RUN) go vet -buildvcs=false ./...

lint: builder ## Run golangci-lint
	$(DOCKER_RUN) golangci-lint run --concurrency=1

# ──── Docs ────

docs: builder ## Build documentation
	$(DOCKER_RUN) sh -c "cd docs && mdbook build"

docs-serve: builder ## Serve documentation locally on port 3000
	docker run --rm -v $(CURDIR):/src -w /src -p 3000:3000 $(BUILDER_IMAGE) \
		sh -c "cd docs && mdbook serve -n 0.0.0.0"

# ──── Release ────

release: ## Bump patch version and release (x.y.Z)
	@./scripts/release.sh patch

release-minor: ## Bump minor version and release (x.Y.0)
	@./scripts/release.sh minor

release-major: ## Bump major version and release (X.0.0)
	@./scripts/release.sh major

release-patch: release ## Alias for release

# ──── Clean ────

clean: ## Remove build artifacts
	rm -rf bin/ docs/book/ coverage.out
