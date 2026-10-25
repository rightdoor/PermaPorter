# PermaPorter 配置说明

配置分三层：**服务器配置文件**（启动必需项：数据库与五端口）→ **环境变量**（同项兜底 + 其余进程配置）→ **运行时生态配置**（存单库 `ecosystem_settings`，控制台"系统配置"按生态修改）。取值优先级：**配置文件 > 环境变量 > 内置默认值**——文件中已填写的项生效，未填写的项按环境变量、默认值兜底。环境变量模板见 [.env.example](../.env.example) 与 `build/package/permaporter.env.example`（systemd 部署复制为 `/etc/default/permaporter`）。

## 服务器配置文件（permaporter.toml）

- **路径**：环境变量 `CONFIG_DIR` 指定配置目录（默认 `./config`，容器内 `/app/config`，systemd 单元预设 `/var/lib/permaporter/config`——相对路径按进程工作目录解析，部署时须保证运行用户对配置目录可写），文件名 `permaporter.toml`。
- **内容**：只存两类启动必需项——`[database]`（数据库）与 `[ports]`（五端口）。文件顶部注释说明用途、重启生效与明文密码提醒；**postgresql 密码为明文存储**（文件权限 0600）。
- **生成**：启动时若配置目录存在且文件缺失，自动写入模板（`[database]` 段全注释态、端口预填当前生效值——即环境变量/默认值合并结果，容器内 ENV 端口因此天然保持生效），并记一条 INFO 日志；目录不存在时按默认路径创建。
- **修改后需重启生效**；写回（向导 initialize）一律走临时文件 + 原子改名。

```toml
# ---------- 数据库（driver 缺失或为空 = 数据库未配置：首次启动进入初始化向导） ----------
[database]
# driver 只支持 sqlite / postgresql 两种取值（去掉注释并取消相应行的注释后生效）
driver = "sqlite"
dsn = "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"

# PostgreSQL 形态（host/port/user/password/database 必填；sslmode 缺省 prefer）：
# driver = "postgresql"
# host = "127.0.0.1"        # 容器 compose 内填服务名 postgres
# port = 5432
# user = "permaporter"
# password = "change-me"    # 明文存储，注意文件权限 0600
# database = "permaporter"
# sslmode = "prefer"        # prefer（默认）/ disable / require / verify-ca / verify-full

# ---------- 五端口（互不相同，违反即拒绝启动） ----------
[ports]
ui  = 9090 # 控制台（SPA + /api + 聚合 healthz/metrics）
npm = 9091 # npm Registry
go  = 9092 # GOPROXY
py  = 9093 # PyPI Simple
mvn = 9094 # Maven 仓库
```

`[ports]` 各项为 0 或缺失时按环境变量/默认值兜底；`[database].dsn` 仅 sqlite 使用（缺省用默认 DSN）。

## 启动判定顺序与日志

严格按以下顺序（`internal/config/bootstrap.go`）：

1. 读取配置文件：文件不存在 → 生成模板并记 INFO（含文件路径）；文件存在但 **TOML 解析失败 → 拒绝启动**（日志输出文件路径与具体解析错误，非零退出，不监听任何端口）。
2. `UNATTENDED=1` → 无头正常启动（数据库未配置时回落默认 SQLite DSN；日志说明无头模式与最终数据库类型）。供 CI、测试、脚本使用（`make run`/`make test` 已内置）。
3. 数据库**已配置**（`driver` 非空）→ 启动校验：必填字段完整性（postgresql 的 host/user/password/database；sqlite 的 DSN 缺省用默认值）、五端口互异、数据库可连通（sqlite 可建库、PG 握手成功）、`schema_version` 匹配。**任一失败 → 拒绝启动**：结构化日志输出具体原因与修复建议，非零退出，不监听任何端口。
4. 数据库**未配置** → **初始化模式**（合法状态而非错误）：INFO 日志提示"数据库未配置，进入初始化模式：仅监听 9090"，浏览器打开控制台完成初始化向导；完成后全部端口原地就绪（无需重启）。

注意区分：「未配置（driver 为空）→ 初始化模式」与「配置不完整/错误（如 driver 已填但字段缺失、PG 不可达）→ 拒绝启动」——后者绝不静默进入初始化模式。

### 初始化向导

- 入口：初始化模式下浏览器访问 `http://<host>:9090`，管理端自动导向 `/admin/wizard`；分步收集数据库（含"测试连接"）、五端口、四生态初始上游、管理员密码。
- 端点（仅 9090，且仅初始化模式存在；完成后关闭）：`GET /api/setup/status`、`POST /api/setup/test-database`（只测连通不落盘）、`POST /api/setup/initialize`（校验 → 原子写回配置文件 → 建库 → 写管理员密码与初始上游 → 原地启动全部端口）。
- **安全**：支持可选 `INIT_TOKEN` 环境变量（设置后向导第一步需先输入令牌）。**未初始化前向导对网络开放**——公网部署务必设置 `INIT_TOKEN` 或用防火墙收敛 9090，完成初始化后可移除。
- **重新进入向导**（高级操作）：手工清空/注释 `[database]` 段的 `driver` 后重启即回到初始化模式；向导对所选库中已有数据不做删除（建表按可加性演进幂等），但**管理员密码步骤会重置管理员密码**。
- 初始化完成后，系统设置页提供"数据库与端口"**只读**卡片（当前值 + 配置文件路径）；修改需编辑配置文件并重启。上游、出站代理、TTL、管理密码等仍可在控制台修改。

