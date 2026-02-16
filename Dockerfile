# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/tokenhub ./cmd/tokenhub

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/tokenhub /tokenhub
COPY config/config.example.yaml /config/config.yaml
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/tokenhub"]
