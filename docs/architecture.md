# PermaPorter 架构

## 组件与依赖方向

```
cmd/permaporter (main)
  │  装配：config → core 单例（单库 Store）→ 四个 adapter → 五个 gin 引擎 → 五个 http.Server
  ▼
┌─────────────────────────── internal/core ────────────────────────────┐
│ config      环境变量配置与启动校验                                      │
│ database    单库 data/permaporter.db（Store：建库/连接池/平台凭据/可加性演进）│
│ middleware  会话存储 SessionStore / 登录限流 LoginLimiter（唯一实例）    │
│ web         控制台聚合层（9090 全部路由）、访问日志、静态资源、pprof     │
│ webui       前端产物 embed + SPA 外壳 HTML 生成                        │
│ upstream    按出站代理地址缓存的 http.Client（拨号/TLS/响应头超时）      │
│ logring     代理请求环形日志（每生态一枚，容量 500）                    │
│ utils       Bcrypt、markdown 渲染、版本比较、磁盘剩余空间              │
└──────────────────────────────────────────────────────────────────────┘
        ▲ 句柄注入（core 不 import 任何 adapter）
┌────────────── internal/goadapter ──────────────┐  ┌───────────── internal/npmadapter ─────────────┐
│ proxy     GOPROXY 引擎（list/latest/info/mod/  │  │ proxy     Registry 引擎（packument/tarball/    │
│           zip/ziphash/sumdb + 校验链）          │  │           改写/完整性校验）                     │
│ database  go_* 前缀表查询层（挂载单库 Store）    │  │ database  npm_* 前缀表查询层（挂载单库 Store）  │
│ cleanup   存储清理（安全动作集）                 │  │ cleanup   存储清理（孤立文件/仅元数据包）        │
│ preheat   预热任务管理（4 并发，保留 5 个任务）   │  │ preheat   预热任务管理（4 并发，保留 5 个任务）  │
│ web       协议端口路由装配 + 后台清理调度        │  │ web       协议端口路由装配 + 后台清理调度       │
└─────────────────────────────────────────────────┘  └────────────────────────────────────────────────┘
┌────────────── internal/pyadapter ──────────────┐  ┌───────────── internal/mvnadapter ──────────────┐
│ proxy     PyPI Simple 引擎（锚点页/发行文件/    │  │ proxy     Maven 仓库引擎（metadata/构件文件/    │
│           sha256 校验链）                       │  │           校验和端点）                          │
│ database  py_* 前缀表查询层（挂载单库 Store）    │  │ database  mvn_* 前缀表查询层（挂载单库 Store）   │
│ cleanup   存储清理（复位/孤立回收）              │  │ cleanup   存储清理（复位/孤立/空目录剪枝）       │
│ preheat   预热任务管理（4 并发，保留 5 个任务）   │  │ preheat   预热任务管理（4 并发，保留 5 个任务）  │
│ web       协议端口路由装配 + 后台清理调度        │  │ web       协议端口路由装配 + 后台清理调度        │
└─────────────────────────────────────────────────┘  └────────────────────────────────────────────────┘
```

依赖方向约束：

- `core` 不 import 任何 adapter。各生态通过 `core/web` 中的 `GoEcosystem` / `NpmEcosystem` / `PyEcosystem` / `MvnEcosystem` 接口注入控制台聚合层（由 `cmd/permaporter` 装配时传入，靠 adapter 的 `*App` 隐式实现）。
- adapter 之间互不 import；共享的基础设施（单库 Store、会话、限流、上游客户端、日志环形缓冲、markdown、版本比较）全部下沉到 `core`。
- `config` 只被 `cmd` 与各 adapter 读取进程级配置；上游地址、出站代理、存储目录、meta_ttl 的运行时值存在单库 `ecosystem_settings`（go / npm / py / mvn 各一行，内存缓存，写操作即时失效）。

## 端口与职责

