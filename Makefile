.PHONY: build install package run start stop restart admin-key logs test test-race test-integration test-e2e vet lint clean docs docs-serve release release-major release-minor release-patch builder setup _write-env

INSTALL_DIR ?= $(HOME)/.local/bin
MAN_DIR     ?= $(HOME)/.local/share/man

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
	@if docker image inspect $(BUILDER_IMAGE) >/dev/null 2>&1; then \
		true; \
	else \
		echo "Building builder image (first time)..."; \
		$(DOCKER_BUILD) -t $(BUILDER_IMAGE) -f Dockerfile.dev .; \
	fi

# ──── Build ────

build: builder
	@echo "Compiling tokenhub..."
	@$(DOCKER_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	@echo "Compiling tokenhubctl..."
	@$(DOCKER_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@echo "Build complete: bin/tokenhub bin/tokenhubctl"

# ──── Install ────
# Builds natively on the host (requires Go 1.24+) and installs to ~/.local/bin.

install:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@mkdir -p $(INSTALL_DIR)
	cp bin/tokenhub bin/tokenhubctl $(INSTALL_DIR)/
	@mkdir -p $(MAN_DIR)/man1
	cp man/man1/tokenhubctl.1 $(MAN_DIR)/man1/
	@echo "Installed tokenhub and tokenhubctl to $(INSTALL_DIR)"
	@echo "Installed man page to $(MAN_DIR)/man1 (run: man tokenhubctl)"

# ──── Package ────

# package builds the production container image and tags it as both
# tokenhub:<version> (for pinning) and tokenhub:latest (for compose).
package: setup
	$(DOCKER_BUILD) -t tokenhub:$(VERSION) -t tokenhub:latest .

# ──── Lifecycle ────

run: package
	docker compose up -d
	docker compose logs -f tokenhub

start:
	docker compose up -d tokenhub
	@$(MAKE) -s _write-env

admin-key: start
	@grep '^TOKENHUB_ADMIN_TOKEN=' $(HOME)/.tokenhub/env | cut -d= -f2-

stop:
	docker compose stop tokenhub

restart:
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
		if [ -n "$$env" ]; then \
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

logs:
	docker compose logs -f tokenhub

# ──── Tests ────

test: builder
	$(DOCKER_RUN) go test -buildvcs=false ./...

test-race:
	go test -race ./...

test-coverage:
	go test -race -coverprofile=coverage.out ./...

test-integration: package
	@bash tests/integration.sh

test-e2e: package
	@bash tests/e2e-temporal.sh

# ──── Code quality ────

vet: builder
	$(DOCKER_RUN) go vet -buildvcs=false ./...

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
