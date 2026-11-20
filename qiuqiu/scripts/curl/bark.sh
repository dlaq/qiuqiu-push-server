#!/bin/sh
set -eu
: "${BASE_URL:?}" "${DEVICE_KEY:?}"
curl --fail-with-body --silent --show-error --get "$BASE_URL/bark/$DEVICE_KEY" --data-urlencode 'title=服务器告警' --data-urlencode 'subtitle=Ubuntu' --data-urlencode 'body=CPU温度异常' --data-urlencode 'group=server' --data-urlencode 'sound=alarm' --data-urlencode 'icon=server' --data-urlencode 'url=https://example.com/server'
