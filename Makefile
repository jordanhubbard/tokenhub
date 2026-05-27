.PHONY: help start stop status restart logs install build deploy package test release release-minor release-major release-help release-local clean-binaries _write-env

INSTALL_DIR ?= $(HOME)/.local/bin
MAN_DIR     ?= $(HOME)/.local/share/man

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

GOLANG_IMAGE := golang:1.24-bookworm

COMPILE_RUN := docker run --rm \
	-v $(CURDIR):/src \
	-v tokenhub-gomod:/go/pkg/mod \
	-v tokenhub-gocache:/root/.cache/go-build \
	-w /src \
	$(GOLANG_IMAGE)

DOCKER_BUILD := $(shell docker buildx version >/dev/null 2>&1 && echo "docker buildx build --load" || echo "docker build")

# Host port the tokenhub container exposes (must match docker-compose.yaml).
TOKENHUB_PORT ?= 8090

# ──── Help ────

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

# ──── Lifecycle ────

start: ## Start tokenhub container (builds image if not present)
	@if ! docker image inspect tokenhub:latest >/dev/null 2>&1; then \
		echo "Image tokenhub:latest not found, building..."; \
		$(MAKE) package; \
	fi
	docker compose up -d tokenhub
	@$(MAKE) -s _write-env

stop: ## Stop tokenhub container
	docker compose stop tokenhub

status: ## Show tokenhub container status
	docker compose ps tokenhub

restart: ## Restart tokenhub container
	docker compose stop tokenhub
	docker compose up -d tokenhub
	@$(MAKE) -s _write-env

logs: ## Follow tokenhub container logs
	docker compose logs -f tokenhub

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

# ──── Build ────

install: ## Build natively and install to ~/.local/bin (requires Go 1.24+)
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@mkdir -p $(INSTALL_DIR)
	cp bin/tokenhub bin/tokenhubctl $(INSTALL_DIR)/
	@mkdir -p $(MAN_DIR)/man1
	cp man/man1/tokenhubctl.1 $(MAN_DIR)/man1/
	@echo "Installed tokenhub and tokenhubctl to $(INSTALL_DIR)"
	@echo "Installed man page to $(MAN_DIR)/man1 (run: man tokenhubctl)"

build: ## Compile tokenhub and tokenhubctl binaries (in Docker)
	@echo "Compiling tokenhub..."
	@$(COMPILE_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub
	@echo "Compiling tokenhubctl..."
	@$(COMPILE_RUN) go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl ./cmd/tokenhubctl
	@echo "Build complete: bin/tokenhub bin/tokenhubctl"

package: ## Build production container image
	$(DOCKER_BUILD) -t tokenhub:$(VERSION) -t tokenhub:latest .

test: ## Run unit tests (native)
	go test -race ./...

deploy: build ## Compile and inject binary into running image, then restart (no internet required)
	@echo "Injecting bin/tokenhub into tokenhub:latest..."
	@printf 'FROM tokenhub:latest\nUSER root\nCOPY bin/tokenhub /tokenhub\nCOPY scripts/docker-entrypoint.sh /entrypoint.sh\nRUN chmod +x /entrypoint.sh\nENTRYPOINT ["/entrypoint.sh"]\n' | docker build -t tokenhub:latest -f - .
	@$(MAKE) -s restart

# ──── Cross-Compilation (for local testing) ────

build-linux-amd64: ## Build tokenhub and tokenhubctl for Linux x86-64
	@mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub-$(VERSION)-linux-amd64 ./cmd/tokenhub
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl-$(VERSION)-linux-amd64 ./cmd/tokenhubctl

build-linux-arm64: ## Build tokenhub and tokenhubctl for Linux ARM64
	@mkdir -p bin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub-$(VERSION)-linux-arm64 ./cmd/tokenhub
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl-$(VERSION)-linux-arm64 ./cmd/tokenhubctl

build-darwin-amd64: ## Build tokenhub and tokenhubctl for macOS Intel
	@mkdir -p bin
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub-$(VERSION)-darwin-amd64 ./cmd/tokenhub
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl-$(VERSION)-darwin-amd64 ./cmd/tokenhubctl

build-darwin-arm64: ## Build tokenhub and tokenhubctl for macOS Apple Silicon
	@mkdir -p bin
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub-$(VERSION)-darwin-arm64 ./cmd/tokenhub
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhubctl-$(VERSION)-darwin-arm64 ./cmd/tokenhubctl

build-all-platforms: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 ## Build binaries for all platforms

clean-binaries: ## Remove built binaries
	rm -f bin/tokenhub-* bin/tokenhubctl-*

# ──── Release ────

release-help: ## Show release process documentation
	@echo "TokenHub Release Process"
	@echo "========================"
	@echo ""
	@echo "Prerequisites:"
	@echo "  - GitHub CLI (gh) installed and authenticated: gh auth login"
	@echo "  - Working directory clean: git status"
	@echo "  - On main branch: git checkout main"
	@echo ""
	@echo "To create a release:"
	@echo "  ./scripts/release.sh [major|minor|patch]"
	@echo ""
	@echo "The release script will:"
	@echo "  1. Validate prerequisites (gh auth, clean working tree, main branch)"
	@echo "  2. Calculate next version (semantic versioning)"
	@echo "  3. Generate and update CHANGELOG.md from git commits"
	@echo "  4. Run test suite (must pass)"
	@echo "  5. Build Docker image"
	@echo "  6. Create git tag and push to origin"
	@echo "  7. Create GitHub release with release notes"
	@echo ""
	@echo "After the release:"
	@echo "  - GitHub Actions automatically builds multi-platform binaries"
	@echo "  - Binaries are uploaded to GitHub release assets"
	@echo "  - Available platforms:"
	@echo "    - macOS (Intel x86-64 and Apple Silicon ARM64)"
	@echo "    - Linux (x86-64 and ARM64)"
	@echo ""
	@echo "To test cross-compilation locally:"
	@echo "  make build-all-platforms       # Build all platforms"
	@echo "  make build-linux-amd64         # Build for Linux x86-64"
	@echo "  make build-linux-arm64         # Build for Linux ARM64"
	@echo "  make build-darwin-amd64        # Build for macOS Intel"
	@echo "  make build-darwin-arm64        # Build for macOS Apple Silicon"
	@echo ""

release-local: build-all-platforms ## Build all platform binaries locally
	@echo ""
	@echo "Local cross-compilation complete!"
	@echo "Binaries are in bin/ directory:"
	@ls -lh bin/tokenhub-* bin/tokenhubctl-* 2>/dev/null || echo "(none built)"

# ──── Release Commands ────

release: ## Create a patch release (v*.*.X)
	@echo "Creating patch release..."
	@./scripts/release.sh patch

release-minor: ## Create a minor release (v*.X.0)
	@echo "Creating minor release..."
	@./scripts/release.sh minor

release-major: ## Create a major release (v X.0.0)
	@echo "Creating major release..."
	@./scripts/release.sh major
