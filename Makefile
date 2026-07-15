MOSDNS_BASE := v5.3.4

.PHONY: build test race lint web-build compose-up compose-down

build:
	go -C mosdns build ./...
	go -C controller build ./...
	npm --prefix web run build

test:
	go -C mosdns test ./...
	go -C controller test ./...
	npm --prefix web run typecheck

race:
	go -C mosdns test -race ./plugin/executable/dynamic_rule_engine/...
	go -C mosdns test -race ./plugin/executable/query_audit/...
	go -C controller test -race ./...

lint:
	go -C mosdns vet ./...
	go -C controller vet ./...
	npm --prefix web run typecheck

web-build:
	npm --prefix web run build

compose-up:
	docker compose -f deploy/docker-compose.yml up --build

compose-down:
	docker compose -f deploy/docker-compose.yml down
