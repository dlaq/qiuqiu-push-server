#!/bin/sh
set -eu
: "${NTFY_BASE_URL:?}" "${NTFY_TOKEN:?}" "${NTFY_TOPIC:?}"
curl --fail-with-body --silent --show-error -X POST "$NTFY_BASE_URL/$NTFY_TOPIC" -H "Authorization: Bearer $NTFY_TOKEN" -H 'Title: 服务器告警' -H 'Priority: urgent' -H 'Tags: warning,server' -H 'Click: https://example.com/server' -d '服务器 CPU 温度异常'
