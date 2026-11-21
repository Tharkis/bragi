# syntax=docker/dockerfile:1

# ---- Web UI (architecture-independent, so build it on the build host) ----
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Go binary (cross-compiled; pure-Go SQLite means no CGO) ----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/bragi ./cmd/bragi

# ---- Runtime ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata su-exec
COPY --from=build /out/bragi /usr/local/bin/bragi
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Unraid defaults: nobody:users (99:100).
ENV BRAGI_CONFIG_DIR=/config \
    BRAGI_PORT=8787 \
    PUID=99 \
    PGID=100 \
    UMASK=002 \
    TZ=Etc/UTC

VOLUME ["/config"]
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- "http://127.0.0.1:${BRAGI_PORT}/api/v1/health" >/dev/null || exit 1

LABEL org.opencontainers.image.title="Bragi" \
      org.opencontainers.image.description="All-in-one media manager: request, search, download and import" \
      org.opencontainers.image.source="https://github.com/Tharkis/bragi" \
      org.opencontainers.image.licenses="GPL-3.0-only"

ENTRYPOINT ["/entrypoint.sh"]
