MOSDNS_BASE := v5.3.4
LOCAL_DIR := .local
LOCAL_TOKEN := $(LOCAL_DIR)/mosdns_control_token
LOCAL_MOSDNS_CONFIG := deploy/local/mosdns.yaml
COMPOSE_MOSDNS_CONFIG := deploy/mosdns/config.yaml
INTEGRATION_MOSDNS_CONFIG := deploy/mosdns/config.integration.yaml
LOCAL_CONTROLLER_CONFIG := deploy/local/controller.yaml
COMPOSE_CONTROLLER_CONFIG := deploy/controller/config.yaml
BINARY_DEPLOY_DIR := deploy/binary
BINARY_PACKAGE_DIR := $(BINARY_DEPLOY_DIR)/package
BINARY_GOOS ?= $(shell go env GOOS)
BINARY_GOARCH ?= $(shell go env GOARCH)
BINARY_ARCHIVE := $(BINARY_DEPLOY_DIR)/mosdns-manager-$(BINARY_GOOS)-$(BINARY_GOARCH).tar.gz

.PHONY: build test race lint web-build web-embed configs compose-up compose-down local-init local-mosdns local-controller local-up local-create-admin local-clean binary-package binary-copy binary-clean

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
	rm -rf controller/internal/web/static/*
	cp -R web/dist/. controller/internal/web/static/

configs: $(COMPOSE_MOSDNS_CONFIG) $(LOCAL_MOSDNS_CONFIG) $(INTEGRATION_MOSDNS_CONFIG) $(COMPOSE_CONTROLLER_CONFIG) $(LOCAL_CONTROLLER_CONFIG)

$(COMPOSE_MOSDNS_CONFIG): deploy/mosdns/config.yaml.tmpl deploy/render-mosdns-config.sh
	bash deploy/render-mosdns-config.sh compose $@

$(LOCAL_MOSDNS_CONFIG): deploy/mosdns/config.yaml.tmpl deploy/render-mosdns-config.sh
	bash deploy/render-mosdns-config.sh local $@

$(INTEGRATION_MOSDNS_CONFIG): deploy/mosdns/config.yaml.tmpl deploy/render-mosdns-config.sh
	bash deploy/render-mosdns-config.sh integration $@

$(COMPOSE_CONTROLLER_CONFIG): deploy/controller/config.yaml.tmpl deploy/render-controller-config.sh
	bash deploy/render-controller-config.sh compose $@

$(LOCAL_CONTROLLER_CONFIG): deploy/controller/config.yaml.tmpl deploy/render-controller-config.sh
	bash deploy/render-controller-config.sh local $@

compose-up: $(COMPOSE_MOSDNS_CONFIG) $(COMPOSE_CONTROLLER_CONFIG)
	docker compose -f deploy/docker-compose.yml up --build

compose-down:
	docker compose -f deploy/docker-compose.yml down

# 本地模式使用回环端口和 .local/ 运行数据，不会占用生产 DNS 的 53 端口。
local-init:
	mkdir -p $(LOCAL_DIR)/mosdns $(LOCAL_DIR)/controller
	@if [ ! -f $(LOCAL_TOKEN) ]; then umask 077; openssl rand -hex 32 > $(LOCAL_TOKEN); fi
	chmod 600 $(LOCAL_TOKEN)

# 请在独立终端执行 make local-mosdns 和 make local-controller，或执行 make local-up。
local-mosdns: local-init $(LOCAL_MOSDNS_CONFIG)
	go -C mosdns run . start -c ../$(LOCAL_MOSDNS_CONFIG)

# 运行 controller
local-controller: local-init $(LOCAL_CONTROLLER_CONFIG)
	go -C controller run ./cmd/controller serve -config ../$(LOCAL_CONTROLLER_CONFIG)

local-up: local-init web-embed $(LOCAL_MOSDNS_CONFIG) $(LOCAL_CONTROLLER_CONFIG)
	@set -e; \
	go -C mosdns run . start -c ../$(LOCAL_MOSDNS_CONFIG) & mosdns_pid=$$!; \
	go -C controller run ./cmd/controller serve -config ../$(LOCAL_CONTROLLER_CONFIG) & controller_pid=$$!; \
	trap 'kill $$mosdns_pid $$controller_pid 2>/dev/null || true; wait $$mosdns_pid $$controller_pid 2>/dev/null || true' EXIT INT TERM; \
	wait $$mosdns_pid $$controller_pid

local-create-admin: local-init $(LOCAL_CONTROLLER_CONFIG)
	@printf "管理员用户名: "; IFS= read -r username; \
	printf "管理员密码: "; stty -echo; \
	trap 'stty echo; printf "\\n"' 0 1 2 15; \
	IFS= read -r password; stty echo; trap - 0 1 2 15; printf '\n'; \
	go -C controller run ./cmd/controller create-admin -config ../$(LOCAL_CONTROLLER_CONFIG) -username "$$username" -password "$$password"

# 仅删除本地开发数据；不会影响 Docker Compose 的命名卷。
local-clean:
	rm -rf $(LOCAL_DIR)

# 生成可传输的原生部署目录；包含二进制、配置、规则、systemd 单元和安装脚本。
binary-package: web-embed
	rm -rf $(BINARY_PACKAGE_DIR) $(BINARY_DEPLOY_DIR)/mosdns-manager-*.tar.gz
	install -d $(BINARY_PACKAGE_DIR)/bin $(BINARY_PACKAGE_DIR)/etc/mosdns/rules $(BINARY_PACKAGE_DIR)/etc/controller $(BINARY_PACKAGE_DIR)/systemd
	GOOS=$(BINARY_GOOS) GOARCH=$(BINARY_GOARCH) go -C mosdns build -trimpath -o ../$(BINARY_PACKAGE_DIR)/bin/mosdns .
	GOOS=$(BINARY_GOOS) GOARCH=$(BINARY_GOARCH) go -C controller build -trimpath -o ../$(BINARY_PACKAGE_DIR)/bin/controller ./cmd/controller
	bash deploy/render-mosdns-config.sh binary $(BINARY_PACKAGE_DIR)/etc/mosdns/config.yaml
	bash deploy/render-controller-config.sh binary $(BINARY_PACKAGE_DIR)/etc/controller/config.yaml
	install -m 0644 $(BINARY_DEPLOY_DIR)/mosdns.service $(BINARY_PACKAGE_DIR)/systemd/mosdns.service
	install -m 0644 $(BINARY_DEPLOY_DIR)/mosdns-controller.service $(BINARY_PACKAGE_DIR)/systemd/mosdns-controller.service
	install -m 0755 $(BINARY_DEPLOY_DIR)/install.sh $(BINARY_PACKAGE_DIR)/install.sh
	install -m 0644 $(BINARY_DEPLOY_DIR)/README.md $(BINARY_PACKAGE_DIR)/README.md
	tar -C $(BINARY_DEPLOY_DIR) -czf $(BINARY_ARCHIVE) package

# BINARY_HOST 例如 root@dns-host；可选 BINARY_REMOTE_DIR=/tmp/mosdns-manager。
binary-copy: binary-package
	@test -n "$(BINARY_HOST)" || { printf '%s\n' 'Set BINARY_HOST, for example: make binary-copy BINARY_HOST=root@dns-host'; exit 2; }
	rsync -az --delete $(BINARY_PACKAGE_DIR)/ $(BINARY_HOST):$(or $(BINARY_REMOTE_DIR),/tmp/mosdns-manager)/

binary-clean:
	rm -rf $(BINARY_PACKAGE_DIR) $(BINARY_DEPLOY_DIR)/*.tar.gz
