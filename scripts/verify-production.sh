#!/usr/bin/env bash
# 在发布镜像前执行本地静态检查；不会生成或读取真实 secret。
set -euo pipefail

grep -q 'MOSDNS_BASE := v5.3.4' Makefile
grep -q '"53:53/udp"' deploy/docker-compose.yml
grep -q '"53:53/tcp"' deploy/docker-compose.yml
grep -q '"8080:8080/tcp"' deploy/docker-compose.yml
if grep -Eq '(^|[^0-9])9091:|(^|[^0-9])8081:' deploy/docker-compose.yml; then
  printf '%s\n' 'internal ports must not be published' >&2
  exit 1
fi
test -f deploy/secrets/.gitkeep
if ! git check-ignore -q deploy/secrets/mosdns_control_token; then
  printf '%s\n' 'runtime secret must remain ignored by Git' >&2
  exit 1
fi
go -C controller test ./...
go -C mosdns test ./plugin/executable/dynamic_rule_engine/... ./plugin/executable/query_audit/...
npm --prefix web run typecheck
