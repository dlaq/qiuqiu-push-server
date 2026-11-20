#!/bin/sh
set -eu
cd "$(dirname "$0")/../deploy"
backup_dir="${1:?usage: backup.sh /absolute/backup/directory}"
case "$backup_dir" in /*) ;; *) echo '请使用绝对备份路径' >&2; exit 1;; esac
mkdir -p "$backup_dir"
# Stop writers for a consistent backup of SQLite databases, WAL files and attachments.
docker compose stop gateway ntfy
trap 'docker compose start ntfy gateway' EXIT
for volume in gateway_data ntfy_data; do
 docker run --rm -v "qiuqiu-push_${volume}:/source:ro" -v "$backup_dir:/backup" alpine:3.23 sh -c 'umask 077; tar czf "/backup/$1.tar.gz" -C /source .; chown "$2" "/backup/$1.tar.gz"' -- "$volume" "$(id -u):$(id -g)"
done
chmod 600 "$backup_dir"/*.tar.gz
