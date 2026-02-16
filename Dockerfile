# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o tokenhub ./cmd/tokenhub

# Runtime stage - use a distroless image for security and CA certs
FROM gcr.io/distroless/static:nonroot

# Copy the binary from builder
COPY --from=builder /app/tokenhub /tokenhub

# Expose the default port
EXPOSE 8080

# Run the application
ENTRYPOINT ["/tokenhub"]
