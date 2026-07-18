#!/usr/bin/env bash
set -euo pipefail

profile=${1:?profile is required}
output=${2:?output path is required}
root=$(dirname "$(dirname "$0")")

export MOSDNS_STATE_DIR="/var/lib/mosdns"
export CACHE_DUMP_INTERVAL=600
export QUERY_AUDIT_INCLUDE_ANSWERS=false

case "$profile" in
  compose)
    export MOSDNS_API_LISTEN="0.0.0.0:9091" MOSDNS_RULES_DIR="/etc/mosdns/rules" MOSDNS_TOKEN_FILE="/run/secrets/mosdns_control_token" DNS_LISTEN="0.0.0.0:53"
    export QUERY_AUDIT_ENDPOINT="http://controller:8081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=65536 QUERY_AUDIT_BATCH_SIZE=256 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=4096
    export LOCAL_CONCURRENT=2 LOCAL_SOCKS5="" LOCAL_UPSTREAM_1_TAG="alidns_doh" LOCAL_UPSTREAM_1_ADDR="https://223.5.5.5/dns-query" LOCAL_UPSTREAM_2_TAG="dnspod_doh" LOCAL_UPSTREAM_2_ADDR="https://1.12.12.12/dns-query"
    export REMOTE_CONCURRENT=1 REMOTE_SOCKS5="" REMOTE_UPSTREAM_TAG="remote_gateway" REMOTE_UPSTREAM_ADDR="https://cloudflare-dns.com/dns-query"
    ;;
  local)
    export MOSDNS_API_LISTEN="127.0.0.1:19091" MOSDNS_STATE_DIR="../.local/mosdns" MOSDNS_RULES_DIR="../deploy/mosdns/rules" MOSDNS_TOKEN_FILE="../.local/mosdns_control_token" DNS_LISTEN="0.0.0.0:5353"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:18081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=1024 QUERY_AUDIT_BATCH_SIZE=32 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=256
    export LOCAL_CONCURRENT=1 LOCAL_SOCKS5="" LOCAL_UPSTREAM_1_TAG="local-google" LOCAL_UPSTREAM_1_ADDR="https://dns.google/dns-query" LOCAL_UPSTREAM_2_TAG="local-cloudflare" LOCAL_UPSTREAM_2_ADDR="https://cloudflare-dns.com/dns-query"
    export REMOTE_CONCURRENT=1 REMOTE_SOCKS5="" REMOTE_UPSTREAM_TAG="remote-cloudflare" REMOTE_UPSTREAM_ADDR="https://cloudflare-dns.com/dns-query"
    ;;
  integration)
    export MOSDNS_API_LISTEN="0.0.0.0:9091" MOSDNS_RULES_DIR="/etc/mosdns/rules" MOSDNS_TOKEN_FILE="/run/secrets/mosdns_control_token" DNS_LISTEN="0.0.0.0:5353"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:1/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=32 QUERY_AUDIT_BATCH_SIZE=8 QUERY_AUDIT_TIMEOUT="100ms" QUERY_AUDIT_RETRIES=0 CACHE_SIZE=64
    export LOCAL_CONCURRENT=1 LOCAL_SOCKS5="" LOCAL_UPSTREAM_1_TAG="mock-local" LOCAL_UPSTREAM_1_ADDR="udp://mock-local:53" LOCAL_UPSTREAM_2_TAG="mock-local-backup" LOCAL_UPSTREAM_2_ADDR="udp://mock-local:53"
    export REMOTE_CONCURRENT=1 REMOTE_SOCKS5="" REMOTE_UPSTREAM_TAG="mock-remote" REMOTE_UPSTREAM_ADDR="udp://mock-remote:53"
    ;;
  binary)
    export MOSDNS_API_LISTEN="127.0.0.1:9091" MOSDNS_RULES_DIR="/etc/mosdns-manager/mosdns/rules" MOSDNS_TOKEN_FILE="/etc/mosdns-manager/mosdns_control_token" DNS_LISTEN="0.0.0.0:53"
    export QUERY_AUDIT_ENDPOINT="http://127.0.0.1:8081/internal/v1/query-events/batch" QUERY_AUDIT_QUEUE_SIZE=65536 QUERY_AUDIT_BATCH_SIZE=256 QUERY_AUDIT_TIMEOUT="2s" QUERY_AUDIT_RETRIES=1 CACHE_SIZE=4096
    export LOCAL_CONCURRENT=2 LOCAL_SOCKS5="" LOCAL_UPSTREAM_1_TAG="alidns_doh" LOCAL_UPSTREAM_1_ADDR="https://223.5.5.5/dns-query" LOCAL_UPSTREAM_2_TAG="dnspod_doh" LOCAL_UPSTREAM_2_ADDR="https://1.12.12.12/dns-query"
    export REMOTE_CONCURRENT=1 REMOTE_SOCKS5="" REMOTE_UPSTREAM_TAG="remote_gateway" REMOTE_UPSTREAM_ADDR="<REMOTE_DOH_URL>"
    ;;
  *) echo "unknown mosdns configuration profile: $profile" >&2; exit 2 ;;
esac

mkdir -p "$(dirname "$output")"
envsubst '$MOSDNS_API_LISTEN $MOSDNS_STATE_DIR $MOSDNS_RULES_DIR $MOSDNS_TOKEN_FILE $QUERY_AUDIT_ENDPOINT $QUERY_AUDIT_QUEUE_SIZE $QUERY_AUDIT_BATCH_SIZE $QUERY_AUDIT_TIMEOUT $QUERY_AUDIT_RETRIES $QUERY_AUDIT_INCLUDE_ANSWERS $CACHE_SIZE $CACHE_DUMP_INTERVAL $LOCAL_CONCURRENT $LOCAL_SOCKS5 $LOCAL_UPSTREAM_1_TAG $LOCAL_UPSTREAM_1_ADDR $LOCAL_UPSTREAM_2_TAG $LOCAL_UPSTREAM_2_ADDR $REMOTE_CONCURRENT $REMOTE_SOCKS5 $REMOTE_UPSTREAM_TAG $REMOTE_UPSTREAM_ADDR $DNS_LISTEN' < "$root/deploy/mosdns/config.yaml.tmpl" > "$output"
