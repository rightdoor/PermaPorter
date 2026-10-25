# PermaPorter HTTP API

全部端点均从代码提取。约定：

- **协议代理端点**：包/模块管理客户端使用，无认证，只读。
- **公开 JSON API**：控制台公开数据，无认证。
- **管理 JSON API**：需要登录（Cookie `permaporter_session`，经 `POST /api/auth/login` 获取）。
- 错误响应统一形态：`{"error": "<信息>"}`（npm 端口与控制台 JSON API）；9092、9093 与 9094 的协议错误为 `text/plain; charset=utf-8` 纯文本。
- 成功响应统一包裹：`{"ok": true, …}`（控制台 JSON API）。

---

## 9090 控制台

### 静态资源与 SPA

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| GET | `/static/*filepath` | 内嵌前端产物（Vite 输出）；带内容 hash 的 JS/CSS 一年 immutable，其余 `max-age=0 must-revalidate`；按内容 ETag 支持 304 |
| GET | `/`、`/goproxy`、`/npmregistry`、`/pythonsimple`、`/maven`、`/module/*rest`、`/package/*rest`、`/pyproject/*rest`、`/mvnartifact/*rest`、`/admin`、`/admin/*rest` | SPA 外壳（HTML 内注入 `window.__STATE__`：`initialized/authed/version/goProxy/npmRegistry/pyIndex/mavenRepo`）；未列出的路径由前端展示 404 页 |
| 其他 | NoRoute | 浏览器（GET + `Accept: text/html`）→ SPA 外壳；其余 → `404 {"error":"not found"}` |

### 公开 JSON API（无认证）

| 方法 | 路径 | 成功响应（`ok` 省略） | 失败 |
| :--- | :--- | :--- | :--- |
| GET | `/api/state` | `{"initialized":bool,"authed":bool,"setupMode":bool,"version":"…","goProxy":"http://host:9092","npmRegistry":"http://host:9091/","pyIndex":"http://host:9093/simple/","mavenRepo":"http://host:9094"}`（`setupMode` 仅初始化模式为 true） | — |
| GET | `/api/summary` | `{"go":{"moduleCount":n,"verCount":n,"totalSize":n,"hitRate":f},"npm":{"pkgCount":n,"verCount":n,"totalSize":n,"hitRate":f},"py":{"projectCount":n,"fileCount":n,"totalSize":n,"hitRate":f},"mvn":{"artifactCount":n,"fileCount":n,"totalSize":n,"hitRate":f},"authed":bool}` | 生态段降级为 `{"ok":false,"error":…,"status":n}` |
| GET | `/api/pyprojects?q=&page=` | `{"items":[{projectName,displayName,latestVersion,summary,fileCount,cachedFileCount,totalSize,lastCached}],"total":n,"page":n,"totalPages":n}`（24/页） | 500 `{"error":"查询失败"}` |
| GET | `/api/pyproject/{projectName}` | `{project:{projectName,displayName,latestVersion,summary,metaCachedAt,lastAccessedAt,fileCount,cachedFileCount,cachedTotalSize}, files:[{id,version,filename,fileSizeBytes,cached,sha256,requiresPython,uploadTime,cachedAt,downloadURL}], cachedCount, cachedSize, showRefresh}` | 404 `{"error":"项目不存在"}` |
| GET | `/api/modules?q=&page=` | `{"items":[{modulePath,summary,latestVersion,versionCount,totalSize,lastCached}],"total":n,"page":n,"totalPages":n}`（24/页） | 500 `{"error":"查询失败"}` |
| GET | `/api/module/{modulePath}?v=` | `{module:{modulePath,latestVersion,latestTime,metaCachedAt,lastAccessedAt}, readmeHTML, goMod, versions:[{id,version,cached,zipSizeBytes,modSizeBytes,publishedAt,cachedAt,pseudo}], versionTotal, versionsTruncated, current:{version,zipSizeBytes,modSizeBytes,publishedAt,cachedAt,goVersion,requirements:[{path,version,indirect}],pseudo}, cachedCount, cachedSize, zipURL, modURL, infoURL, pkgGoDevURL, showRefresh}` | 404 `{"error":"模块不存在"}` |
| GET | `/api/packages?q=&page=` | `{"items":[{pkgName,description,latestVersion,versionCount,totalSize,lastCached}],"total":n,"page":n,"totalPages":n}`（24/页） | 500 `{"error":"查询失败"}` |
| GET | `/api/mvnartifacts?q=&page=` | `{"items":[{name,groupId,artifactId,latestVersion,versionCount,fileCount,cachedFileCount,totalSize,lastCached}],"total":n,"page":n,"totalPages":n}`（24/页） | 500 `{"error":"查询失败"}` |
| GET | `/api/mvnartifact/{groupId}:{artifactId}` | `{artifact:{name,groupId,artifactId,latestVersion,versionCount,lastUpdated,metaCachedAt,lastAccessedAt,fileCount,cachedFileCount,cachedTotalSize}, files:[{id,repoPath,version,filename,fileSizeBytes,cached,sha1,cachedAt,downloadURL}], cachedCount, cachedSize, showRefresh}` | 404 `{"error":"构件不存在"}` |
| GET | `/api/package/{pkgName}?v=` | `{pkg:{pkgName,description,latestVersion,license,homepageURL,repositoryURL,metaCachedAt,lastAccessedAt}, readmeHTML, versions:[{id,version,fileSizeBytes,publishedAt,cachedAt}], versionTotal, versionsTruncated, current:{version,fileSizeBytes,publishedAt,cachedAt,deps,devDeps,peerDeps}, cachedCount, cachedSize, downloadURL, showRefresh}`（deps 为 `[{name,range}]`） | 404 `{"error":"包不存在"}` |

