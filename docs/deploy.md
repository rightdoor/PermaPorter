# PermaPorter 部署

## 前置条件

| 项目 | 要求 |
| :--- | :--- |
| 操作系统 | Linux（amd64/arm64 均可，`make package` 默认 linux/amd64，可用 `GOOS/GOARCH` 覆盖）；Windows 可直接运行但生产建议 Linux |
| 运行时依赖 | 无（静态单二进制，SQLite 为纯 Go 实现，不依赖 CGO/glibc 特性） |
| 构建依赖 | Go ≥ 1.27、pnpm（构建期需要，运行期不需要） |
| 网络 | 能访问配置的上游（默认 `https://proxy.golang.org`、`https://registry.npmjs.org`、`https://pypi.org/simple/` 与 `https://repo.maven.apache.org/maven2`）；如需经代理出网，在控制台"系统配置"按生态设置出站代理（http/https/socks5） |

## 端口与防火墙建议

| 端口 | 服务 | 开放范围建议 |
| :--- | :--- | :--- |
| 9090 | 控制台（UI + 管理 API） | 内网管理网段。管理接口有登录与限流保护，但仍建议收敛来源 |
| 9091 | npm Registry | 开发机与 CI 网段（只读，无认证） |
| 9092 | GOPROXY | 开发机与 CI 网段（只读，无认证） |
| 9093 | PyPI Simple | 开发机与 CI 网段（只读，无认证） |
| 9094 | Maven 仓库（Java/Gradle） | 开发机与 CI 网段（只读，无认证） |

- 五端口互异由启动校验强制；修改端口后需同步更新客户端配置与防火墙规则。
- 全部端口监听 `0.0.0.0`；建议用防火墙而非改动代码限制来源。协议端口无认证，只读语义，暴露面为"能读到已缓存内容并触发回源"。

## 路径一：二进制 + systemd（推荐）

`make package` 产出 `dist/permaporter-<版本>-linux-amd64.tar.gz`（含二进制、`permaporter.service`、`permaporter.env.example`、`install.sh`、`uninstall.sh`、README）与 `.sha256` 校验文件。

```bash
tar -xzf permaporter-<版本>-linux-amd64.tar.gz && cd permaporter-<版本>-linux-amd64
sudo ./install.sh            # 安装到 /usr/local/bin/permaporter，数据目录 /var/lib/permaporter/data
```

`install.sh` 行为：创建系统用户 `permaporter` 与数据/配置目录（0750，属主统一纠正——曾以 root 手工运行遗留的属主文件一并处理）；环境配置写入 `/etc/default/permaporter`（已存在则跳过）；安装并启动 `permaporter.service`（`Restart=on-failure`，`ProtectSystem=strict` + `StateDirectory`/`ReadWritePaths=/var/lib/permaporter` 最小权限加固，单元预设 `CONFIG_DIR=/var/lib/permaporter/config`，`TimeoutStopSec=45` 配合优雅退出）。

首次初始化：浏览器打开 `http://<host>:9090/`——数据库未配置（未初始化）时控制台自动进入部署向导 `/admin/wizard`，依次完成数据库（SQLite 默认 / PostgreSQL）、端口、上游源与管理员密码；完成后全部端口原地就绪，无需重启。

数据库已配置而未设管理员密码时（高级：手工先填写配置文件 `[database]` 段），可经密码初始化页设置，或用环境变量自动写入：

```bash
# 浏览器打开 http://<host>:9090/admin/setup 设置密码；或：
echo 'INIT_PASSWORD=<密码>' | sudo tee -a /etc/default/permaporter && sudo systemctl restart permaporter
```

自检：

```bash
curl http://127.0.0.1:9090/healthz   # 控制台聚合（含 go/npm/py/mvn 四段）
curl http://127.0.0.1:9091/healthz   # npm
curl http://127.0.0.1:9092/healthz   # GOPROXY
curl http://127.0.0.1:9093/healthz   # PyPI Simple
curl http://127.0.0.1:9094/healthz   # Maven 仓库
```

