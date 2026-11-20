#!/bin/sh
set -eu
: "${BASE_URL:?}" "${TOKEN:?}"
curl --fail-with-body --silent --show-error "$BASE_URL/send/$TOKEN" -H 'Content-Type: application/json' -d '{"title":"任务完成","content":"**模型部署成功**","template":"markdown","url":"https://example.com/task"}'