| 端口 | 角色 | 允许 | 禁止 |
| :--- | :--- | :--- | :--- |
| **9090** | 控制台 | SPA 外壳、`/static`、`/api`（公开/认证/管理）、聚合 `/healthz`、聚合 `/metrics`（四族指标并集）、`PPROF=1` 时 `/debug/pprof`（需登录） | 任何 registry / GOPROXY / PyPI / Maven 协议代理（协议路径返回 JSON 404） |
| **9091** | npm Registry | packument（`GET /{pkg}`）、tarball（`GET /{pkg}/-/{file}.tgz`）、audit 透传（`POST /-/npm/v1/security/advisories*`）、本口 `/healthz`、`npm_permaporter_*` 指标 | 发布类请求（405）、`/admin`、`/api`、任何 UI 数据接口 |
| **9092** | GOPROXY | `@v/list`、`@latest`、`.info/.mod/.zip/.ziphash`、`/sumdb/*`（GET/HEAD）、本口 `/healthz`、`go_permaporter_*` 指标 | 非协议路径（text/plain 404）、发布类请求（405）、`/admin`、`/api`、任何 UI 数据接口 |
| **9093** | PyPI Simple | `/simple/` 项目索引、`/simple/{project}/` 锚点页（`#sha256=` 片段）、`/packages/{file}` 发行文件（GET/HEAD）、本口 `/healthz`、`py_permaporter_*` 指标 | 非协议路径（text/plain 404）、发布类请求（405）、`/admin`、`/api`、任何 UI 数据接口 |
| **9094** | Maven 仓库（Java/Gradle） | `/{group}/{artifact}/maven-metadata.xml`（构件级/版本级）、`/{group}/{artifact}/{version}/{file}` 构件文件、`<资源>.sha1/.md5/.sha256/.sha512` 校验和（GET/HEAD）、本口 `/healthz`、`mvn_permaporter_*` 指标 | 非协议路径（text/plain 404）、发布类请求（405）、`/admin`、`/api`、任何 UI 数据接口 |

一次登录仅发生在 9090 控制台（会话 Cookie `permaporter_session` 仅存在于 9090）；四个协议端口只读、无认证。

## 五端口不变式

1. **控制台只服务 UI 与聚合 API**。9090 上的 `/api/module/*`、`/api/package/*`、`/api/pyproject/*`、`/api/mvnartifact/*` 是控制台详情数据接口（读单库本生态前缀表），不是协议端点；协议路径（如 `/x/@v/list`）在 9090 上只会落入 NoRoute，协议客户端得到 JSON 404。
2. **协议端口不注册任何 UI 路由与数据接口**。9091/9092/9093/9094 只注册 `/healthz`、`/metrics`（与 go 侧的 `/sumdb/*`），其余全部进入各自的 NoRoute 协议分发；`/api`、`/admin` 不存在（`/admin` 前缀返回各自格式的 404，浏览器形态输出外壳 HTML 作引导）。协议端口对浏览器形态的未知路径仅回落输出外壳 HTML，且因无 `/static` 路由页面无法真正加载。
3. **控制台聚合必须进程内完成**。9090 的聚合接口（summary/dashboard/settings/healthz/metrics）通过 `capture()` 直接调用注入的生态 handler 并捕获响应体，严禁对 9091/9092/9093/9094 发起 HTTP 反代——协议端口随时可只读重启，且聚合层可以拿到各生态的完整结构化数据。
4. **各协议端口保持自己的错误格式**。9091 一律 JSON `{"error": …}`；9092、9093 与 9094 一律 `text/plain; charset=utf-8`（GOPROXY 客户端按文本处理错误，链式回退依赖状态码；pip 与 Maven/Gradle 客户端同样按状态码决定回退）；9090 浏览器回落 SPA 外壳、API 形态 JSON 404。各端口的 404 语义互不复用。
5. **生态数据隔离**。数据层为单库：各生态以 `go_*` / `npm_*` / `py_*` / `mvn_*` 表名前缀隔离（查询层入口带表隔离断言，越界即 panic）；四棵存储树两两互异且互不为父子（`config.Validate` 启动校验，违反即拒绝启动、不监听任何端口）；四套清理器各自只接收自己生态 `ecosystem_settings.storage_dir` 指向的目录。

