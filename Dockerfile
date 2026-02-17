# syntax=docker/dockerfile:1

# --- Build stage: Go binary + documentation ---
FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src

# Install build dependencies: git, certificates, Rust/Cargo (for mdbook), golangci-lint.
RUN apk add --no-cache git ca-certificates curl cargo && \
    cargo install mdbook --locked --root /usr/local && \
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin

# Download Go dependencies first (layer cache).
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build everything.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tokenhub ./cmd/tokenhub
RUN cd docs && mdbook build

# --- Runtime stage: minimal image ---
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/tokenhub /tokenhub
COPY --from=build /src/docs/book /docs/book
COPY --from=build /src/config/config.example.yaml /config/config.yaml
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/tokenhub", "-healthcheck"]
ENTRYPOINT ["/tokenhub"]