## 环境变量全表

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `CONFIG_DIR` | `./config` | 服务器配置文件目录（`permaporter.toml`；容器内 `/app/config`） |
| `UNATTENDED` | 空 | `1` 开启无头模式：跳过初始化向导判定，数据库未配置时按默认 SQLite 正常启动（CI/测试/脚本；`make run`/`make test` 已内置） |
| `INIT_TOKEN` | 空 | 初始化向导访问令牌（可选；未初始化前向导对网络开放，公网部署建议设置） |
| `UI_PORT` | `9090` | 控制台端口（SPA + `/api` + 聚合 healthz/metrics；配置文件 `[ports].ui` 优先生效） |
| `NPM_PORT` | `9091` | npm Registry 协议端口（`[ports].npm` 优先生效） |
| `GO_PORT` | `9092` | GOPROXY 协议端口（`[ports].go` 优先生效） |
| `PY_PORT` | `9093` | PyPI Simple 协议端口（`[ports].py` 优先生效） |
| `MVN_PORT` | `9094` | Maven 仓库协议端口（`[ports].mvn` 优先生效） |
| `DB_DSN` | `file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE` | SQLite 单库 DSN（配置文件 `[database]` 配置后此项不再生效）；保持 DELETE 回滚日志模式（运行期只有单个 `.db` 文件） |
| `GO_STORAGE_DIR` | `./data/storage-go` | go 存储树（四棵树两两互异且互不为父子，违反拒绝启动） |
| `NPM_STORAGE_DIR` | `./data/storage-npm` | npm 存储树 |
| `PY_STORAGE_DIR` | `./data/storage-py` | py 存储树 |
| `MVN_STORAGE_DIR` | `./data/storage-mvn` | mvn 存储树（镜像 Maven 仓库目录布局） |
| `DEFAULT_GO_UPSTREAM` | `https://proxy.golang.org` | go 生态默认主上游（向导/首次初始化时写入 ecosystem_settings 的预填值） |
| `DEFAULT_GO_FALLBACK_UPSTREAM` | 空 | go 生态默认备用上游（仅 go 侧有回退链语义） |
| `DEFAULT_NPM_UPSTREAM` | `https://registry.npmjs.org` | npm 生态默认主上游 |
| `DEFAULT_PY_UPSTREAM` | `https://pypi.org/simple/` | py 生态默认主上游（PyPI Simple 根，以 `/simple/` 结尾） |
| `DEFAULT_MVN_UPSTREAM` | `https://repo.maven.apache.org/maven2` | mvn 生态默认主上游（Maven 仓库根） |
| `INIT_PASSWORD` | 空 | 首次启动自动初始化管理员密码（写入 platform_settings；无头/正常模式语义不变） |
| `META_TTL` | `2m` | 元数据刷新间隔初始值（首次初始化写入 ecosystem_settings，此后以库内值为准；Go duration 格式） |
| `PPROF` | 空 | `1` 开启 `/debug/pprof`（仅 9090，需管理员登录） |
| `GIN_MODE` | —— | Gin 框架模式；容器镜像内置 `GIN_MODE=release`（非容器环境无需设置） |

## 校验规则

- 五端口互不相同（含配置文件取值）、四棵存储树两两互异且互不为父子：违反任意一条进程**拒绝启动、不监听任何端口**（`internal/config.Validate`）。
- 端口非法值（如非数字、0、>65535）同样拒绝启动或回退并留下告警日志。
- postgresql 必填字段（host/user/password/database）缺失、`driver` 取值非法：拒绝启动并指出具体字段与修复方法。
- 数据库连通性：sqlite 不可建库、PG 握手失败即拒绝启动（日志含具体连接错误）。
- 数据库 schema 校验见 [single-db.md](single-db.md)：`schema_version` 已有组件版本不匹配即拒绝启动（SQLite 与 PostgreSQL 同一套机制）。

## 数据库（SQLite / PostgreSQL 双方言）

- SQLite 为默认（纯 Go 驱动，单文件库）；PostgreSQL 经配置文件 `driver = "postgresql"` 启用。
- 双方言差异全部收敛在 `internal/core/database`（DDL 类型映射、占位符与语法改写、PRAGMA/连接池策略）；对上层完全透明。
- `schema_version` 建库与可加性演进机制双方言通用；既有 SQLite 数据库升级启动不受任何影响。
- 四棵存储树与文件缓存逻辑不涉及数据库方言，两种驱动下完全一致；五端口 `/healthz` 与 `/metrics` 的 db 状态口径同样一致。

## 运行时生态配置（存单库，经控制台修改）

| 配置 | 说明 |
| :--- | :--- |
| 主上游 / 备用上游 | 按生态设置；必须 `http(s)://` 开头。go 与 mvn 侧主上游 404/410 不切备用（避免未知对象双倍回源），网络错误/5xx/403 才切换；npm 与 py 侧按候选顺序依次尝试 |
| 出站代理 | `http://`、`https://` 或 `socks5://` 前缀；控制台提供连通性测试 |
| `meta_ttl`（毫秒） | 元数据缓存刷新间隔，各生态独立；高频统计与元数据刷新策略详见 [architecture.md](architecture.md) |
