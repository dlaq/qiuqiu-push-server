#!/bin/sh
set -eu
: "${BASE_URL:?}" "${SENDKEY:?}"
curl --fail-with-body --silent --show-error --get "$BASE_URL/pushbear/sub" --data-urlencode "sendkey=$SENDKEY" --data-urlencode 'text=历史接口测试' --data-urlencode 'desp=球球推送本地兼容'