客户端配置：

```bash
go env -w GOPROXY=http://<host>:9092,direct
npm config set registry http://<host>:9091/
pip config set global.index-url http://<host>:9093/simple
pip config set global.trusted-host <host>
```

## 路径二：纯二进制部署

不使用 systemd 时直接运行（数据目录相对工作目录解析，建议固定 `WorkingDirectory`）：

```bash
cd /var/lib/permaporter && /usr/local/bin/permaporter   # 读取进程环境变量（见 .env.example / permaporter.env.example）
```

可用环境变量与默认值见 [configuration.md](configuration.md)；五个端口与四棵存储树均可用环境变量覆盖，违反互异约束会拒绝启动。停止时发送 SIGTERM 即走优雅退出（停收请求 → 等待在途下载与后台刷新 → 统计落盘 → 关库），不要用 SIGKILL。

### 配置文件手工修改 + 重启生效

数据库与端口属启动必需项，运行期不可改（系统设置页"数据库与端口"卡片为只读）。修改流程：

1. 编辑 `CONFIG_DIR/permaporter.toml`（默认 `./config`；[完整参考](configuration.md#服务器配置文件permaportertoml)）；
2. 重启进程（systemd 部署 `systemctl restart permaporter`，容器部署 `docker compose restart permaporter`）；
3. 启动失败会在日志给出具体原因（哪个字段/哪条连接/怎么修），且不监听任何端口——修正后再重启即可。

## 路径三：容器部署

```bash
make docker-build    # VERSION 必填（镜像名默认 permaporter:$(VERSION)，可用 IMAGE= 覆盖）
make docker-up       # 本地试跑：-p 9090:9090 … -p 9094:9094 -v permaporter-data:/app/data
```

镜像要点（见 [build/docker/Dockerfile](../build/docker/Dockerfile)）：

- 多阶段构建：前端（pnpm build）→ 后端（CGO_ENABLED=0，版本号经 `--build-arg VERSION` 注入，缺失即构建失败）→ 最终层为官方 `alpine:3.20` + 补装 nginx 与构建期 tzdata。
- 容器内 nginx 监听 9090/9091/9092/9093/9094 对外，后端进程只监听本机回环 8090/8091/8092/8093/8094（`UI_PORT=8090 … MVN_PORT=8094`；首次启动生成的 `permaporter.toml` 模板会预填这些生效值）。
- 数据卷挂载点 `/app/data`（单库 permaporter.db + 四棵存储树）与配置卷挂载点 `/app/config`（`permaporter.toml`，首次启动生成模板）。
- 生产运行建议叠加资源限制与健康检查（`curl -f http://127.0.0.1:9090/healthz`）。

### docker compose 双模式部署

```bash
docker compose up -d                          # SQLite 模式（默认）
docker compose --profile postgres up -d       # PostgreSQL 模式（同时拉起 postgres 服务）
```

- 首次启动后浏览器打开 `http://<host>:9090` 进入初始化向导：选数据库（SQLite 直接下一步 / PostgreSQL 填连接字段）→ 端口（保持默认即可——容器对外端口由 compose `ports` 决定，向导中的端口是容器内后端端口）→ 上游源 → 管理员密码；完成后四个协议端口原地就绪，无需重启容器。
- PostgreSQL 模式：向导"数据库"步骤填服务名 `postgres`、端口 `5432`、用户名/密码/库名与 compose 中 `POSTGRES_*` 一致；`pg_isready` healthcheck 就绪前向导也可完成填表（"测试连接"通过后再提交即可）。
- PostgreSQL 服务向宿主发布 5432 仅供排障，可经 `PG_HOST_PORT` 覆盖（宿主 5432 已被占用时 `PG_HOST_PORT=15432 docker compose --profile postgres up -d`）；容器网络内向导连接 `postgres:5432` 不受该映射影响。
- 应用配置（数据库连接、上游源）一律经向导 / `config/permaporter.toml`，compose 不注入任何应用配置项；唯一例外是端口映射——修改 `ports:` 时须同步 `permaporter.toml` 的 `[ports]`（容器内后端端口）与 nginx 反代约定，修改任一侧须同步另一侧并重建。
- 控制台注入给前端的四生态下载地址（`__STATE__` / `/api/state`）按"统一偏移"推导外部端口：以 UI 请求自带的对外端口（如 9090）相对容器内后端端口（8090）的差值，统一套用到四个协议口（得 9091-9094）。官方 compose/nginx 与直连部署均满足该假设；若自行改为**非统一**端口映射（各口偏移不一致），注入地址将以配置端口为准，需自行保证配置端口即外部可达端口。
- 公网暴露 9090 时建议在 compose 中设置 `INIT_TOKEN` 环境变量（向导访问令牌；初始化完成后可移除）。

## nginx 反向代理（宿主机层）

容器内已含 nginx；宿主机自建反代时参考 [build/nginx/nginx.conf](../build/nginx/nginx.conf) 的要点：

- **五个独立 server 块**（9090/9091/9092/9093/9094），各自 `proxy_pass` 到对应后端端口，职责互斥不混流。
- 统一传递 `Host`、`X-Forwarded-Host`、`X-Real-IP`、`X-Forwarded-For`、`X-Forwarded-Proto`（后端据此还原外部基地址与会话 Cookie 的 Secure 标记；可信代理仅本机回环）。
- 下载路径（`.zip`/`.tgz`/jar/wheel 等产物文件）：`proxy_read_timeout`/`proxy_send_timeout` ≥ 1800s（后端 zip 下载预算 30 分钟），`proxy_buffering off`、`proxy_max_temp_file_size 0` 流式转发；`.zip/.tgz` 等产物本身已压缩，不要加入 `gzip_types`。
- 9090 的 `/static/`（带内容 hash）可开 nginx 缓存（示例配置 7d + cache_lock）；9090 建议补齐 CSP、`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options` 等响应头（示例配置已含）。
- `client_max_body_size 1M` 足够（只读服务，请求体仅 audit 查询）。

## 可观测性

| 端点 | 内容 |
| :--- | :--- |
| `:9090/healthz` | `{"ok":true,"ui":"ok","uptime":…,"version":…,"go":{db,ready,uptime,inflight},"npm":{…},"py":{…},"mvn":{…}}` |
| `:9091/healthz`、`:9092/healthz`、`:9093/healthz`、`:9094/healthz` | `{"ok":…,"db":"ok/fail","ready":…,"uptime":…,"inflight":…}`（DB 不可用时 503） |
| `:9090/metrics` | `go_permaporter_*`、`npm_permaporter_*`、`py_permaporter_*`、`mvn_permaporter_*` 四族并集（Prometheus 文本格式） |
| 各协议端口 `/metrics` | 仅本生态指标（排障用，不建议配抓取） |
| 访问日志 | 结构化 slog 输出 stdout，带 `X-Request-ID`；跳过静态资源、健康/指标与成功的产物下载 |

## 安全说明

- 密码 Bcrypt 哈希存储于单库 `platform_settings`；登录 5 次失败封禁来源 IP 15 分钟。
- 会话 Cookie `permaporter_session`：HttpOnly、SameSite=Lax、24 小时有效；经反代走 https 时自动加 Secure。
- 协议端口无认证、只读（仅 GET/HEAD）；管理接口全部要求登录；管理接口对模块/包/项目/构件名做白名单校验，杜绝路径穿越。
- README 渲染由服务端 goldmark 完成，默认不输出原始 HTML；外链新窗口打开，远程图片懒加载。
- pprof 仅在 `PPROF=1` 且登录后可用；可信代理仅本机（127.0.0.1/::1）。

## 数据备份与恢复

数据 = **1 个库文件 + 4 棵存储树**，全部位于数据目录下（默认 `./data/`，systemd 部署在 `/var/lib/permaporter/data/`）：

```
permaporter.db   单库（平台凭据 + 各生态索引/统计/配置，表名 go_*/npm_*/py_*/mvn_* 前缀隔离）
storage-go/      go 产物（modules/<mod>/@v/* + tmp/）
storage-npm/     npm 产物（meta/*.json + packages/*/*.tgz）
storage-py/      py 产物（packages/* wheel/sdist）
storage-mvn/     mvn 产物（镜像 Maven 仓库目录布局）
```

**备份顺序**（在线备份，SQLite DELETE 模式下库文件存在写窗口，稳妥做法是停服冷备）：

1. 可选热备：直接复制 `permaporter.db` + 四棵树；库文件可能正处于一次写事务中，恢复前用 `sqlite3 permaporter.db "PRAGMA integrity_check;"` 校验。
2. 冷备（推荐）：`systemctl stop permaporter` → 复制整个数据目录 → `systemctl start permaporter`。
3. 存储树体积远大于库；只备库文件时，恢复后缺失的产物会按需回源重建（磁盘有文件而索引缺失的场景由 go 侧命中对账与清理任务自愈），但 `permaporter.db` 必须备份，否则管理员密码与各生态配置丢失。

**恢复顺序**：

1. 停服；2. 还原整个数据目录（或库文件 + 存储树，保持目录结构与 `ecosystem_settings.storage_dir` 指向一致）；3. 启服；4. `curl` 各端口 `/healthz` 验证，`sqlite3` integrity_check 可选。

### PostgreSQL 备份要点

PostgreSQL 模式下数据 = **PG 库（全部表）+ 四棵存储树 + `config/permaporter.toml`**：

- 库内数据用 `pg_dump` / `pg_dumpall` 常规备份即可（服务自身只经单写连接访问，无额外一致性要求）：`pg_dump -U permaporter -d permaporter -Fc -f permaporter.dump`。
- `permaporter.toml` 含连接字段（PG 密码明文），务必随备份保存并保持 0600 权限。
- 四棵存储树的备份/恢复与 SQLite 模式完全一致（存储树不涉及数据库方言）。
- compose 部署的 PG 数据目录在宿主机 `./pgdata/`，也可直接停服后整目录冷备。

## 开发期重置

开发期数据可随时丢弃（正式部署**不要**执行）：

```bash
systemctl stop permaporter  # 先停止服务
make db-reset               # 删除 data/permaporter.db
systemctl start permaporter # 下次启动按目标 schema 重新建库，之后经 /admin/setup 重新初始化
```

重置会**丢弃库内全部数据**（管理员密码、各生态配置、索引与统计）；四棵存储树不在重置范围内，如需彻底重来请一并手动清空。初始化与启动校验细则见 [docs/single-db.md](single-db.md)。

## 升级

1. 备份（见上）。
2. 停服：`systemctl stop permaporter`（优雅退出，统计落盘）。
3. 替换二进制（`install.sh` 重复执行即可覆盖 `/usr/local/bin/permaporter`；env/service 已存在则跳过）。
4. 启服：`systemctl start permaporter`；schema 变更由服务启动时自动初始化（新增生态组件按可加性演进自动补齐）。
5. 核对版本：`/usr/local/bin/permaporter -version` 或 `curl :9090/healthz` 的 `version` 字段。

## 回滚

1. 停服；2. 还原上一版二进制；3. 还原备份的数据目录（如新版本已运行过）；4. 启服并核对 `/healthz`。数据库 schema 向后兼容由每次升级前的备份保证，回滚即恢复备份。

## 卸载

包内 `uninstall.sh`（与 `install.sh` 同目录）一键卸载：

```bash
sudo ./uninstall.sh          # 停止并注销服务，移除二进制、systemd 单元、/etc/default/permaporter 与系统用户；保留数据目录
sudo ./uninstall.sh --purge  # 连同数据目录（单库 + 四棵存储树 + 配置文件）与环境文件中自定义的 CONFIG_DIR 一并删除
```

- 默认保留 `/var/lib/permaporter`（重装可无缝续用：管理员凭据、索引与缓存产物都在）；确认不再需要时用 `--purge` 或手动删除。
- `*_STORAGE_DIR` / `DB_DSN` 指向数据目录之外的自定义路径不在清理范围，需手动处理。

## 常见故障排查

| 症状 | 可能原因 | 排查命令 |
| :--- | :--- | :--- |
| 服务拒绝启动，日志"端口校验失败" | 端口配置重复 | `env \| grep -E 'UI_PORT\|NPM_PORT\|GO_PORT\|PY_PORT\|MVN_PORT'`，核对 `/etc/default/permaporter` |
| 服务拒绝启动，日志"存储目录…相同或互为嵌套" | 存储树配置冲突 | `env \| grep STORAGE_DIR` |
| 服务拒绝启动，日志"配置文件写入失败…mkdir …: permission denied" | 配置目录不可写：`CONFIG_DIR` 为相对路径时按 `WorkingDirectory` 解析，目录属主非运行用户 | `ls -ld /var/lib/permaporter /var/lib/permaporter/config`（属主应为 permaporter）；重跑 `install.sh`，或手工 `chown permaporter:permaporter /var/lib/permaporter /var/lib/permaporter/config` |
| 控制台 9090 打不开（一直转圈/超时） | 服务未启动（启动判定失败时不监听任何端口），或 9090 被旧实例/旧容器占用 | `systemctl status permaporter`；`ss -ltnp \| grep 9090`；`docker ps`（旧 compose 容器占用则 `docker compose down`） |
| 服务拒绝启动，日志"schema_version 缺失/不匹配" | 单库不是本服务初始化的库，或版本不匹配 | 核对 `DB_DSN` 指向；确认可丢弃后执行 `make db-reset` 重建 |
| 端口监听失败退出 | 端口被占用 | `ss -ltnp \| grep -E '9090\|9091\|9092\|9093\|9094'` |
| `/healthz` 返回 503、`db":"fail"` | 库文件损坏/磁盘满/权限不足 | 日志慢查询与 PRAGMA 报错；`df -h <数据目录>`；`sqlite3 permaporter.db "PRAGMA integrity_check;"` |
| npm 客户端 404（包明明存在） | registry 配置指到其他协议端口 | `npm config get registry`，应为 `:9091/` |
| go 命令一直 404 | GOPROXY 指到其他协议端口，或模块从未被请求过（冷缓存首次拉取需上游可达） | `go env GOPROXY`；`curl :9092/<mod>/@v/list` 看具体错误文本 |
| pip 拉取 404 | index-url 未指向 `:9093/simple`，或项目从未被请求过 | `pip config get global.index-url`；`curl :9093/simple/<project>/` |
| mvn/gradle 拉取 404 | mirror/repo 未指向 `:9094`，或坐标从未被请求过 | 核对 `settings.xml`/`build.gradle`；`curl :9094/<group 路径>/<artifact>/maven-metadata.xml` |
| 元数据不更新 | 上游不可达进入 1 分钟刷新冷却；或 META_TTL 设置过大 | `curl :9091/healthz` 看 `inflight`；控制台对应管理页强制刷新单个对象 |
| 回源缓慢 / 大量 502 | 出站代理失效、上游限流 | 控制台"系统配置 → 测试"；`curl -x <proxy> <上游地址>` |
| 磁盘写满 / 507 | 缓存只增不减 | 控制台仪表盘磁盘水位（<5GB 告警）；手动触发清理；扩容或调整对应 `*_STORAGE_DIR` 位置 |
| 登录提示 IP 已封禁 | 5 次密码错误触发 15 分钟封禁 | 等待 15 分钟或重启进程（限流在内存） |
| 重启后需重新登录 | 会话在内存中 | 属预期行为 |