## 三条典型请求的生命周期

### 1. npm 元数据请求：`GET :9091/react`

1. NoRoute 进入 `ProxyDispatch`：非 `/admin` 前缀、已初始化、GET 方法，`splitPkgPath` 解析出包名 `react`（无 tarball 文件名）。
2. `Engine.ServePackument`：读生态配置与 `GetPackageLite` 判断 `meta_cached_at` 是否超过生态配置的 `meta_ttl`（初始值取 META_TTL，默认 2m）。
3. 未过期 → 命中改写缓存（LRU，key 为 `pkgName|scheme://host`）或读 `storage-npm/meta/react.json` 落盘内容；过期且不在失败冷却 → 同步刷新（singleflight 归并同包并发，整体超时 30s，等待上限 5s，超时回退旧缓存 + 后台继续刷新）。
4. 响应前经 `ApplyBase` 把落盘内容中的哨兵基地址 `http://__STEAD_BASE__` 一次替换为请求方站点根地址（`dist.tarball` 指向 `http://<host>:9091/react/-/react-<ver>.tgz`），`Content-Type: application/json`。
5. 命中计数写入内存统计缓冲（错峰批量落库 `npm_status`/`npm_daily_stats`），并 `TouchPackage` 刷新访问时间。

冷缓存（或库有记录但文件丢失）时直接同步回源：主上游带 `If-None-Match`（304 则只更新时间戳），按候选顺序尝试备用上游；拉回后 `ExtractPackument` 入库、`SentinelizePackument` 哨兵化、原子写盘。

### 2. Go 版本列表请求：`GET :9092/github.com/gin-gonic/gin/@v/list`

1. NoRoute 进入 `ProxyDispatch`，`proxy.Parse` 识别为 `KindList` 协议请求（模块路径先 `module.UnescapePath` 再 `module.CheckPath`，统一按重新编码形式回上游与磁盘）。
2. `Engine.serveList`：`GetModuleLite` 判断 `meta_cached_at` 是否过期；过期则 `refreshList`（singleflight + 30s 超时 + 5s 等待上限 + 失败冷却 1m，逻辑与 npm 侧同构）。
3. 命中：读 `storage-go/modules/<escaped>/@v/list`，`text/plain; charset=utf-8` + `Cache-Control: no-cache` 返回。
4. 冷缓存：回源拉取上游 `@v/list`（支持 ETag 条件请求），`NormalizeList` 过滤伪版本/非 semver 行并排序，原子写盘后 `MarkListed` 入库；`@latest` 未拉取过时按列表推导最高版本供 UI 展示。

`.zip` 请求路径类似但产物不可变：磁盘命中零拷贝返回（`Cache-Control: public, max-age=31536000, immutable`，`http.ServeContent` 处理 ETag/Range）；未命中走 `ensureZip`（30 分钟预算、16 并发闸、磁盘余量预检 256MB、临时文件落 `storage-go/tmp/`、校验链通过后原子改名）。

### 3. 控制台聚合 API 请求：`GET :9090/api/admin/dashboard`（已登录）

1. `/api/admin` 组中间件 `RequireAuth` 校验会话 Cookie（内存 SessionStore，24h 滑动续期）。
2. `DashboardAggregated` 用 `capture()` 分别以 `GET /api/admin/dashboard` 进程内直调各生态的 `DashboardAPI`（httptest.Recorder 捕获响应体与状态码）。
3. 各生态 handler 查询单库中自己前缀的表（各自经过 30s 页面缓存：`viewCache` 按 `writeGen`/`statsGen` 两个数据代次判断失效）。
4. 以 `{"ok":true,"go":{…},"npm":{…},"py":{…},"mvn":{…}}` 结构返回；任一生态响应异常时该段降级为 `{"ok":false,"error":"生态句柄响应异常","status":<code>}`。

## 数据层

### 库文件与初始化

