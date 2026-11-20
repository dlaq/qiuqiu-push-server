#!/bin/sh
set -eu
# Only the password HASH is provisioned here. The Gateway reads its independent secret file.
export NTFY_AUTH_USERS="gateway:$(cat /run/secrets/ntfy_admin_hash):admin"
exec ntfy serve
