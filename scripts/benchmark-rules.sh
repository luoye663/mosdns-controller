#!/usr/bin/env bash
# 规则规模基准只测不可变快照匹配；结果必须保存到验收报告，禁止事后估算。
set -euo pipefail

count="${COUNT:-3}"
go -C mosdns test -run '^$' -bench 'BenchmarkMatch(100K|200K)Domains' -benchmem -count "$count" ./plugin/executable/dynamic_rule_engine
