#!/bin/sh
set -eu
: "${BASE_URL:?}" "${TOKEN:?}"
curl --fail-with-body --silent --show-error "$BASE_URL/api/v1/push" -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" -H 'Idempotency-Key: native-curl-example-1' -d '{"title":"服务器告警","body":"CPU 温度异常","business_type":"server","sound":"alarm","icon":"server","click":"https://example.com/server","tags":["warning","server"]}'