单库承载平台与各生态（go/npm/py/mvn）全部数据，驱动二选一：**SQLite（默认）**（modernc.org/sqlite，纯 Go 无 CGO，默认 `file:./data/permaporter.db`，DELETE 回滚日志模式——运行期只有单个 `.db` 文件，无 `-wal/-shm`，`synchronous=FULL`，外键开启）或 **PostgreSQL**（pgx，经服务器配置文件 `permaporter.toml` 的 `[database]` 段启用；详见 [configuration.md](configuration.md)）。连接池：两种驱动同构——全库共享**写池单连接**（平台与各生态共用一把写锁，`database/sql` 层排队；SQLite 侧杜绝 SQLITE_BUSY，PG 侧保持同等串行语义），读池 4 连接（SQLite 侧 `query_only=1` 连接级只读；PG 侧无 PRAGMA 概念，读池只是普通连接池）；`busy_timeout=30s` 仅作用于 SQLite；超过 100ms 的 SQL 打慢查询日志。

**双方言收敛在 `internal/core/database`**：查询层面向双方言兼容 SQL（或 SQLite 形态），PG 驱动包装器在执行前做词法级改写（`?`→`$N`、`LIKE`→`ILIKE`、`CURRENT_TIMESTAMP`→UTC 秒级格式化、DDL 类型映射 `INTEGER`→`BIGINT`/`DATETIME`→`TEXT`/`AUTOINCREMENT`→身份列、`SUM(...)`→`SUM(...)::int8`；`PRAGMA` 语句转 no-op）。adapter 层与协议层不出现任何方言分支；`schema_version` 建库与可加性演进机制双方言通用。

首次启动空库时按目标 schema 一次建齐全部表/索引并写入 `schema_version`（platform / go / npm / py / mvn 组件，标记初始化完成时间）；已存在的库启动时核对 `schema_version`：已有组件版本不匹配即拒绝启动（`make db-reset` 重置），新增生态组件缺失属可加性演进——自动建表补行（幂等），既有数据保留。初始化、启动校验与重置细则见 [single-db.md](single-db.md)。

### 表命名：生态前缀，而非 ecosystem 判别列

各生态共用一个库文件，但**绝不用一张宽表加 `ecosystem` 判别列**承载多套生态数据：

- 判别列漏写一个 WHERE，清理或统计就会跨生态误删/误算——错误发生在运行期，难以在评审期发现；
- 前缀表（`go_modules` vs `npm_packages`）让错表在评审期直接暴露，且查询层入口有表白名单断言兜底（越界即 panic）；
- 各生态的数据语义完全不同（go 版本有 `.info/.mod/.zip` 三类产物与伪版本，npm 版本对应 tarball 与 SRI，py 项目对应发行文件与 sha256，mvn 构件对应仓库路径文件与 sha1），合并宽表会迫使大量列对任一生态为空。

| 前缀表 | 内容 |
| :--- | :--- |
| `platform_settings` | 平台单行（id=1）：`admin_password_hash`、`updated_at`（管理员密码统一存这里） |
| `ecosystem_settings` | 生态配置，`ecosystem` 主键（`'go'`/`'npm'`/`'py'`/`'mvn'` 各一行）：`upstream_url`、`fallback_url`（npm 初始为空）、`outbound_proxy`、`storage_dir`、`meta_ttl`（毫秒） |
| `schema_version` | `component` 主键（platform/go/npm/py/mvn）+ `version` + `applied_at`（初始化完成时间） |
| `go_modules` / `go_versions` / `go_status` / `go_daily_stats` | go 生态（索引 `idx_go_version_*`；`go_versions.module_id` 外键指向 `go_modules(id) ON DELETE CASCADE`） |
| `npm_packages` / `npm_versions` / `npm_status` / `npm_daily_stats` | npm 生态（索引 `idx_npm_version_*`；`npm_versions.pkg_id` 外键指向 `npm_packages(id) ON DELETE CASCADE`） |
| `py_projects` / `py_files` / `py_status` / `py_daily_stats` | py 生态（索引 `idx_py_file_*`；`py_files.project_id` 外键指向 `py_projects(id) ON DELETE CASCADE`） |
| `mvn_artifacts` / `mvn_metadata` / `mvn_files` / `mvn_status` / `mvn_daily_stats` | mvn 生态（索引 `idx_mvn_file_*`；`mvn_files.artifact_id` 外键指向 `mvn_artifacts(id) ON DELETE CASCADE`） |

