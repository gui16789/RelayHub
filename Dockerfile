# RelayHub headless server image.
# Builds the no-GUI server binary (cmd/headless); the Wails desktop app is
# not part of this image.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache dependency downloads separately from source compilation.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/relayhub ./cmd/headless

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 relayhub

WORKDIR /app
COPY --from=build /out/relayhub /app/relayhub
COPY config.example.yaml /app/config.example.yaml

# /data holds config.yaml, stats.json and cooldowns.json — mount a volume here
# so state survives container recreation.
RUN mkdir -p /data && chown relayhub:relayhub /data
VOLUME /data

USER relayhub
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8787/v1/models >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/relayhub", "/data/config.yaml"]
