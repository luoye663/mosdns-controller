#!/usr/bin/env bash
set -euo pipefail

profile=${1:?profile is required}
output=${2:?output path is required}
root=$(dirname "$(dirname "$0")")

export MOSDNS_STATE_DIR="/var/lib/mosdns"
export QUERY_AUDIT_INCLUDE_ANSWERS=false

case "$profile" in
  compose)
    export MOSDNS_API_LISTEN="0.0.0.0:9091" MOSDNS_RULES_DIR="/etc/mosdns/rules" MOSDNS_TOKEN_FILE="/run/secrets/mosdns_control_token" DNS_LISTEN="0.0.0.0:53"
    export QUERY_AUDIT_ENDPOINT="http://controller:8081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=65536 QUERY_AUDIT_BATCH_SIZE=256 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=4096
    export DEFAULT_UPSTREAM_CONCURRENT=1 DEFAULT_UPSTREAM_SOCKS5="" DEFAULT_UPSTREAM_TAG="cloudflare_doh" DEFAULT_UPSTREAM_ADDR="https://cloudflare-dns.com/dns-query"
    ;;
  local)
    export MOSDNS_API_LISTEN="127.0.0.1:19091" MOSDNS_STATE_DIR="../.local/mosdns" MOSDNS_RULES_DIR="../deploy/mosdns/rules" MOSDNS_TOKEN_FILE="../.local/mosdns_control_token" DNS_LISTEN="0.0.0.0:5353"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:18081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=1024 QUERY_AUDIT_BATCH_SIZE=32 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=256
    export DEFAULT_UPSTREAM_CONCURRENT=1 DEFAULT_UPSTREAM_SOCKS5="" DEFAULT_UPSTREAM_TAG="google_doh" DEFAULT_UPSTREAM_ADDR="https://dns.google/dns-query"
    ;;
  integration)
    export MOSDNS_API_LISTEN="0.0.0.0:9091" MOSDNS_RULES_DIR="/etc/mosdns/rules" MOSDNS_TOKEN_FILE="/run/secrets/mosdns_control_token" DNS_LISTEN="0.0.0.0:5353"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:1/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=32 QUERY_AUDIT_BATCH_SIZE=8 QUERY_AUDIT_TIMEOUT="100ms" QUERY_AUDIT_RETRIES=0 CACHE_SIZE=64
    export DEFAULT_UPSTREAM_CONCURRENT=1 DEFAULT_UPSTREAM_SOCKS5="" DEFAULT_UPSTREAM_TAG="mock_default" DEFAULT_UPSTREAM_ADDR="udp://mock-default:53"
    ;;
  binary)
    export MOSDNS_API_LISTEN="127.0.0.1:9091" MOSDNS_RULES_DIR="/etc/mosdns-manager/mosdns/rules" MOSDNS_TOKEN_FILE="/etc/mosdns-manager/mosdns_control_token" DNS_LISTEN="0.0.0.0:53"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:8081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=65536 QUERY_AUDIT_BATCH_SIZE=256 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=4096
    export DEFAULT_UPSTREAM_CONCURRENT=1 DEFAULT_UPSTREAM_SOCKS5="" DEFAULT_UPSTREAM_TAG="cloudflare_doh" DEFAULT_UPSTREAM_ADDR="https://cloudflare-dns.com/dns-query"
    ;;
  *) echo "unknown mosdns configuration profile: $profile" >&2; exit 2 ;;
esac

mkdir -p "$(dirname "$output")"
envsubst '$MOSDNS_API_LISTEN $MOSDNS_STATE_DIR $MOSDNS_RULES_DIR $MOSDNS_TOKEN_FILE $QUERY_AUDIT_ENDPOINT $QUERY_AUDIT_QUEUE_SIZE $QUERY_AUDIT_BATCH_SIZE $QUERY_AUDIT_TIMEOUT $QUERY_AUDIT_RETRIES $QUERY_AUDIT_INCLUDE_ANSWERS $CACHE_SIZE $DEFAULT_UPSTREAM_CONCURRENT $DEFAULT_UPSTREAM_SOCKS5 $DEFAULT_UPSTREAM_TAG $DEFAULT_UPSTREAM_ADDR $DNS_LISTEN' < "$root/deploy/mosdns/config.yaml.tmpl" > "$output"
