# PermaPorter 构建与打包（单二进制 · 五端口：9090 控制台 / 9091 npm / 9092 GOPROXY / 9093 PyPI / 9094 Maven）
#
# 用法：
#   make            # 等同 make help
#   make package    # 产出 dist/permaporter-<版本>-linux-amd64.tar.gz
#   make release    # 先重建前端再打包（需要 pnpm）
#
# Windows 请在 Git Bash 中执行（依赖 POSIX 工具：mkdir/cp/tar/sha256sum）。

ifeq ($(OS),Windows_NT)
  SHELL := bash
  EXE   := .exe
else
  EXE   :=
endif

APP     := permaporter
# 版本号唯一来源：升级版本只修改这里，所有构建入口统一使用该值。
VERSION := 1.0.1
GOOS    ?= linux
GOARCH  ?= amd64
DIST    := dist
PKG     := $(APP)-$(VERSION)-$(GOOS)-$(GOARCH)
LDFLAGS := -s -w -X permaporter/internal/config.Version=$(VERSION)

# 镜像名与仓库地址可按需覆盖
IMAGE   ?= permaporter:$(VERSION)

SHA256  := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

.DEFAULT_GOAL := help

.PHONY: help deps frontend frontend-assets frontend-fresh build build-linux package release test test-race vet lint fmt tidy check run db-reset clean docker-build docker-up

help: ## 显示可用目标
	@echo "permaporter v$(VERSION)  ($(GOOS)/$(GOARCH))"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

deps: ## 下载 Go 依赖
	go mod download

frontend: ## 重建前端产物到 internal/core/webui/assets（需要 pnpm）
	cd frontend && pnpm install --frozen-lockfile && pnpm build

# 开发类目标（test/vet/run/tidy）：产物缺失时才构建，已有则复用。
frontend-assets:
	@test -f internal/core/webui/assets/.vite/manifest.json || "$(MAKE)" frontend

# 发布构建：无条件从零重建前端，保证 frontend/public（如 logo）与代码改动即时进入二进制。
frontend-fresh:
	@rm -rf internal/core/webui/assets
	@"$(MAKE)" frontend

build: frontend-fresh ## 构建当前平台二进制到 dist/permaporter
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(APP)$(EXE) ./cmd/permaporter
# 产物目录只存在于构建期
	@rm -rf internal/core/webui/assets
	@echo "built: $(DIST)/$(APP)$(EXE)"

build-linux: frontend-fresh ## 交叉编译并打包 dist/permaporter-<版本>-<GOOS>-<GOARCH>.tar.gz
	@mkdir -p $(DIST)/$(PKG)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(DIST)/$(PKG)/$(APP) ./cmd/permaporter
	@cp build/package/permaporter.service $(DIST)/$(PKG)/
	@cp build/package/permaporter.env.example $(DIST)/$(PKG)/
	@cp build/package/install.sh $(DIST)/$(PKG)/
	@cp build/package/uninstall.sh $(DIST)/$(PKG)/
	@chmod 0755 $(DIST)/$(PKG)/install.sh $(DIST)/$(PKG)/uninstall.sh
	@cp README.md $(DIST)/$(PKG)/README.md
	@cd $(DIST) && tar -czf $(PKG).tar.gz $(PKG) && sha256sum $(PKG).tar.gz > $(PKG).tar.gz.sha256
	@echo "packaged: $(DIST)/$(PKG).tar.gz"

package: build-linux ## 打包发布产物

release: frontend-fresh build ## 重建前端并构建当前平台

test: ## 运行全部测试（UNATTENDED=1 无头模式：跳过初始化向导判定）
	UNATTENDED=1 go test -count=1 ./...

test-race: ## 竞态检测
	UNATTENDED=1 go test -race -count=1 ./...

vet: ## 静态检查
	go vet ./...

tidy: ## 整理依赖
	go mod tidy

run: frontend-assets ## 本地运行（UNATTENDED=1 无头模式：默认 9090-9094，可用 UI_PORT/NPM_PORT/GO_PORT/PY_PORT/MVN_PORT 覆盖；去掉该变量可体验初始化向导）
	UNATTENDED=1 go run ./cmd/permaporter

db-reset: ## 开发期重置：删除单库文件（先停止服务；库内数据全部丢弃，下次启动重建）
	rm -f data/permaporter.db
	@echo "数据库已重置：data/permaporter.db 已删除，下次启动将重新建库"

docker-build: ## 构建镜像（需 VERSION）
	docker build -f build/docker/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE) .

docker-up: ## 本地起容器（对外 9090-9094）
	docker run --rm -p 9090:9090 -p 9091:9091 -p 9092:9092 -p 9093:9093 -p 9094:9094 -v permaporter-data:/app/data $(IMAGE)

clean: ## 清理构建产物
	rm -rf $(DIST)
	rm -rf internal/core/webui/assets
