package handlers

import "log"

// EnsureInitSettings 确保 npm 生态配置行存在（缺失时按默认配置补齐）。
// 密码统一存于单库 platform_settings，生态配置行不含密码——
// 已初始化的生态行原样不动。
func (a *App) EnsureInitSettings() error {
	if a.DB.IsInitialized() {
		return nil
	}
	if err := a.DB.InitSettings(a.Cfg.NPMUpstream, a.Cfg.NPMStorageDir, a.Cfg.MetaTTL); err != nil {
		return err
	}
	log.Printf("[npm-adapter] npm 生态配置已按默认配置初始化（upstream=%s, storage=%s）",
		a.Cfg.NPMUpstream, a.Cfg.NPMStorageDir)
	return nil
}
