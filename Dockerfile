# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1 — frontend (web/modern, Vite)
#
# The frontend that ships is web/modern, NOT web/default (CRA). Vite writes its
# output to `../build/default` relative to /web/modern, i.e. /web/build/default
# — the exact path main.go embeds with `//go:embed web/build/*`.
# ---------------------------------------------------------------------------
FROM node:20-alpine AS web

WORKDIR /web/modern

# Manifests first, so `npm ci` is only re-run when dependencies change.
COPY web/modern/package.json web/modern/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/modern/ ./

# keep outDir (../build/default) => /web/build/default, preserving the tree the
# embed pattern expects.
RUN npm run build

# ---------------------------------------------------------------------------
# Stage 2 — Go binary
#
# CGO is required by mattn/go-sqlite3. The external static link keeps the
# runtime image independent of any glibc/musl version on the host.
# ---------------------------------------------------------------------------
FROM golang:alpine AS build

# musl-dev/build-base provide libc.a, which `-extldflags '-static'` needs.
RUN apk add --no-cache \
    gcc \
    musl-dev \
    sqlite-dev \
    build-base

ENV GO111MODULE=on \
    CGO_ENABLED=1 \
    GOOS=linux

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /web/build ./web/build

# VERSION in this repo is a 0-byte file, so reading it would stamp an empty
# string over common.Version's "v0.0.0" default. A build arg is explicit.
ARG VERSION=docker

RUN go build -trimpath \
    -ldflags "-s -w -X 'github.com/songquanpeng/one-api/common.Version=${VERSION}' -linkmode external -extldflags '-static'" \
    -o one-api

# ---------------------------------------------------------------------------
# Stage 3 — runtime
# ---------------------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S oneapi \
    && adduser -S -D -H -u 10001 -G oneapi -h /data oneapi \
    && mkdir -p /data/tiktoken /var/log/one-api \
    && chown -R oneapi:oneapi /data /var/log/one-api

COPY --from=build /build/one-api /usr/local/bin/one-api

# SQLITE_PATH is relative to the working directory by default ("one-api.db");
# pin it so the database lives next to the tiktoken cache on the data volume.
ENV TZ=Asia/Shanghai \
    SQLITE_PATH=/data/one-api.db \
    TIKTOKEN_CACHE_DIR=/data/tiktoken

WORKDIR /data
USER oneapi

EXPOSE 3000

ENTRYPOINT ["/usr/local/bin/one-api"]
# Log to the volume and (through logger's MultiWriter) to stdout/stderr.
CMD ["--log-dir", "/var/log/one-api"]
