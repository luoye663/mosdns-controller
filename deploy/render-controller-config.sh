#!/usr/bin/env bash
set -euo pipefail

profile=${1:?profile is required}
output=${2:?output path is required}
root=$(dirname "$(dirname "$0")")

case "$profile" in
  compose)
    export CONTROLLER_PUBLIC_LISTEN="0.0.0.0:8080" CONTROLLER_INTERNAL_LISTEN="0.0.0.0:8081"
    export CONTROLLER_DB_PATH="/var/lib/mosdns-controller/controller.db" CONTROLLER_MOSDNS_BASE_URL="http://mosdns:9091" CONTROLLER_MOSDNS_TOKEN_FILE="/run/secrets/mosdns_control_token" CONTROLLER_SECURE_COOKIE=false
    ;;
  local)
    export CONTROLLER_PUBLIC_LISTEN="127.0.0.1:18080" CONTROLLER_INTERNAL_LISTEN="127.0.0.1:18081"
    export CONTROLLER_DB_PATH="../.local/controller/controller.db" CONTROLLER_MOSDNS_BASE_URL="http://127.0.0.1:19091" CONTROLLER_MOSDNS_TOKEN_FILE="../.local/mosdns_control_token" CONTROLLER_SECURE_COOKIE=false
    ;;
  binary)
    export CONTROLLER_PUBLIC_LISTEN="0.0.0.0:8080" CONTROLLER_INTERNAL_LISTEN="127.0.0.1:8081"
    export CONTROLLER_DB_PATH="/var/lib/mosdns-controller/controller.db" CONTROLLER_MOSDNS_BASE_URL="http://127.0.0.1:9091" CONTROLLER_MOSDNS_TOKEN_FILE="/etc/mosdns-manager/mosdns_control_token" CONTROLLER_SECURE_COOKIE=false
    ;;
  *) echo "unknown controller configuration profile: $profile" >&2; exit 2 ;;
esac

mkdir -p "$(dirname "$output")"
envsubst '$CONTROLLER_PUBLIC_LISTEN $CONTROLLER_INTERNAL_LISTEN $CONTROLLER_DB_PATH $CONTROLLER_MOSDNS_BASE_URL $CONTROLLER_MOSDNS_TOKEN_FILE $CONTROLLER_SECURE_COOKIE' < "$root/deploy/controller/config.yaml.tmpl" > "$output"