表要点：

- go `go_modules`：`module_path`（UNIQUE）、`latest_version/latest_time`、`meta_cached_at`、`latest_cached_at`、`list_etag`、README 三列（`readme_content/readme_version/summary`）、`last_accessed_at`、物化统计列（`version_count/cached_version_count/cached_total_size/last_cached_at`，启动与清理后全量重算）。
- go `go_versions`：`(module_id, version)` UNIQUE，`info_time/info_cached_at/mod_cached_at/zip_cached_at`、`zip_size_bytes/mod_size_bytes`、`zip_path/ziphash`、`mod_content/requirements_json/go_version`、`listed_at`、`is_pseudo`；三个覆盖"已缓存 zip"的部分索引。
- npm `npm_packages`：`pkg_name`（UNIQUE）、元数据摘要列、`meta_cached_at`、`meta_etag`、`last_accessed_at`、物化统计列（`latest_cached_version/cached_version_count/cached_total_size/last_cached_at`）。
- npm `npm_versions`：`(pkg_id, version)` UNIQUE，`tarball_path`、`tarball_url`、`file_size_bytes`、`integrity`（SRI）、三组依赖 JSON、`published_at/cached_at`。
- py `py_projects`：`project_name`（UNIQUE，PEP 503 规范化名）、`display_name/latest_version/summary`、`meta_cached_at`、`meta_etag`、`last_accessed_at`、物化统计列（`file_count/cached_file_count/cached_total_size/last_cached_at`）。
- py `py_files`：`(project_id, filename)` UNIQUE，`version/filename/file_size_bytes/sha256/file_path/source_url/requires_python/upload_time/cached_at`；锚点页由本表生成（相对路径锚点），磁盘不存 HTML。
- mvn `mvn_artifacts`：`(group_id, artifact_id)` UNIQUE，`latest_version/release_version`、`version_count`、`last_updated`、`meta_cached_at`、`last_accessed_at`、物化统计列（`file_count/cached_file_count/cached_total_size/last_cached_at`）。
- mvn `mvn_metadata`：`repo_path`（UNIQUE，构件级/版本级 metadata 的仓库路径）、`xml`（raw XML）、`etag`、`cached_at`。
- mvn `mvn_files`：`(artifact_id, repo_path)` UNIQUE，`version/filename/file_size_bytes/sha1/file_path/cached_at`。
- `go_status`/`npm_status`/`py_status`/`mvn_status`（单行）+ `go_daily_stats`/`npm_daily_stats`/`py_daily_stats`/`mvn_daily_stats`（按日）：元数据/产物请求总数与命中数；高频计数在各自生态的内存缓冲，错峰批量落库（go 每分钟整点、npm 第 20 秒、py 第 40 秒、mvn 第 50 秒，单库下避免同时抢写锁），退出时强制 flush。
- 查询层入口带**表隔离断言**：每条 SQL 经表白名单（go 侧 `go_* + ecosystem_settings`，npm 侧 `npm_* + ecosystem_settings`，py 侧 `py_* + ecosystem_settings`，mvn 侧 `mvn_* + ecosystem_settings`）校验，越界即 panic——清理器在结构上不可能触达另一生态的表。

### 存储目录布局与路径编码