> 独立搜索接口（`/api/search`）与搜索页已移除：GO / NPM 列表页用各自工具栏直搜（`/api/modules`、`/api/packages` 的 `q` 参数）。

### 初始化向导 API（仅 9090，且仅初始化模式注册；数据库已配置后全部不存在）

| 方法 | 路径 | 请求体 | 成功 | 失败 |
| :--- | :--- | :--- | :--- | :--- |
| GET | `/api/setup/status` | — | `{"configured":false,"configFile":"/path/permaporter.toml","initTokenRequired":bool,"ports":{ui,npm,go,py,mvn},"upstreams":{go,goFallback,npm,py,mvn}}`（可预填值：生效端口与环境变量默认上游） | 404 已完成初始化（向导端点已下线，落入完整控制台 404） |
| POST | `/api/setup/test-database` | `{"token?":"…","driver":"sqlite\\|postgresql","dsn?":"…","host?":"…","port?":n,"user?":"…","password?":"…","database?":"…","sslmode?":"…"}` | `{"ok":true,"message":"连接成功"}`（只测连通，不落盘不建表） | 400 driver 非法/字段缺失/连接失败；401 令牌错误 |
| POST | `/api/setup/initialize` | 上述数据库字段 + `{"ports":{ui,npm,go,py,mvn},"upstreams":{go,goFallback,npm,py,mvn},"adminPassword":"…","confirm":"…"}`（≥6 位） | `{"ok":true,"ports":{…}}`；原子写回配置文件 → 建库 → 写管理员密码与初始上游 → 原地启动全部端口 | 400 校验失败（端口互异/密码/上游格式等）；401 令牌错误；404 已完成初始化（向导端点已下线，落入完整控制台 404）；500 初始化失败 |
| GET | `/healthz`（初始化模式） | — | `{"ok":true,"ui":"ok","setupPending":true,"version":"…"}` | — |

> 重新进入向导：手工清空/注释配置文件 `[database]` 段的 `driver` 后重启；向导不删除既有数据，但管理员密码步骤会重置密码。未初始化前向导对网络开放，公网部署建议设置 `INIT_TOKEN`。

### 认证 JSON API（仅 9090）

| 方法 | 路径 | 请求体 | 成功 | 失败 |
| :--- | :--- | :--- | :--- | :--- |
| POST | `/api/auth/setup` | `{"password":"…","confirm":"…"}`（≥6 位，仅未初始化时可用） | `200 {"ok":true}`；写 platform_settings 并按默认配置补齐各生态配置行 | 409 已初始化；400 格式/长度/不一致；500 写库失败 |
| POST | `/api/auth/login` | `{"password":"…"}` | `200 {"ok":true}` + 会话 Cookie（24h，HttpOnly，SameSite=Lax） | 429 IP 封禁（5 次失败/15 分钟）；401 密码错误（附剩余次数）；500 未初始化 |
| POST | `/api/auth/logout` | — | `200 {"ok":true}`，销毁会话 | — |

### 管理 JSON API（`/api/admin`，全部需登录）

生态分派：标 `[eco]` 的接口支持 `?eco=go|npm|py|mvn`（缺省 go）。

