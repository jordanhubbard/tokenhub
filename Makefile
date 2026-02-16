.PHONY: build run test lint

build:
	go build -o bin/tokenhub ./cmd/tokenhub

run:
	go run ./cmd/tokenhub

test:
	go test ./...

lint:
	@echo "Add golangci-lint if desired"