```
GO_STORAGE_DIR (./data/storage-go)
├── modules/<GOPROXY 编码模块路径>/@v/
│   ├── list                    @v/list 落盘（规范化后的版本行）
│   ├── latest.json             @latest 落盘（上游原始 JSON）
│   ├── <编码版本>.info / .mod / .zip / .ziphash
└── tmp/                        zip 下载临时文件（.zip-*，清理任务回收 >1h 的残留）

NPM_STORAGE_DIR (./data/storage-npm)
├── meta/<包名>.json            packument 落盘（dist.tarball 已哨兵化）
└── packages/<包名>/<版本>.tgz  tarball 落盘（@scope/name 为嵌套目录）

PY_STORAGE_DIR (./data/storage-py)
└── packages/<filename>         wheel/sdist 落盘（文件名全局唯一，平铺；锚点页由 py_files 表生成不落盘）

MVN_STORAGE_DIR (./data/storage-mvn)
└── <group 路径>/<artifact>/<version>/<file>   构件文件镜像 Maven 仓库目录布局落盘
```

- 模块路径使用 GOPROXY 大小写编码（`github.com/!burnt!sushi/toml`），写入前经 `module.CheckPath` 校验，磁盘路径拼接经 `storageFilePath` 兜底确认不越出存储目录；`.info` 允许非规范版本 token（分支/提交号），但仅透传不落盘，`.mod/.zip/.ziphash` 只接受规范版本。
- npm 包名经 `pkgNameRe`（`^(@[a-zA-Z0-9._~-]+/)?[a-zA-Z0-9._~-]+$`）校验后才拼磁盘路径。
- py 项目名按 PEP 503 规范化（连续 `-_.` 折叠为 `-` 并转小写）后才查库与生成锚点；发行文件名经字符集白名单校验后才拼磁盘路径（`packages/` 平铺单层，无子目录穿越面）。
- mvn 路径每段必须匹配 `^[A-Za-z0-9][A-Za-z0-9._-]*$`（挡 `..` 与编码残留），段数 ≤12、总长 ≤512；构件文件名必须以 `<artifactId>-<version>` 开头；DB 记录拼磁盘路径前经 `StorageFilePath` 越界断言。
- npm 元数据落盘前把全部 `dist.tarball` 改写为哨兵地址（与 host 无关，任意域名/协议下可复用），响应时一次字节替换。

## 缓存与刷新策略

| 对象 | 缓存语义 | 刷新 |
| :--- | :--- | :--- |
| npm packument | `meta/<pkg>.json` + `packages` 表索引 | TTL（`META_TTL`，默认 2m）+ 上游 ETag 条件请求（304 只更新时间戳）；过期先同步刷新，等待上限 5s，超时先回旧缓存、后台继续 |
| npm tarball | `packages/<pkg>/<ver>.tgz`，**永久** | 命中即返回，永不主动回源；npm 不允许同版本重新发布，产物不可变 |
| go `@v/list`、`@latest` | 落盘 + 库索引 | TTL + ETag 条件请求，策略与 npm packument 同构 |
| go `.info/.mod/.zip/.ziphash` | 落盘，**永久** | 磁盘命中即返回；zip 有自愈对账（磁盘有文件而索引缺失时补登记） |
| py 锚点页 | 由 `py_files` 表生成，不落盘 HTML | TTL + ETag 条件请求（304 只更新时间戳）；刷新失败/超时回退旧页 |
| py 发行文件 | `packages/<filename>`，**永久** | 磁盘命中即返回；回源时按锚点页记录的 `#sha256=` 边下边验 |
| mvn `maven-metadata.xml` | `mvn_metadata` 表（raw XML + etag） | TTL + ETag 条件请求，raw XML 原样透传；不做构件记账 |
| mvn 构件文件 | 镜像仓库布局落盘，**永久** | 磁盘命中即返回；回源时按上游 `.sha1` 边下边验；`<资源>.sha1/.md5/.sha256/.sha512` 由本地缓存内容即时计算 |

**产物校验链**：npm tarball = Content-Length 截断检测 → gzip 魔数 → SRI 摘要（sha512/sha256/sha1，有 `dist.integrity` 才校验）；go zip = Content-Length 截断检测 → zip 魔数 → `zip.CheckZip` 结构校验 → h1 校验和（`dirhash.Hash1`，与 go.sum 口径一致）→ 上游 `.ziphash` 可选比对；py 发行文件 = Content-Length 截断检测 → sha256 边下边验；mvn 构件文件 = Content-Length 截断检测 → 上游 `.sha1` 边下边验（上游可得时强制）。任一环节失败即丢弃临时文件、切换候选上游，绝不落盘。

