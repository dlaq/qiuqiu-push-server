#!/bin/sh
set -eu
: "${BASE_URL:?}" "${SENDKEY:?}" "${SC3_UID:?}"
curl --fail-with-body --silent --show-error "$BASE_URL/sc3/$SC3_UID/send/$SENDKEY.send" --data-urlencode 'title=SC3告警' --data-urlencode 'desp=服务器运行异常' --data-urlencode 'tags=server|urgent'
