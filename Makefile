MOSDNS_BASE := v5.3.4
LOCAL_DIR := .local
LOCAL_TOKEN := $(LOCAL_DIR)/mosdns_control_token
LOCAL_MOSDNS_CONFIG := deploy/local/mosdns.yaml
LOCAL_CONTROLLER_CONFIG := deploy/local/controller.yaml

.PHONY: build test race lint web-build web-embed compose-up compose-down local-init local-mosdns local-controller local-up local-create-admin local-clean

build: web-embed
	go -C mosdns build ./...
	go -C controller build ./...

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

# web-dev: 运行 WebUI 开发服务器，支持热更新。
web-dev:
	npm --prefix web run dev

web-build: web-embed

# controller 通过 go:embed 提供生产 WebUI；本地 Go 构建前必须同步最新 dist。
web-embed:
	npm --prefix web run build
	rm -rf controller/internal/web/static
	mkdir -p controller/internal/web/static
	cp -R web/dist/. controller/internal/web/static/

compose-up:
	docker compose -f deploy/docker-compose.yml up --build

compose-down:
	docker compose -f deploy/docker-compose.yml down

# 本地模式使用回环端口和 .local/ 运行数据，不会占用生产 DNS 的 53 端口。
local-init:
	mkdir -p $(LOCAL_DIR)/mosdns $(LOCAL_DIR)/controller
	@if [ ! -f $(LOCAL_TOKEN) ]; then umask 077; openssl rand -hex 32 > $(LOCAL_TOKEN); fi
	chmod 600 $(LOCAL_TOKEN)

# 请在独立终端执行 make local-mosdns 和 make local-controller，或执行 make local-up。
local-mosdns: local-init
	go -C mosdns run . start -c ../$(LOCAL_MOSDNS_CONFIG)

# 运行 controller
local-controller: local-init
	go -C controller run ./cmd/controller serve -config ../$(LOCAL_CONTROLLER_CONFIG)

local-up: local-init web-embed
	@set -e; \
	go -C mosdns run . start -c ../$(LOCAL_MOSDNS_CONFIG) & mosdns_pid=$$!; \
	go -C controller run ./cmd/controller serve -config ../$(LOCAL_CONTROLLER_CONFIG) & controller_pid=$$!; \
	trap 'kill $$mosdns_pid $$controller_pid 2>/dev/null || true; wait $$mosdns_pid $$controller_pid 2>/dev/null || true' EXIT INT TERM; \
	wait $$mosdns_pid $$controller_pid

local-create-admin: local-init
	@printf "管理员用户名: "; IFS= read -r username; \
	printf "管理员密码: "; stty -echo; \
	trap 'stty echo; printf "\\n"' 0 1 2 15; \
	IFS= read -r password; stty echo; trap - 0 1 2 15; printf '\n'; \
	go -C controller run ./cmd/controller create-admin -config ../$(LOCAL_CONTROLLER_CONFIG) -username "$$username" -password "$$password"

# 仅删除本地开发数据；不会影响 Docker Compose 的命名卷。
local-clean:
	rm -rf $(LOCAL_DIR)