**上游故障时的降级行为**：

- 元数据刷新失败/超时进入 1 分钟冷却（冷却期内跳过重复刷新直接回旧缓存）；失败结果不进入 singleflight 缓存，重试立即生效。
- 上游 404/410：npm 返回 JSON 404（"上游不存在该包"）；go/mvn 侧返回 text/plain 404 且不切备用上游（避免未知对象每次双倍请求）；网络错误/5xx/403 继续尝试备用上游（npm/py 侧按候选顺序依次尝试）。
- zip 下载前检查磁盘剩余空间（Content-Length + 256MB），不足返回 507。
- sumdb 透传失败时立即失效 `/supported` 探测缓存（10 分钟 TTL），让客户端回退直连 sumdb。

## 四套清理策略（设计约束：不得通用化复用）

四套清理器各自独立调度（启动 10 分钟后首跑，此后每 12 小时；管理接口可手动触发，运行中重复提交返回 409），规则不同，**不允许抽成一个通用清理器复用**：

| | go 清理器 | npm 清理器 | py 清理器 | mvn 清理器 |
| :--- | :--- | :--- | :--- | :--- |
| 有记录无文件 | **复位** zip 状态（保留版本记录——该版本可能仍在 `@v/list` 中，客户端仍会请求其 `.info/.mod`） | **删除**版本记录（npm 语义下无 tarball 的版本记录无价值） | **复位**文件缓存状态（保留记录——锚点页仍列出它，客户端下次请求会重新回源） | **复位**文件缓存状态（保留记录——仓库路径仍可重新回源） |
| 孤立文件 | 仅当不匹配任何已知（模块, 版本）对时删除（产物不可变，宁可保留；覆盖 DB 重建后的自愈窗口） | 无对应版本记录即删除 | 无 DB 记录且超 30 分钟保护窗口即删除；`.pydl-*` 下载残留超 1 小时删除 | 无 DB 记录且超 30 分钟保护窗口即删除；`.mvndl-*` 下载残留超 1 小时删除 |
| 元数据 | `@v/list`、`latest.json` 与**仅元数据模块**是合法缓存，绝不删除 | **仅元数据包**视为无用缓存，连同 meta JSON 一并删除（30 分钟活跃窗口内除外） | 项目记录与**仅元数据项目**（锚点页缓存）是合法终态，绝不删除 | 构件记录、`mvn_metadata` 缓存与**仅元数据构件**（版本区间/动态版本解析只读元数据即可工作）绝不删除 |
| 临时文件 | `tmp/` 下超过 1 小时删除 | 无独立 tmp 目录 | `packages/` 下 `.pydl-*` 超过 1 小时删除 | 全树扫描 `.mvndl-*` 超过 1 小时删除 |
| 目录语义 | 模块路径可嵌套（`example.com/a` 与 `example.com/a/b` 共享父目录），只删文件与 `@v` 目录、向上剪枝空目录，禁止 `RemoveAll(moduleDir)` | 包目录与包一一对应，可整包删除 | 文件平铺（packages/ 单层），无目录剪枝 | 镜像仓库布局嵌套，删除文件后剪枝空目录 |

不能通用化复用的原因是**语义而非实现**：GOPROXY 协议要求 `@v/list` 与版本产物长期可服务（客户端校验和数据库依赖产物不可变），元数据模块是合法终态；npm 侧的"仅元数据"状态只是下载流程的中间态，可安全回收；py/mvn 侧的锚点页与元数据是协议工作所需的最小缓存，记录本身是清单来源。同一套规则套用到另一侧要么误删合法协议产物、要么永久保留无用缓存。

各生态唯一共享的约定是 30 分钟活跃保护窗口（窗口内有访问记录的对象视为下载进行中，跳过清理）。

## 管理后台（9090 `/admin`）