| 方法 | 路径 | 参数 / 请求体 | 成功响应要点 |
| :--- | :--- | :--- | :--- |
| GET | `/api/admin/dashboard` | — | `{"go":{moduleCount,versionCount,totalSize,hitRate,recent:[{modulePath,version,zipSize,cachedAt}],disk:{free,total,warn,err,inflight},trend:[{day,meta,zip,hits}]},"npm":{…tarball 版…},"py":{…file 版…},"mvn":{artifactCount,fileCount,totalSize,hitRate,recent:[{name,groupId,artifactId,version,filename,fileSizeBytes,cachedAt}],disk:{…},trend:[{day,meta,file,hits}]}}`（趋势 14 天） |
| GET | `/api/admin/modules` | `?q=&sort=name|versions|size|time&dir=asc|desc&page=` | 同 `/api/modules` 形态（20/页） |
| GET | `/api/admin/module-versions` | `?module=` | `{"modulePath":"…","versions":[{version,zipSizeBytes,modSizeBytes,zipCached,modCached,infoCached,cachedAt,pseudo}]}`（仅含有任何产物的版本） |
| DELETE | `/api/admin/module-version` | `?module=&version=` | `{"moduleRemoved":bool}`；删空时连 `@v` 目录一起清理 |
| POST | `/api/admin/module-refresh` | `?module=` | `{"ok":true}`（强制刷新 @v/list 与 @latest） |
| GET | `/api/admin/packages` | `?q=&sort=&dir=&page=` | 同 `/api/packages` 形态（20/页） |
| GET | `/api/admin/package-versions` | `?pkg=` | `{"pkgName":"…","versions":[{id,version,fileSizeBytes,cachedAt}]}`（仅已缓存版本） |
| DELETE | `/api/admin/package-version` | `?pkg=&version=` | `{"pkgRemoved":bool}`；删空时连包与 meta 文件一起删 |
| POST | `/api/admin/package-refresh` | `?pkg=` | `{"ok":true}` |
| GET | `/api/admin/pyprojects` | `?q=&sort=&dir=&page=` | 同 `/api/pyprojects` 形态（20/页） |
| GET | `/api/admin/pyproject-files` | `?project=` | `{"projectName":"…","files":[{id,version,filename,fileSizeBytes,sha256,requiresPython,uploadTime,cachedAt}]}`（仅已缓存文件） |
| DELETE | `/api/admin/py-file` | `?filename=` | `{"ok":true}`；项目记录保留 |
| POST | `/api/admin/py-refresh` | `?project=` | `{"ok":true}`（强制刷新锚点页） |
| GET | `/api/admin/mvnartifacts` | `?q=&sort=&dir=&page=` | 同 `/api/mvnartifacts` 形态（20/页） |
| GET | `/api/admin/mvnartifact-files` | `?coords=groupId:artifactId` | `{"name":"g:a","files":[{id,repoPath,version,filename,fileSizeBytes,sha1,cachedAt}]}`（仅已缓存文件） |
| DELETE | `/api/admin/mvn-file` | `?path=<仓库相对路径>` | `{"ok":true}`；构件记录保留 |
| POST | `/api/admin/mvn-refresh` | `?coords=groupId:artifactId` | `{"ok":true}`（强制刷新构件级 maven-metadata.xml） |
| GET | `/api/admin/preheat` `[eco]` | — | `{"job":{id,withTarball|withZip,createdAt,finished,done,failed,total,items:[{name,state,version,size,err}]}}`（无任务时 `job:null`） |
| POST | `/api/admin/preheat` `[eco]` | go：`{"deps":"go.mod/go.sum/逐行 module@version","withZip":bool}`；npm：`{"deps":"package.json/逐行包名","withTarball":bool}`；mvn：`{"deps":"pom.xml/Gradle/逐行 g:a[:v][:c]","withFiles":bool}`（单次上限 500） | `{"total":n,"job":{…}}` |
| GET | `/api/admin/logs` `[eco]` | — | `{"entries":[{time,ip,kind,pkg,hit,durMs,err}]}`（最近 200 条） |
| GET | `/api/admin/settings` | — | `{"go":{upstreamUrl,fallbackUrl,outboundProxy,storageDir},"npm":{…},"py":{…},"mvn":{…}}`（不含密码哈希） |
| POST | `/api/admin/settings/upstream` `[eco]` | `{"upstreamUrl":"…","fallbackUrl":"…"}` | `{"ok":true}`；npm 侧 `fallbackUrl` 同样接受 |
| POST | `/api/admin/settings/proxy` `[eco]` | `{"outboundProxy":"http(s)://…或socks5://…或空"}` | `{"ok":true}` |
| POST | `/api/admin/settings/test-proxy` `[eco]` | `{"outboundProxy":"…"}` | `{"ok":true,"latencyMs":n}`；502 `{"error":"连接失败: …"}` |
| POST | `/api/admin/settings/password` | `{"newPassword":"…","confirm":"…"}`（≥6 位） | `{"ok":true}`（写单库 platform_settings） |
| POST | `/api/admin/settings/cleanup` `[eco]` | — | `{"running":bool,"startedAt":"…","finished":bool,"message":"…","err":""}`；已在运行时 409 |
| GET | `/api/admin/settings/cleanup` `[eco]` | — | 同上形态（轮询用，完成时附 `finishedAt`/`durationMs`） |
| GET | `/api/admin/system-info` | — | `{"driver":"sqlite\\|postgresql","configFile":"/path/permaporter.toml","ports":{ui,npm,go,py,mvn},"storageDirs":{go,npm,py,mvn},"hint":"…"}`（只读：修改需编辑服务器配置文件并重启） |

