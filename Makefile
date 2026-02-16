.PHONY: build run test test-race vet lint docker clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/tokenhub ./cmd/tokenhub

run: build
	./bin/tokenhub

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

docker:
	docker build -t tokenhub:$(VERSION) .

clean:
	rm -rf bin/