- **仪表盘**：模块/包/项目/构件数、版本与文件数、缓存体积、命中率、磁盘水位、每日请求趋势（14 天）、最近缓存活动（各生态独立卡片）。
- **GO / NPM / PYTHON / JAVA 管理**：搜索、排序、分页；查看缓存状态；删除单个版本产物或构件文件（npm 侧删空后连包一起删；go 侧删空后剪枝 `@v` 目录；py 侧项目锚点页保留；mvn 侧构件记录与元数据保留）；强制刷新单模块/单包/单项目/单构件元数据。模块/包/项目详情页渲染上游 README（服务端 goldmark），并提供接入命令示例。
- **依赖预热**：粘贴 `go.mod`/`go.sum`/`module@version`、`package.json`/包名列表、`requirements.txt`/项目名列表或 `pom.xml`/Gradle/坐标列表，批量拉取元数据（可选产物），单次上限 500 条。
- **请求日志**：各生态各自最近 500 条代理请求（内存环形缓冲，页面取最近 200 条）。
- **系统配置**：按生态分别设置主/备用上游、出站代理（http/https/socks5）与连通性测试；修改管理密码；手动触发清理任务。

接口明细见 [api.md](api.md)。

## 并发模型

- **单飞（singleflight）**：同一资源（`meta:<pkg>` / `list:<mod>` / `latest:<mod>` / `info|mod|zip:<mod>@<ver>` / `tarball:<pkg>@<ver>` 等）的并发请求归并到一次上游拉取；失败后立即 `Forget`，重试不复用失败结果。
- **下游并发上限**：每生态一个 16 槽信号量闸（`upstreamMaxConcurrent`），所有上游请求（元数据/产物/透传/sumdb）排队获取；突发批量安装时限流对上游的压力。`/healthz` 的 `inflight` 与 `<eco>_permaporter_upstream_inflight` 暴露当前占用数。
- **超时预算**：元数据拉取 30s；tarball 下载 10 分钟；zip 下载 30 分钟（临时文件先落 `tmp/`，校验通过后原子改名）；sumdb `/supported` 探测 15s（结果缓存 10 分钟）；HTTP 客户端阶段超时：拨号/TLS 15s、响应头 30s。
- **请求与刷新解耦**：元数据刷新与产物下载使用独立的后台 context，客户端断开不会连坐同资源的其他等待者；等待上限 5s，超时先回旧缓存（可用性优先）。
- **数据库**：单库写池单连接串行（平台与各生态共享一把写锁）+ 读池 4 连接只读；`busy_timeout=30s` 兜底；统计落库错峰对齐墙钟（go 整分、npm 第 20 秒、py 第 40 秒、mvn 第 50 秒），与页面缓存的数据代次（`writeGen`/`statsGen`，各生态各自独立计数）分离，避免统计写放大打掉页面缓存。
- **优雅退出**：停收请求（`http.Server.Shutdown`，10s）→ 四引擎等待后台任务（`bgWG`，30s 上限）→ 统计落盘（Close 内刷盘）→ 关闭单库。任一端口监听失败即整体退出，避免半可用状态。

## 已知限制（完整版）

- npm 生态的 audit 透传（`POST /-/npm/v1/security/advisories*`）依赖上游可达；上游不可用时返回 502。四个协议都没有（聚合）搜索端点，页面搜索仅覆盖本地已缓存数据（GO / NPM / PYTHON / JAVA 列表页工具栏直搜）。py 侧锚点页仅收录被请求过的项目，`/simple/` 根索引不回源全量清单。
- 缓存语义为"只增不减"：产物永久缓存，清理任务只回收孤立文件/失效记录，不按容量或年龄淘汰有效缓存；磁盘水位由仪表盘监控（低于 5GB 告警）。
- 上游 `.ziphash` 并非所有代理都提供；缺失时以本地计算的 h1 校验和为准。
- `@latest` 并非所有上游对所有模块都提供；go 侧预热会回退到 `@v/list` 的最高版本。
- 会话存于内存，重启后需重新登录；统计中的命中率计数重启后从零累计（每日趋势已落库不受影响）。