管理接口失败形态：400 `{"error":"参数格式无效"}` / 404 `{"error":"包不存在"|"模块不存在"}` / 401 `{"error":"未登录或登录已过期"}` / 409 / 500。

### 健康检查与指标（9090，无认证）

| 方法 | 路径 | 输出 |
| :--- | :--- | :--- |
| GET | `/healthz` | `200 {"ok":true,"ui":"ok","uptime":"1h2m3s","version":"1.0.0","go":{"ok":true,"db":"ok","ready":true,"uptime":"…","inflight":0},"npm":{…},"py":{…},"mvn":{…}}`（聚合各生态 healthz 响应体） |
| GET | `/metrics` | `200 text/plain`：`go_permaporter_*`、`npm_permaporter_*`、`py_permaporter_*`、`mvn_permaporter_*` 全部样本直接拼接 |
| 任意 | `/debug/pprof/*` | 仅 `PPROF=1` 时注册；需登录；索引/cmdline/profile/symbol/trace/allocs/block/goroutine/heap/mutex/threadcreate |

---

## 9091 npm Registry（协议端口，无认证）

| 方法 | 路径 | 行为 |
| :--- | :--- | :--- |
| GET | `/healthz` | `200 {"ok":true,"db":"ok","ready":true,"uptime":"…","inflight":n}`；DB 不可用 503 |
| GET | `/metrics` | `200 text/plain`：`npm_permaporter_meta_requests_total`、`npm_permaporter_meta_hits_total`、`npm_permaporter_tarball_requests_total`、`npm_permaporter_tarball_hits_total`（counter）；`npm_permaporter_cache_hit_ratio`、`npm_permaporter_upstream_inflight`、`npm_permaporter_cached_packages`、`npm_permaporter_cached_versions`、`npm_permaporter_cache_bytes`、`npm_permaporter_uptime_seconds`、`npm_permaporter_build_info{version=…}`（gauge） |
| GET/HEAD | `/{pkg}` | packument。命中：`200 application/json`（`dist.tarball` 已改写为本站地址）；未缓存：回源后同上；上游不存在：`404 {"error":"上游不存在该包"}`；上游故障：`502 {"error":"上游请求失败: …"}` |
| GET/HEAD | `/{pkg}/-/{filename}.tgz` | tarball。磁盘命中：`200 application/octet-stream` + `X-Proxy-Cache: HIT` + `Cache-Control: public, max-age=31536000, immutable`（支持 ETag/Range）；未命中：下载校验后返回 + `X-Proxy-Cache: MISS`；无版本记录：透传上游不落盘；失败：`502 {"error":"tarball 拉取失败: …"}` |
| POST | `/-/npm/v1/security/advisories*` | audit 安全查询透传（只读语义，请求体 ≤1MB），不缓存 |
| 其他方法 | 任意协议路径 | `405 {"error":"此镜像为只读代理，禁止发布操作"}` |
| GET | `/admin*`（浏览器） | SPA 外壳 HTML（404 引导）；非浏览器 `404 {"error":"not found"}` |
| GET | 其他未知路径 | 浏览器 → SPA 外壳；非浏览器 `404 {"error":"not found"}` |
| GET | `/api/…` 等非包名路径 | `404 {"error":"not found"}`（不满足包名格式） |
| 任意 | 生态配置读取失败（库不可用等运行期降级） | `503 {"error":"服务未完成初始化"}`（未初始化向导模式下协议端口不监听，连接拒绝） |

