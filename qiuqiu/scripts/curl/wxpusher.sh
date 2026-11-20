#!/bin/sh
set -eu
: "${BASE_URL:?}" "${APP_TOKEN:?}" "${LOCAL_UID:?}"
curl --fail-with-body --silent --show-error --get "$BASE_URL/api/send/message" --data-urlencode "appToken=$APP_TOKEN" --data-urlencode "uids=$LOCAL_UID" --data-urlencode 'summary=服务器状态' --data-urlencode 'content=服务器运行正常' --data-urlencode 'contentType=1' --data-urlencode 'url=https://example.com'
