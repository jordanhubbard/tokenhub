# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
ARG VERSION=dev
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tokenhub ./cmd/tokenhub

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/tokenhub /tokenhub
COPY --from=build /src/config/config.example.yaml /config/config.yaml
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/tokenhub"]