---

## 9092 GOPROXY（协议端口，无认证）

除 `/healthz`、`/metrics`、`/sumdb/*` 外全部经 NoRoute 协议分发；**所有错误为 `text/plain; charset=utf-8`**。

| 方法 | 路径 | 行为 |
| :--- | :--- | :--- |
| GET | `/healthz` | 同 9091 形态（本口数据） |
| GET | `/metrics` | `200 text/plain`：`go_permaporter_meta_requests_total`、`go_permaporter_meta_hits_total`、`go_permaporter_zip_requests_total`、`go_permaporter_zip_hits_total`（counter）；`go_permaporter_cache_hit_ratio`、`go_permaporter_upstream_inflight`、`go_permaporter_cached_modules`、`go_permaporter_cached_versions`、`go_permaporter_cache_bytes`、`go_permaporter_uptime_seconds`、`go_permaporter_build_info{version=…}`（gauge） |
| GET/HEAD | `/{module}/@v/list` | `200 text/plain; charset=utf-8` + `no-cache`（规范化版本行，semver 升序）；上游不存在 404；上游故障 502 |
| GET/HEAD | `/{module}/@latest` | `200 application/json` + `no-cache`（上游原始 JSON） |
| GET/HEAD | `/{module}/@v/{ver}.info` | `200 application/json`，`Cache-Control: public, max-age=31536000, immutable`（支持 ETag/Range）；非规范 token（分支/提交号）透传上游不落盘（`no-cache`） |
| GET/HEAD | `/{module}/@v/{ver}.mod` | `200 text/plain; charset=utf-8`，immutable |
| GET/HEAD | `/{module}/@v/{ver}.zip` | `200 application/zip`，immutable；磁盘命中零拷贝（支持 Range）；磁盘不足 `507` |
| GET/HEAD | `/{module}/@v/{ver}.ziphash` | `200 text/plain; charset=utf-8`，immutable（本地缓存优先，缺失时确保 zip 后以本地 h1 值补算） |
| GET/HEAD | `/sumdb/{name}/supported` | 上游支持 `200`（空体）；不支持/未知 `404`（客户端回退直连 sumdb）；探测结果缓存 10 分钟 |
| GET/HEAD | `/sumdb/{name}/latest`、`/lookup/…`、`/tile/…` | 透传上游状态码与响应体（≤4MB），不落盘；`latest` 加 `Cache-Control: no-store`；上游不可用 502 并失效 supported 探测缓存 |
| 其他 | 非协议路径 | `404 text/plain "not found"`（浏览器形态输出 SPA 外壳或 302 详情页） |
| 其他 | 协议路径参数非法 | `400 text/plain`（如"非法的 GOPROXY 路径"、"版本号包含非法字符"） |
| 其他方法 | 协议路径 | `405 text/plain "此镜像为只读代理，禁止发布操作"` |
| 任意 | 生态配置读取失败（库不可用等运行期降级） | `503 text/plain "服务未完成初始化"`（未初始化向导模式下协议端口不监听，连接拒绝） |

模块路径校验：先 `module.UnescapePath` 再 `module.CheckPath`，超长（>512）拒绝；版本 token 禁止路径分隔符、`..`、空白与控制字符（防路径穿越）。


## 9093 PyPI Simple（协议端口，无认证）

除 `/healthz`、`/metrics` 外全部经 NoRoute 协议分发；**所有错误为 `text/plain; charset=utf-8`**。协议为 PEP 503：项目名规范化（`-_.` 折叠为 `-` 转小写），锚点页由 `py_files` 表生成（相对路径锚点），发行文件不可变。

| 方法 | 路径 | 行为 |
| :--- | :--- | :--- |
| GET | `/healthz` | 同 9091 形态（本口数据） |
| GET | `/metrics` | `200 text/plain`：`py_permaporter_index_requests_total`、`py_permaporter_index_hits_total`、`py_permaporter_file_requests_total`、`py_permaporter_file_hits_total`（counter）；`py_permaporter_cache_hit_ratio`、`py_permaporter_upstream_inflight`、`py_permaporter_cached_projects`、`py_permaporter_cached_files`、`py_permaporter_cache_bytes`、`py_permaporter_uptime_seconds`、`py_permaporter_build_info{version=…}`（gauge） |
| GET/HEAD | `/simple/` | `200 text/html; charset=utf-8`（本地已收录项目清单，字典序；不回源全量清单） |
| GET/HEAD | `/simple/{project}` | `301` 补齐尾斜杠 |
| GET/HEAD | `/simple/{project}/` | `200 text/html; charset=utf-8` 锚点页（每个发行文件一个锚点，带 `#sha256=` 片段与 `data-requires-python`）；TTL 过期先条件刷新（ETag 304 只更新时间戳），刷新失败/超时回退旧页；上游不存在 404；上游故障 502 |
| GET/HEAD | `/packages/{filename}` | `200 application/octet-stream`，immutable（磁盘命中零拷贝，支持 Range）；未缓存按锚点页记录的上游地址回源，sha256 边下边验、截断检测、临时文件原子改名；记录不存在 404；上游故障 502 |
| 其他 | — | 浏览器形态回落 SPA 外壳；其余 `404 text/plain "not found"`；非 GET/HEAD `405` |


