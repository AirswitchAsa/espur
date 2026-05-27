#!/usr/bin/env bash
# Local dev launcher. v0.1 uses plain html/template (no templ build step),
# so this is just `go run` with sensible defaults. README mentions air; if
# you want hot-reload, install air and add a step here.
#
# Reads optional .env from the repo root (lines like KEY=value, ignores
# comments and blank lines).

set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

if [[ -z "${ESPUR_MASTER_KEY:-}" ]]; then
  echo "ESPUR_MASTER_KEY is required."
  echo "Generate one with:  go run ./cmd/espur-genkey"
  echo "Then put it in .env or export it before running this script."
  exit 1
fi

: "${ESPUR_DATA_DIR:=./data}"
: "${ESPUR_WEB_PORT:=8080}"
: "${ESPUR_LOG_LEVEL:=debug}"

export ESPUR_DATA_DIR ESPUR_WEB_PORT ESPUR_LOG_LEVEL
mkdir -p "$ESPUR_DATA_DIR"

echo "espur dev: web on :$ESPUR_WEB_PORT  data=$ESPUR_DATA_DIR  level=$ESPUR_LOG_LEVEL"
exec go run ./cmd/espur
