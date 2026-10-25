# PermaPorter 开发指南

## 本地开发环境

依赖：Go ≥ 1.27、pnpm ≥ 10、Node ≥ 22（前端构建）、GNU make（Windows 下在 Git Bash 中执行）。

### 后端启动

```bash
make run            # 等同 go run ./cmd/permaporter（产物缺失时自动构建前端）
# 或直接：
go run ./cmd/permaporter
```

默认监听 `0.0.0.0:9090/9091/9092/9093/9094`，数据写在 `./data/`（单库 permaporter.db + 四棵存储树）。可用环境变量覆盖：

```bash
UI_PORT=19090 NPM_PORT=19091 GO_PORT=19092 PY_PORT=19093 MVN_PORT=19094 go run ./cmd/permaporter
```

### 前端 dev server 与代理规则

```bash
cd frontend
pnpm install
pnpm dev            # Vite dev server，端口 5173
```

[vite.config.js](../frontend/vite.config.js) 中 `BACKEND = http://127.0.0.1:${UI_PORT:-9090}`（固定 IPv4，与 Go 的 `0.0.0.0` 监听对齐），代理规则：

| dev 请求 | 转发到 | 说明 |
| :--- | :--- | :--- |
| `/api/**` | 9090 | JSON API（会话 Cookie 同源，无 CORS 问题） |
| `/static/**` | 9090 | logo 等静态资源（Go 内嵌产物） |
| `/healthz` | 9090 | 健康检查 |

后端未启动时代理返回 `502 {"error": "后端不可达（…）"}`，而不是裸 500——看到它先确认后端是否已在跑。首页 logo 在 dev 期由 Vite 直接从 `frontend/public/` 提供（`LOGO_URL` 取 `/logo.png`），不依赖后端，因此后端未起时只有接口报错、图片不会裂。

tarball/zip 下载链接由服务端注入 9091/9092 绝对地址，dev 不经代理。生产构建由 Go 服务端输出 HTML 外壳（`internal/core/webui/manifest.go`），`frontend/index.html` 仅作 dev server 入口。

### 多端口联调方式

- 前后端分离调试：后端 `make run`（9090-9094 五端口全部启动）+ 前端 `pnpm dev`（5173）。浏览器访问 `http://localhost:5173`，页面走 dev server；协议端口（9091/9092/9093/9094）直接用 curl / go / npm / pip / mvn 客户端实测。
- 仅后端调试：`make build` 后运行 `dist/permaporter`，前端产物已 embed，直接访问 9090。
- 改了前端想立即在 embed 产物里生效：`make release`（无条件重建前端 + 构建），或开发期用 `pnpm watch` 配合 `go build`。

## 目录结构

```
cmd/permaporter/      服务入口：配置加载、装配、五监听器、优雅退出
internal/config/      环境变量配置与启动校验
internal/core/        平台无关内核：单库数据层（permaporter.db）、会话/限流、控制台聚合、访问日志、
                      上游 HTTP 客户端、环形日志、通用工具（markdown/版本比较/磁盘）
internal/core/webui/  前端构建产物 embed（构建时生成，不入库）
internal/goadapter/   go 生态适配器：GOPROXY 协议引擎、go_* 前缀表查询层、清理、预热、
                      协议端口路由
internal/npmadapter/  npm 生态适配器：Registry 协议引擎、npm_* 前缀表查询层、清理、预热、
                      协议端口路由
internal/pyadapter/   py 生态适配器：PyPI Simple 协议引擎、py_* 前缀表查询层、清理、预热、
                      协议端口路由
internal/mvnadapter/  mvn 生态适配器：Maven 仓库协议引擎、mvn_* 前缀表查询层、清理、预热、
                      协议端口路由（9094）
frontend/             Vue 3 + Vite 前端源码（构建输出到 internal/core/webui/assets）
build/docker/         容器镜像 Dockerfile
build/nginx/          容器内 nginx 反代配置（五 server 块）
build/package/        systemd 单元、环境变量模板、安装脚本
docs/                 架构 / API / 配置 / 部署 / 开发 / 单库说明文档
```

## 构建与产物

| 命令 | 产物 |
| :--- | :--- |
| `make frontend` | 前端产物 → `internal/core/webui/assets/`（含 `.vite/manifest.json`，Go embed 引用；该目录不入库） |
| `make build` | 当前平台二进制 `dist/permaporter`（`frontend-fresh`：无条件重建前端） |
| `make build-linux` / `make package` | `dist/permaporter-<VERSION>-<GOOS>-<GOARCH>.tar.gz` + `.sha256`（含 service/env/install.sh/README） |
| `make release` | `frontend-fresh` + `build`（当前平台） |
| `make docker-build` | 镜像（`--build-arg VERSION` 必填） |
| `make db-reset` | 开发期重置：删除 `data/permaporter.db`（先停服务；库内数据全部丢弃，见 [single-db.md](single-db.md)） |

### 版本号注入

版本号唯一来源是 [Makefile](../Makefile) 的 `VERSION` 变量，所有构建入口统一使用：

