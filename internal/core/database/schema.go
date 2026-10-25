package database

// 单库目标 schema：一个 SQLite 文件（data/permaporter.db）承载平台与四生态全部数据。
//
// 生态隔离靠表名前缀（go_* / npm_* / py_* / mvn_*），不用 ecosystem 判别列：
// 判别列漏写即跨生态误删；前缀让错表在评审期暴露。各生态的数据表
// 绝不合并成一张宽表，四套清理语义也绝不通用化。
//
// 新增生态 = 新增前缀表组件：schema_version 行缺失视为可加性演进，
// 启动时自动建表并补写行（幂等），不拒启、不做数据迁移。

// schemaComponent 目标 schema 的组件：名称、版本与建表/建索引语句合并在同一处，
// 新增组件只改这一张表（名称/版本/DDL 分离的双切片曾靠顺序对齐，漏改一边只能
// 运行期暴露）。建库时写入版本行；启动校验要求各行版本一致，否则拒绝启动。
	// 顺序：平台/生态配置 → 版本标记 → go 生态（先 modules 后 versions，外键引用）
	// → npm 生态 → py 生态 → maven 生态。
var schemaComponents = []struct {
	Name    string
	Version int
	DDL     []string
}{
	{"platform", 1, []string{
		// ---------- 平台：管理员凭据（密码统一存这里，生态侧不再保存） ----------
		`CREATE TABLE IF NOT EXISTS platform_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			admin_password_hash TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		// ---------- 生态配置：各生态一行（ecosystem = 'go' | 'npm' | 'py'） ----------
		// ecosystem 取值由应用层保证（写入侧只使用固定字面量）：
		// 不加 CHECK 约束，避免既有库在新增生态时因旧 CHECK 无法补行（可加性演进要求）。
		`CREATE TABLE IF NOT EXISTS ecosystem_settings (
			ecosystem TEXT PRIMARY KEY,
			upstream_url TEXT NOT NULL DEFAULT '',
			fallback_url TEXT NOT NULL DEFAULT '',
			outbound_proxy TEXT NOT NULL DEFAULT '',
			storage_dir TEXT NOT NULL DEFAULT '',
			meta_ttl INTEGER NOT NULL DEFAULT 120000 -- 毫秒（120s），支持毫秒级 TTL 便于测试
		)`,
		// ---------- schema 版本标记（初始化完成时间） ----------
		`CREATE TABLE IF NOT EXISTS schema_version (
			component TEXT PRIMARY KEY,
			version INTEGER NOT NULL,
			applied_at TEXT NOT NULL DEFAULT ''
		)`,
	}},
	{"go", 1, []string{
		`CREATE TABLE IF NOT EXISTS go_modules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			module_path TEXT UNIQUE NOT NULL,
			latest_version TEXT DEFAULT '',
			latest_time TEXT DEFAULT '',
			meta_cached_at DATETIME,
			latest_cached_at DATETIME,
			list_etag TEXT DEFAULT '',
			readme_content TEXT DEFAULT '',
			readme_version TEXT DEFAULT '',
			summary TEXT DEFAULT '',
			last_accessed_at DATETIME,
			version_count INTEGER NOT NULL DEFAULT 0,
			cached_version_count INTEGER NOT NULL DEFAULT 0,
			cached_total_size INTEGER NOT NULL DEFAULT 0,
			last_cached_at TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// module_path 的 UNIQUE 约束已自带索引
		`CREATE TABLE IF NOT EXISTS go_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			module_id INTEGER NOT NULL,
			version TEXT NOT NULL,
			info_time TEXT DEFAULT '',
			info_cached_at DATETIME,
			mod_cached_at DATETIME,
			zip_cached_at DATETIME,
			zip_size_bytes INTEGER NOT NULL DEFAULT 0,
			mod_size_bytes INTEGER NOT NULL DEFAULT 0,
			zip_path TEXT DEFAULT '',
			ziphash TEXT DEFAULT '',
			mod_content TEXT DEFAULT '',
			requirements_json TEXT DEFAULT '',
			go_version TEXT DEFAULT '',
			listed_at DATETIME,
			is_pseudo INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (module_id) REFERENCES go_modules(id) ON DELETE CASCADE,
			UNIQUE(module_id, version)
		)`,
		// 部分索引：只覆盖已缓存 zip 的版本，服务仪表盘聚合与"已缓存版本"过滤
		`CREATE INDEX IF NOT EXISTS idx_go_version_zip_cached ON go_versions(module_id) WHERE zip_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_go_version_zip_size ON go_versions(zip_size_bytes) WHERE zip_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_go_version_zip_cached_at ON go_versions(zip_cached_at) WHERE zip_size_bytes > 0`,
		`CREATE TABLE IF NOT EXISTS go_status (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			zip_total INTEGER DEFAULT 0,
			zip_hits INTEGER DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 每日代理请求统计（本地日聚合），支撑仪表盘每日趋势图
		`CREATE TABLE IF NOT EXISTS go_daily_stats (
			day TEXT PRIMARY KEY,
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			zip_total INTEGER DEFAULT 0,
			zip_hits INTEGER DEFAULT 0
		)`,
	}},
	{"npm", 1, []string{
		`CREATE TABLE IF NOT EXISTS npm_packages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pkg_name TEXT UNIQUE NOT NULL,
			description TEXT DEFAULT '',
			latest_version TEXT DEFAULT '',
			latest_cached_version TEXT NOT NULL DEFAULT '',
			repository_url TEXT DEFAULT '',
			homepage_url TEXT DEFAULT '',
			license TEXT DEFAULT '',
			readme_content TEXT DEFAULT '',
			meta_cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			meta_etag TEXT NOT NULL DEFAULT '',
			last_accessed_at DATETIME,
			cached_version_count INTEGER NOT NULL DEFAULT 0,
			cached_total_size INTEGER NOT NULL DEFAULT 0,
			last_cached_at TEXT NOT NULL DEFAULT ''
		)`,
		// pkg_name 的 UNIQUE 约束已自带索引
		`CREATE TABLE IF NOT EXISTS npm_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pkg_id INTEGER NOT NULL,
			version TEXT NOT NULL,
			tarball_path TEXT NOT NULL,
			tarball_url TEXT DEFAULT '',
			file_size_bytes INTEGER,
			integrity TEXT NOT NULL DEFAULT '',
			dependencies_json TEXT DEFAULT '',
			dev_dependencies_json TEXT DEFAULT '',
			peer_dependencies_json TEXT DEFAULT '',
			published_at DATETIME,
			cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (pkg_id) REFERENCES npm_packages(id) ON DELETE CASCADE,
			UNIQUE(pkg_id, version)
		)`,
		// 部分索引：只覆盖已缓存版本，服务仪表盘聚合与"已缓存版本"过滤
		`CREATE INDEX IF NOT EXISTS idx_npm_version_cached ON npm_versions(pkg_id) WHERE file_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_npm_version_cached_size ON npm_versions(file_size_bytes) WHERE file_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_npm_version_cached_at ON npm_versions(cached_at) WHERE file_size_bytes > 0`,
		`CREATE TABLE IF NOT EXISTS npm_status (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			tarball_total INTEGER DEFAULT 0,
			tarball_hits INTEGER DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 每日代理请求统计（本地日聚合），支撑仪表盘每日下载趋势图
		`CREATE TABLE IF NOT EXISTS npm_daily_stats (
			day TEXT PRIMARY KEY,
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			tarball_total INTEGER DEFAULT 0,
			tarball_hits INTEGER DEFAULT 0
		)`,
	}},
	{"py", 1, []string{
		`CREATE TABLE IF NOT EXISTS py_projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_name TEXT UNIQUE NOT NULL,
			display_name TEXT DEFAULT '',
			latest_version TEXT DEFAULT '',
			summary TEXT DEFAULT '',
			meta_cached_at DATETIME,
			meta_etag TEXT NOT NULL DEFAULT '',
			last_accessed_at DATETIME,
			file_count INTEGER NOT NULL DEFAULT 0,
			cached_file_count INTEGER NOT NULL DEFAULT 0,
			cached_total_size INTEGER NOT NULL DEFAULT 0,
			last_cached_at TEXT NOT NULL DEFAULT ''
		)`,
		// project_name 的 UNIQUE 约束已自带索引（PEP 503 规范化名）
		`CREATE TABLE IF NOT EXISTS py_files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			version TEXT DEFAULT '',
			filename TEXT NOT NULL,
			file_size_bytes INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT DEFAULT '',
			file_path TEXT DEFAULT '',
			source_url TEXT DEFAULT '',
			requires_python TEXT DEFAULT '',
			upload_time TEXT DEFAULT '',
			cached_at DATETIME,
			FOREIGN KEY (project_id) REFERENCES py_projects(id) ON DELETE CASCADE,
			UNIQUE(project_id, filename)
		)`,
		// 部分索引：只覆盖已缓存文件，服务仪表盘聚合
		`CREATE INDEX IF NOT EXISTS idx_py_file_cached ON py_files(project_id) WHERE file_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_py_file_cached_size ON py_files(file_size_bytes) WHERE file_size_bytes > 0`,
		`CREATE TABLE IF NOT EXISTS py_status (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			index_total INTEGER DEFAULT 0,
			index_hits INTEGER DEFAULT 0,
			file_total INTEGER DEFAULT 0,
			file_hits INTEGER DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 每日代理请求统计（本地日聚合），支撑仪表盘每日下载趋势图
		`CREATE TABLE IF NOT EXISTS py_daily_stats (
			day TEXT PRIMARY KEY,
			index_total INTEGER DEFAULT 0,
			index_hits INTEGER DEFAULT 0,
			file_total INTEGER DEFAULT 0,
			file_hits INTEGER DEFAULT 0
		)`,
	}},
	{"maven", 1, []string{
		`CREATE TABLE IF NOT EXISTS mvn_artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			latest_version TEXT DEFAULT '',
			release_version TEXT DEFAULT '',
			version_count INTEGER NOT NULL DEFAULT 0,
			last_updated TEXT DEFAULT '',
			meta_cached_at DATETIME,
			last_accessed_at DATETIME,
			file_count INTEGER NOT NULL DEFAULT 0,
			cached_file_count INTEGER NOT NULL DEFAULT 0,
			cached_total_size INTEGER NOT NULL DEFAULT 0,
			last_cached_at TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(group_id, artifact_id)
		)`,
		// UNIQUE(group_id, artifact_id) 约束已自带索引
		`CREATE TABLE IF NOT EXISTS mvn_metadata (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_path TEXT UNIQUE NOT NULL,
			xml TEXT NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			cached_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS mvn_files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			artifact_id INTEGER NOT NULL,
			repo_path TEXT UNIQUE NOT NULL,
			version TEXT DEFAULT '',
			filename TEXT NOT NULL,
			file_size_bytes INTEGER NOT NULL DEFAULT 0,
			sha1 TEXT DEFAULT '',
			file_path TEXT DEFAULT '',
			cached_at DATETIME,
			FOREIGN KEY (artifact_id) REFERENCES mvn_artifacts(id) ON DELETE CASCADE,
			UNIQUE(artifact_id, repo_path)
		)`,
		// 部分索引：只覆盖已缓存文件，服务仪表盘聚合
		`CREATE INDEX IF NOT EXISTS idx_mvn_file_cached ON mvn_files(artifact_id) WHERE file_size_bytes > 0`,
		`CREATE INDEX IF NOT EXISTS idx_mvn_file_cached_size ON mvn_files(file_size_bytes) WHERE file_size_bytes > 0`,
		`CREATE TABLE IF NOT EXISTS mvn_status (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			file_total INTEGER DEFAULT 0,
			file_hits INTEGER DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 每日代理请求统计（本地日聚合），支撑仪表盘每日下载趋势图
		`CREATE TABLE IF NOT EXISTS mvn_daily_stats (
			day TEXT PRIMARY KEY,
			meta_total INTEGER DEFAULT 0,
			meta_hits INTEGER DEFAULT 0,
			file_total INTEGER DEFAULT 0,
			file_hits INTEGER DEFAULT 0
		)`,
	}},
}
