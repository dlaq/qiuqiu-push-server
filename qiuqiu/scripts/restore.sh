#!/bin/sh
set -eu
backup_dir="${1:?usage: restore.sh /absolute/backup/directory CONFIRM_REPLACE}"
[ "${2:-}" = CONFIRM_REPLACE ] || { echo '此操作替换目标部署数据卷；请先备份，再传 CONFIRM_REPLACE。' >&2; exit 1; }
case "$backup_dir" in /*) ;; *) echo '请使用绝对备份路径' >&2;exit 1;;esac
for volume in gateway_data ntfy_data;do [ -s "$backup_dir/$volume.tar.gz" ] || { echo '缺少备份文件' >&2;exit 1; };done
cd "$(dirname "$0")/../deploy"
docker compose stop gateway ntfy
for volume in gateway_data ntfy_data;do
 docker volume create "qiuqiu-push_${volume}" >/dev/null
 docker run --rm -v "qiuqiu-push_${volume}:/restore" -v "$backup_dir:/backup:ro" alpine:3.23 sh -c 'find /restore -mindepth 1 -maxdepth 1 -exec rm -rf {} +; tar xzf "/backup/$1.tar.gz" -C /restore' -- "$volume"
done
docker compose start ntfy gateway