---

## 9094 Maven 仓库（协议端口，无认证）

除 `/healthz`、`/metrics` 外全部经 NoRoute 协议分发；**所有错误为 `text/plain; charset=utf-8`**。协议为 Maven 仓库布局（groupId 的 `.` 映射为路径分隔符），Maven 与 Gradle 客户端共用：元数据 `maven-metadata.xml`（构件级版本清单 / 版本级 SNAPSHOT 映射）按 TTL 条件刷新（ETag 304 只更新时间戳，raw XML 原样透传），构件文件（jar/pom/sources/module/asc 等）不可变、按上游 `.sha1` 边下边验后镜像仓库目录布局落盘；`<资源>.sha1/.md5/.sha256/.sha512` 由本地缓存内容即时计算。

| 方法 | 路径 | 行为 |
| :--- | :--- | :--- |
| GET | `/healthz` | 同 9091 形态（本口数据） |
| GET | `/metrics` | `200 text/plain`：`mvn_permaporter_meta_requests_total`、`mvn_permaporter_meta_hits_total`、`mvn_permaporter_file_requests_total`、`mvn_permaporter_file_hits_total`（counter）；`mvn_permaporter_cache_hit_ratio`、`mvn_permaporter_upstream_inflight`、`mvn_permaporter_cached_artifacts`、`mvn_permaporter_cached_files`、`mvn_permaporter_cache_bytes`、`mvn_permaporter_uptime_seconds`、`mvn_permaporter_build_info{version=…}`（gauge） |
| GET/HEAD | `/{group 路径}/{artifact}/maven-metadata.xml` | `200 application/xml` + `no-cache`（raw XML 透传，`X-Proxy-Cache` 标记命中）；TTL 过期先条件刷新；上游不存在 404；上游故障 502 |
| GET/HEAD | `/{group 路径}/{artifact}/{version}/maven-metadata.xml` | 版本级（SNAPSHOT）metadata，行为同上（raw 透传，不做构件记账） |
| GET/HEAD | `/{group 路径}/{artifact}/{version}/{file}` | 构件文件。磁盘命中：`200 application/octet-stream` + `X-Proxy-Cache: HIT` + immutable（支持 ETag/Range）；未命中：回源下载（上游 `.sha1` 可得时强制校验）后返回 `X-Proxy-Cache: MISS`；上游全部 404 → 404；上游故障 502 |
| GET/HEAD | `<上述任意资源>.sha1/.md5/.sha256/.sha512` | `200 text/plain`（本地缓存内容计算的 hex 摘要）；目标资源未缓存时先确保缓存 |
| 其他 | — | 浏览器形态回落 SPA 外壳；其余 `404 text/plain "not found"`；路径段白名单不匹配 `400`；非 GET/HEAD `405 text/plain` |

路径校验：每段必须匹配 `^[A-Za-z0-9][A-Za-z0-9._-]*$`（挡 `..` 与编码残留），段数 ≤12、总长 ≤512；构件文件名必须以 `<artifactId>-<version>` 开头（SNAPSHOT 时间戳文件名按去 `-SNAPSHOT` 前缀判定）；DB 记录拼磁盘路径前经 `StorageFilePath` 越界断言。

---

## 会话与安全约束速查

- 登录仅发生在 9090；Cookie `permaporter_session` 仅 9090 签发（HttpOnly、SameSite=Lax、https 反代时加 Secure、24h 滑动续期）。
- 登录失败 5 次 → IP 封禁 15 分钟（429）。
- 全部管理接口 401 形态一致：`{"error":"未登录或登录已过期"}`。
- 出站代理仅支持 `http://`、`https://`、`socks5://` 前缀；上游地址必须 `http(s)://` 开头。
