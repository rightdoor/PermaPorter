# AGENTS.md — 自动化协作约束

本文件面向在本仓库工作的自动化协作方（编码代理、CI 机器人等）。改动前先读 [docs/architecture.md](docs/architecture.md) 了解设计约束；所有描述以代码为准。

## 不可破坏的不变式

以下约束是结构性设计，任何改动（包括"顺手优化"）都不得破坏：

1. **五端口职责互斥**
   - 9090 控制台：只服务 SPA、`/static`、`/api`（公开/认证/管理）与聚合 `/healthz`、`/metrics`；不提供任何协议代理。
   - 9091 npm：只服务 Registry 协议（packument/tarball/audit 透传）+ 本口 `/healthz`/`/metrics`。
   - 9092 GOPROXY：只服务协议端点（`@v/list`、`@latest`、`.info/.mod/.zip/.ziphash`、`/sumdb/*`）+ 本口 `/healthz`/`/metrics`。
   - 9093 PyPI Simple：只服务协议端点（`/simple/`、`/simple/<project>/`、`/packages/<file>`）+ 本口 `/healthz`/`/metrics`。
   - 9094 Maven 仓库：只服务协议端点（构件级/版本级 `maven-metadata.xml`、`<group>/<artifact>/<version>/<file>` 构件文件、`<资源>.sha1/.md5/.sha256/.sha512` 校验和）+ 本口 `/healthz`/`/metrics`。
   - 协议端口不得注册 `/api`、`/admin`、`/static` 等任何 UI 数据接口。
2. **错误格式按端口隔离**：9091 全部 JSON `{"error": …}`；9092、9093 与 9094 全部 `text/plain; charset=utf-8`；9090 浏览器回落 SPA 外壳、API 形态 JSON 404。不得为了统一而改变任何一侧的格式或状态码。
3. **控制台聚合必须进程内直调**：`core/web/console.go` 的 `capture()` 机制直接调用注入的生态 handler；严禁对 9091/9092/9093/9094 发起 HTTP 反代。
4. **四套清理策略隔离**：go 清理器绝不删除 `@v/list`、`latest.json` 与仅元数据模块；npm 清理器删除仅元数据包；py 清理器绝不删除项目记录与仅元数据项目（锚点页缓存）；mvn 清理器绝不删除构件记录、metadata 缓存与仅元数据构件。四套清理器语义不同，不得通用化或互相套用。
5. **单库 + 四棵存储树隔离**：全部数据在一个单库（SQLite 默认或 PostgreSQL 可选，双方言收敛在 `internal/core/database`——`sqlite` DSN 或 PG URL 经配置文件层解析为 `DBDSN`；表名 `go_*`/`npm_*`/`py_*`/`mvn_*` 前缀隔离，查询层入口带表白名单断言）；四棵存储树两两互异且互不为父子（`config.Validate` 强制）；adapter 之间不得读写对方前缀的表或对方的存储目录；模块路径/包名/文件名拼磁盘路径前必须经过既有校验函数（防路径穿越）。**方言逻辑不得泄漏到 adapter 层与协议层**：占位符改写、类型映射、`LIKE→ILIKE`、`SUM::int8` 等全部在 `core/database` 的改写器与 PG 驱动包装器内完成，adapter 面向双方言兼容 SQL。
6. **启动校验即拒绝启动**：`Validate()` 返回错误时进程必须退出且不监听任何端口；不得放宽或绕过。单库 `schema_version` 已有组件版本不匹配同样拒绝启动（新增生态组件按可加性演进自动补齐，见 `core/database/schema.go`）。**启动判定顺序**（`config.Bootstrap`）：配置文件解析失败 → 拒绝启动；`UNATTENDED=1` → 无头正常启动（数据库未配置回落默认 SQLite）；数据库已配置（`[database].driver` 非空）→ 正常启动，任一校验失败（字段缺失/端口冲突/连接不可达/schema 不匹配）→ 拒绝启动；数据库未配置 → **初始化模式**（合法状态：仅监听 9090 的 SPA + 向导，非错误）。「未配置 → 初始化模式」与「配置错误 → 拒绝启动」语义固定，任何"配置不完整"不得静默进入初始化模式。
7. **只读协议**：协议端口仅接受 GET/HEAD（sumdb 透传同样）；发布类请求保持 405。
8. **产物不可变**：落盘产物（`.zip/.tgz/.mod/.info/.ziphash`、meta JSON、发行文件）不得原地改写；写入必须走临时文件 + 原子改名；校验链（gzip/SRI、zip/h1、py sha256）不得省略。配置文件 `permaporter.toml` 写回同样走临时文件 + 原子改名（权限 0600：PG 密码明文存储）。
9. **配置文件层与向导**：启动必需项（数据库与五端口）优先取 `CONFIG_DIR/permaporter.toml`（默认 `./config`），取值优先级**文件 > 环境变量 > 默认值**；`driver` 缺失或为空即"数据库未配置"（初始化模式唯一判定，不引入额外标志）。初始化向导端点（`/api/setup/status|test-database|initialize`）**仅注册在 9090** 且只在初始化模式存在（完成或重启后关闭）；向导 initialize 在进程内原地启动完整后端（替换 9090 处理器 + 启动四个协议端口），不得依赖进程重启。

## 改动边界

- 新功能与重构不得改变现有 API 契约：路径、方法、状态码、Content-Type、响应字段、数据库 schema、存储目录布局、环境变量语义（新增生态组件按可加性演进除外，见 `core/database/schema.go`）。
- `core` 不 import 任何 adapter；adapter 之间互不 import；共享能力下沉 `core`，生态私有能力留在 adapter（判据见 [docs/development.md](docs/development.md)）。
- 会话与登录限流实例由 `cmd/permaporter` 创建并注入，不得在 adapter 内另建。
- 前端不得硬编码协议端口；下载地址一律使用服务端注入的 `goProxy`/`npmRegistry`/`pyIndex` 基地址。

## 提交规范

- 格式：`<type>: <概述>`，type ∈ `feat / fix / docs / refactor / test / chore / build`。
- 一次提交一件事；提交前不引入与本次目的无关的格式化噪音。
- 提交信息与代码注释中不引用任何其他项目、仓库或历史来源。

## 验证清单（每次改动后逐项执行）

```bash
go build ./...
go test ./...          # 或 make test
go vet ./...
(cd frontend && pnpm build)
```

- 涉及协议行为的改动：本地起服务，实测 9090 `/healthz`、9091 packument、9092 `@v/list`、9093 `/simple/`、9094 `maven-metadata.xml`，并确认 9091/9092/9093/9094 访问 `/admin` 返回各自格式的 404。
- 涉及文档的改动：核对文档中出现的路径、端口、环境变量、路由均与代码一致；不描述代码未实现的能力。
- 涉及 `.gitignore` 之外的文件增删：确认 `git status` 干净、无运行数据入库。
