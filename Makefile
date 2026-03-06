.PHONY: help start stop status restart logs install build deploy package test _write-env

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
