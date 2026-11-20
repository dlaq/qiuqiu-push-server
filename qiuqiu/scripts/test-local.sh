#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
(cd server && go test -race ./... && go vet ./...)
(cd admin && npm ci --no-audit --no-fund && npm run build)
PUSH_DOMAIN=push.example.com NTFY_DOMAIN=ntfy.example.com docker compose -f deploy/compose.yml config --quiet
PUSH_DOMAIN=push.example.com NTFY_DOMAIN=ntfy.example.com docker compose -f deploy/compose.yml -f deploy/compose.firebase.yml config --quiet
