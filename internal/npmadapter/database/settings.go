package database

import (
	"time"

	"permaporter/internal/npmadapter/models"
)

// IsInitialized 判断是否已完成首次初始化（ecosystem_settings 存在本生态行）。
// 命中 settings 进程内缓存时零 DB 开销（该函数每个代理请求都会调用）。
func (db *DB) IsInitialized() bool {
	if db.settings.Load() != nil {
		return true
	}
	if _, err := db.GetSettings(); err == nil {
		return true
	}
	return false
}

// GetSettings 读取 npm 生态配置（单库 ecosystem_settings 的 ecosystem='npm' 行；
// 进程内缓存，配置更新时自动失效）。返回共享只读指针，调用方不得修改其字段。
func (db *DB) GetSettings() (*models.Settings, error) {
	if s := db.settings.Load(); s != nil {
		return s, nil
	}
	s := &models.Settings{}
	var metaTTL int64
	err := db.QueryRow(
		`SELECT upstream_url, COALESCE(fallback_url,''),
		        COALESCE(outbound_proxy,''), COALESCE(storage_dir,'./storage'),
		        COALESCE(meta_ttl,120000)
		 FROM ecosystem_settings WHERE ecosystem = 'npm'`,
	).Scan(&s.UpstreamURL, &s.FallbackURL, &s.OutboundProxy, &s.StorageDir, &metaTTL)
	if err != nil {
		return nil, err
	}
	s.MetaTTL = time.Duration(metaTTL) * time.Millisecond
	if s.MetaTTL <= 0 {
		s.MetaTTL = 120 * time.Second // 非法值兜底：与 META_TTL 环境变量默认值一致
	}
	db.settings.Store(s)
	return s, nil
}

// invalidateSettings 使 settings 进程内缓存失效（配置变更后下次读取重新查库）。
func (db *DB) invalidateSettings() {
	db.settings.Store(nil)
}

// InitSettings 首次初始化：写入 npm 生态配置行（默认主上游、存储目录与 meta_ttl）。
// fallback_url 初始为空（备用上游可在控制台按生态设置）；管理员密码不在生态配置内，
// 统一存单库 platform_settings。
func (db *DB) InitSettings(upstream, storageDir string, metaTTL time.Duration) error {
	_, err := db.Exec(
		`INSERT INTO ecosystem_settings
		 (ecosystem, upstream_url, fallback_url, outbound_proxy, storage_dir, meta_ttl)
		 VALUES ('npm', ?, '', '', ?, ?)
		 ON CONFLICT(ecosystem) DO UPDATE SET
			upstream_url = excluded.upstream_url,
			fallback_url = excluded.fallback_url,
			outbound_proxy = excluded.outbound_proxy,
			storage_dir = excluded.storage_dir,
			meta_ttl = excluded.meta_ttl`,
		upstream, storageDir, int64(metaTTL/time.Millisecond))
	if err == nil {
		db.invalidateSettings()
	}
	return err
}

// UpdateUpstreams 更新主/备用上游源，立即生效。
func (db *DB) UpdateUpstreams(upstream, fallback string) error {
	_, err := db.Exec(
		`UPDATE ecosystem_settings SET upstream_url = ?, fallback_url = ? WHERE ecosystem = 'npm'`,
		upstream, fallback)
	if err == nil {
		db.invalidateSettings()
	}
	return err
}

// UpdateOutboundProxy 更新出站代理地址，立即生效。
func (db *DB) UpdateOutboundProxy(proxyURL string) error {
	_, err := db.Exec(
		`UPDATE ecosystem_settings SET outbound_proxy = ? WHERE ecosystem = 'npm'`,
		proxyURL)
	if err == nil {
		db.invalidateSettings()
	}
	return err
}
