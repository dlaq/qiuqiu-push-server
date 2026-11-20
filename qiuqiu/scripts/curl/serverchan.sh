#!/bin/sh
set -eu
: "${BASE_URL:?}" "${SENDKEY:?}"
curl --fail-with-body --silent --show-error "$BASE_URL/sctapi/$SENDKEY.send" --data-urlencode 'title=服务器告警' --data-urlencode 'desp=**中文**：CPU 温度异常' --data-urlencode 'url=https://example.com/server'