```make
LDFLAGS := -s -w -X permaporter/internal/config.Version=$(VERSION)
```

`internal/config.Version` 默认值为 `dev`（直接 `go build`/`go run` 时）。升级版本只改 Makefile 一处；`permaporter -version`、`/healthz` 的 `version` 字段、指标 `*_build_info{version=…}`、页面页脚均读取注入值。

## 测试与静态检查

```bash
make test           # go test -count=1 ./...
make test-race      # go test -race -count=1 ./...
make vet            # go vet ./...
make tidy           # go mod tidy（go.mod/go.sum 保持 tidy）
make clean          # 删除 dist/ 与 internal/core/webui/assets/
cd frontend && pnpm lint   # ESLint
cd frontend && pnpm build  # 前端构建（产物写入 internal/core/webui/assets）
```

提交前最小验证集：`go build ./... && go test ./... && (cd frontend && pnpm build)`。

## 代码组织约定

### 什么代码属于 core

`internal/core/` 收纳平台无关、可被各生态复用的部分：

- `config`：进程级环境变量配置与启动校验。
- `database`：单库 Store（`data/permaporter.db`：建库/连接池/`schema_version` 校验/平台凭据）。
- `middleware`：会话存储与登录限流（**唯一实例由 cmd 创建并注入四个 adapter**，不允许 adapter 自建）。
- `web`：控制台聚合层（9090 全部路由、进程内 `capture()` 聚合）、访问日志、X-Response-Time、内嵌静态资源、pprof、会话/限流后台回收。
- `webui`：前端产物 embed 与 SPA 外壳 HTML。
- `upstream` / `logring` / `utils`：上游 HTTP 客户端、环形请求日志、Bcrypt/markdown/版本比较/磁盘空间等工具。

### 什么必须留在 adapter

与特定生态协议、schema、存储布局绑定的代码留在 `internal/goadapter/`、`internal/npmadapter/`、`internal/pyadapter/`、`internal/mvnadapter/`，且互相之间不 import：

- `proxy`：协议引擎（GOPROXY 的 list/latest/info/mod/zip/ziphash/sumdb；npm 的 packument/tarball/改写/完整性校验；py 的 simple 锚点页/发行文件/sha256 校验；mvn 的 metadata/构件文件/校验和）、各自的错误格式与缓存策略。
- `database`：挂载在单库 Store 上的本生态前缀表（`go_*`/`npm_*`/`py_*`/`mvn_*`）查询层：CRUD、物化统计、统计落库与表白名单断言（连接池由 Store 统一持有，adapter 不得另开）。
- `cleanup`：各自的清理规则（四套清理器语义不同、不得通用化复用，见 [architecture.md](architecture.md)）。
- `preheat`：各自的预热输入解析（go.mod/go.sum、package.json、requirements.txt、pom.xml/Gradle/坐标清单）与任务调度。
- `web`：协议端口路由注册（只挂 `/healthz`、`/metrics` 与 NoRoute 协议分发，不注册任何 UI 路由）与后台清理调度。

**红线**：控制台聚合严禁对 9091/9092/9093/9094 发 HTTP 反代；协议端口不注册 UI/API 路由；会话与限流不得在 adapter 内另建实例；各生态的前缀表、存储树、清理器、错误格式不得互通复用。

### 新增一个生态需要动哪些位置

1. 新建 `internal/<name>adapter/`：`models`、`database`（前缀表查询层/统计）、`proxy`（协议引擎）、`cleanup`、`preheat`、`web`（协议路由装配 + 清理调度），对齐现有四套 adapter 的形态。
2. `internal/core/database/schema.go`：新增生态前缀表与 `schema_version` 组件；`internal/config/config.go`：新增端口、存储目录、默认上游变量，并把新增的端口/存储目录纳入 `Validate()` 互异校验。
3. `internal/core/web/console.go`：在 `Console` 增加生态句柄字段与聚合接口（`GoEcosystem`/`NpmEcosystem` 同构接口），`[eco]` 分派扩展。
4. `cmd/permaporter/main.go`：装配新 App（挂载同一单库）、`StartStatsFlush`（错峰相位）、新增引擎与监听器、退出顺序补齐。
5. 前端：`frontend/src/views` 增加浏览/详情页，`router/index.js` 增加路由，`TopNav`/管理页按需接入；指标名/健康检查沿用 `<eco>_permaporter_*` 约定。
6. 文档：README 端口表、`docs/api.md`、`docs/architecture.md`、`.env.example`、`build/package/permaporter.env.example` 同步补充。

## 提交规范

- 提交信息格式：`<type>: <概述>`，type 取 `feat / fix / docs / refactor / test / chore / build`；概述用祈使句、不加句号。
- 一次提交只做一件事；行为变更与文档变更分开提交。
- 禁止把运行数据（`data/`）、构建产物（`dist/`、`internal/core/webui/assets/`、`node_modules/`）提交入库（.gitignore 已覆盖）。
