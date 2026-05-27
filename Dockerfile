# syntax=docker/dockerfile:1.7
# Espur container. Pure-Go modernc.org/sqlite means we don't need CGo, which
# lets us ship from a static base image. Spec: bootstrap.dog.md + the README
# "Deploy" section. opencode itself must also be available at runtime — it is
# installed in the runtime stage via npm.

# --- build stage --------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /out/espur ./cmd/espur \
 && go build -ldflags="-s -w" -o /out/espur-genkey ./cmd/espur-genkey

# --- runtime stage ------------------------------------------------------
# opencode is a Node CLI; we need Node available. Alpine + npm install is the
# simplest path that keeps the image small without a multi-binary base.
FROM node:20-alpine AS runtime
RUN apk add --no-cache ca-certificates tzdata \
 && npm install -g opencode-ai@latest \
 && mkdir -p /data \
 && addgroup -S espur && adduser -S espur -G espur \
 && chown -R espur:espur /data

COPY --from=build /out/espur /usr/local/bin/espur
COPY --from=build /out/espur-genkey /usr/local/bin/espur-genkey

ENV ESPUR_DATA_DIR=/data \
    ESPUR_WEB_PORT=8080 \
    ESPUR_LOG_LEVEL=info \
    XDG_DATA_HOME=/data/xdg-data \
    HOME=/data

USER espur
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080

# SIGTERM-driven graceful shutdown handled by Espur itself per shutdown.dog.md;
# the container runtime should send SIGTERM and respect terminationGracePeriod
# of at least ESPUR_SHUTDOWN_DRAIN seconds (default 30s).
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/espur"]
