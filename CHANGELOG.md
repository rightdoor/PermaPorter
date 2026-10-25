# Changelog

本项目的版本号唯一来源为 [Makefile](Makefile) 的 `VERSION`，格式遵循 Keep a Changelog 1.1.0 规范。

## [1.0.0] - 2026-10-25

首个公开发布版本：单进程五端口、四生态齐备的内网只读包镜像。

### Added

- 单进程五端口服务：9090 管理控制台（SPA + `/api` 聚合）、9091 npm Registry 协议代理、9092 GOPROXY 协议代理、9093 PyPI Simple 协议代理、9094 Maven 仓库协议代理（Java 构件，Gradle 同协议）；五端口/四棵存储树互异性启动校验；healthz/metrics 五口统一支持 GET/HEAD。
- 服务器配置文件层：`CONFIG_DIR/permaporter.toml`（默认 `./config`）存放启动必需项（数据库与五端口），取值优先级 配置文件 > 环境变量 > 默认值；首次启动自动生成模板（`[database]` 全注释态、端口预填生效值），写回走临时文件 + 原子改名（0600，PG 密码明文存储）。
- 启动判定语义固定：配置文件解析失败、字段缺失、端口冲突、数据库不可达、schema 不匹配 → 结构化错误日志 + 非零退出 + 不监听任何端口；「未配置 → 初始化模式（INFO 日志）」与「配置错误 → 拒绝启动」严格区分。
- 首次部署初始化向导：数据库未配置（`[database].driver` 为空）且未设 `UNATTENDED` 时进入初始化模式（仅监听 9090，合法状态而非错误），控制台所有路由导向向导，浏览器分步完成数据库（含测试连接）/ 端口 / 上游源 / 管理员密码配置；initialize 校验通过后原子写回配置文件并在进程内原地启动全部端口（不重启进程），完成后向导端点关闭（409），重新进入仅能按文档手工清空配置文件 `[database]` 段后重启。向导端点 `GET /api/setup/status`、`POST /api/setup/test-database`、`POST /api/setup/initialize` 仅 9090 且仅初始化模式存在；支持可选 `INIT_TOKEN` 访问令牌。
- `UNATTENDED=1` 无头模式（CI/测试/脚本）：跳过向导判定，数据库未配置时按默认 SQLite 正常启动（`make run`/`make test` 已内置）。
- PostgreSQL 支持：单库可选 `driver = "postgresql"`（pgx 驱动），双方言（DDL 类型映射、占位符/LIKE/SUM 语法改写、PRAGMA 仅 sqlite）全部收敛在 core/database 方言层，adapter 与协议层零方言分支；`schema_version` 建库与可加性演进机制双方言通用；adapter 数据层 upsert 与每日趋势回填为双方言兼容 SQL（`ON CONFLICT`、Go 侧本地日分组）。
- npm 生态：packument 按需回源与 TTL 条件刷新（ETag/304）、tarball 永久缓存（gzip 魔数 + SRI 摘要校验）、audit 安全查询透传、上游搜索候选汇总、哨兵地址改写与响应期一次替换。
- Go 生态：`@v/list`/`@latest` TTL 刷新、`.info/.mod/.zip/.ziphash` 永久缓存（zip 魔数 + `zip.CheckZip` + h1 校验和校验链、磁盘余量预检与 507）、sumdb 透传与 `/supported` 探测缓存、zip 索引自愈对账。
- py 生态：PEP 503 项目锚点页（`#sha256=` 片段与 `data-requires-python`）按需回源与 TTL 条件刷新、发行文件 sha256 边下边验与永久缓存、`/simple/` 本地已收录项目清单。
- mvn 生态：构件级/版本级 `maven-metadata.xml` 按需回源与 TTL 条件刷新（raw XML 透传）、构件文件永久缓存（镜像仓库目录布局 + 上游 `.sha1` 校验链）、`<资源>.sha1/.md5/.sha256/.sha512` 校验和端点（本地内容即时计算）。
- 管理控制台：管理员初始化/登录/改密（Bcrypt，5 次失败封禁 IP 15 分钟）、仪表盘（统计/磁盘水位/每日趋势/最近活动）、模块/包/项目/构件管理（删除/强制刷新）、依赖预热（go.mod/go.sum/package.json/requirements.txt/pom.xml/Gradle/坐标清单解析，单次上限 500）、四生态请求日志、按生态的系统配置（上游/出站代理/连通性测试）、手动清理任务。
- 系统设置页"数据库与端口"只读卡片（当前值 + 配置文件路径 + 修改提示），数据源 `GET /api/admin/system-info`。
- 可观测性：五端口各自 `/healthz`、9090 聚合 `/metrics`（`go_permaporter_*` + `npm_permaporter_*` + `py_permaporter_*` + `mvn_permaporter_*` 四族并集）、结构化访问日志（`X-Request-ID`）、`PPROF=1` 时 9090 上的 pprof（需登录）。
- 可靠性：共享上游客户端对幂等 GET/HEAD 的传输层错误立即重试一次（ctx 取消除外），状态码语义与回退链判定不变。
- 数据层：单库 SQLite（纯 Go 驱动，DELETE 回滚日志模式，`go_*`/`npm_*`/`py_*`/`mvn_*` 前缀表隔离）、写池单连接/读池只读、统计内存缓冲错峰落库、每日趋势表。
- 清理任务：四套按生态独立的清理器（启动 10 分钟后 + 每 12 小时 + 手动触发），30 分钟活跃保护窗口。
- 部署：`make package` 发布包（systemd 单元 + 环境变量模板 + 安装/卸载脚本）、容器镜像（内置 nginx 五 server 块反代，控制台按请求对外端口推导注入四生态下载地址）、docker compose 双模式编排（SQLite 默认 / `--profile postgres` 启用 PG 服务，`pg_isready` healthcheck，PG 宿主端口可经 `PG_HOST_PORT` 覆盖）、nginx 反代参考配置。
