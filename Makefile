.PHONY: build run test test-race test-integration test-e2e vet lint docker clean docs docs-serve release release-major release-minor release-patch builder

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

BUILDER_IMAGE := tokenhub-builder
DOCKER_RUN    := docker run --rm \
	-v $(CURDIR):/src \
	-v tokenhub-gomod:/go/pkg/mod \
	-v tokenhub-gocache:/root/.cache/go-build \
	-w /src \
	$(BUILDER_IMAGE)

# ──── Builder image (cached) ────

builder:
	@docker build -q -t $(BUILDER_IMAGE) -f Dockerfile.dev . >/dev/null

# ──── Build ────

build: builder
	$(DOCKER_RUN) go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub

# ──── Run ────

run: docker
	docker compose up

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
	$(DOCKER_RUN) golangci-lint run

# ──── Docker ────

docker:
	docker build -t tokenhub:$(VERSION) .

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
