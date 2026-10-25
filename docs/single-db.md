# 单库说明

PermaPorter 的全部数据落在一个单库中：默认 SQLite 库文件（`data/permaporter.db`，环境变量 `DB_DSN` 或服务器配置文件 `[database].dsn`），或 PostgreSQL（配置文件 `[database]` 段 `driver = "postgresql"` 启用；双方言差异全部收敛在 `internal/core/database`，表布局与校验机制完全一致）。四棵存储树（`storage-go/`、`storage-npm/`、`storage-py/`、`storage-mvn/`）与库并存于数据目录、布局不变。本文说明单库的初始化、启动校验与重置操作。设计背景（为何用生态前缀而非判别列）见 [architecture.md](architecture.md) 数据层一节。

## 表布局

| 表 | 内容 |
| :--- | :--- |
| `platform_settings` | 平台单行（id=1）：`admin_password_hash`、`updated_at` |
| `ecosystem_settings` | 生态配置：`ecosystem` 主键（`'go'`/`'npm'`/`'py'`/`'mvn'` 各一行），`upstream_url`、`fallback_url`、`outbound_proxy`、`storage_dir`、`meta_ttl`（毫秒） |
| `schema_version` | `component` 主键（platform / go / npm / py / mvn）+ `version` + `applied_at`（初始化完成时间） |
| `go_modules` / `go_versions` / `go_status` / `go_daily_stats` | go 生态（索引 `idx_go_version_*`，外键 `go_versions.module_id → go_modules(id)`） |
| `npm_packages` / `npm_versions` / `npm_status` / `npm_daily_stats` | npm 生态（索引 `idx_npm_version_*`，外键 `npm_versions.pkg_id → npm_packages(id)`） |
| `py_projects` / `py_files` / `py_status` / `py_daily_stats` | py 生态（索引 `idx_py_file_*`，外键 `py_files.project_id → py_projects(id)`；`py_files` 记录 `sha256` 与上游 `source_url`） |
| `mvn_artifacts` / `mvn_metadata` / `mvn_files` / `mvn_status` / `mvn_daily_stats` | mvn 生态（构件记录、metadata 缓存、构件文件索引与统计） |

各生态查询层只触达各自前缀表（加共享的 `ecosystem_settings` 本生态行），入口带表白名单断言，越界即报错；四套清理器语义不同，绝不通用于另一侧。

## 初始化

- 启动只认配置生效的库：sqlite 为 `DB_DSN` / 配置文件 `[database].dsn` 指向的文件（默认 `file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE`，保持 DELETE 回滚日志模式）；postgresql 为配置文件 `[database]` 连接字段指向的库（连接失败即拒绝启动）。
- 库文件不存在 → 按目标 schema 建全部表与索引，写入 `schema_version` 全部组件行（`applied_at` 即初始化完成时间），进程正常启动。
- 全新安装与重置后的首次启动走**同一套建表逻辑**，无任何迁移分支。
- 管理员密码：库内无记录时经 `http://<host>:9090/admin/setup` 初始化，或以 `INIT_PASSWORD` 环境变量自动写入 `platform_settings`。

## 启动校验

启动（打开库）时执行以下校验，任一失败进程立即退出、不监听任何端口：

| 检查 | 行为 |
| :--- | :--- |
| 库为空（无用户表） | 按目标 schema 建库并写 `schema_version` |
| `schema_version` 全部已有组件版本一致 | 正常启动 |
| `schema_version` 已有组件版本不匹配或含未知组件 | **拒绝启动**，日志提示执行重置（不是本服务初始化的库同样拒绝） |

## 重置（开发期）

```bash
# 1. 停止服务（Windows 本地运行直接 Ctrl+C；systemd：systemctl stop permaporter）
# 2. 删除单库文件
make db-reset
# 3. 重新启动 → 自动重新建库 → /admin/setup 重新初始化密码
```

`make db-reset` 等价于删除 `data/permaporter.db`。

## 数据丢弃边界

- **开发期重置会丢失库内全部数据**：管理员密码、各生态配置（上游/出站代理/`storage_dir`/`meta_ttl`）、模块/包/项目/构件索引、版本记录与统计。数据不保留、不迁移。
- 四棵存储树**不在** `db-reset` 范围内：产物文件仍按需回源重建索引（磁盘有文件而索引缺失的场景由 go 侧命中对账与清理任务自愈），如需彻底重来请手动清空 `storage-go/`、`storage-npm/`、`storage-py/`、`storage-mvn/`。
- 正式部署不建议执行 `db-reset`；正式环境的回退手段是备份恢复（见 [deploy.md](deploy.md)）。
