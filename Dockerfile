# syntax=docker/dockerfile:1

# --- Build stage: Go binary + documentation ---
FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src

# Install build dependencies: git, certificates, mdbook (pre-built), golangci-lint.
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates curl bash && \
    MDBOOK_VERSION=0.4.44 && \
    case "${TARGETARCH}" in \
      amd64) MDBOOK_ARCH="x86_64-unknown-linux-musl" ;; \
      arm64) MDBOOK_ARCH="aarch64-unknown-linux-musl" ;; \
      *) echo "unsupported arch: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -sSL --retry 3 --retry-delay 5 "https://github.com/rust-lang/mdBook/releases/download/v${MDBOOK_VERSION}/mdbook-v${MDBOOK_VERSION}-${MDBOOK_ARCH}.tar.gz" \
      -o /tmp/mdbook.tar.gz && tar xz -C /usr/local/bin -f /tmp/mdbook.tar.gz && rm /tmp/mdbook.tar.gz && \
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
