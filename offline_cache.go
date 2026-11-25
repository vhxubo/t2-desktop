package main

import "time"

// 离线缓存的分组。本机只有一个会话，缓存固定落在一个作用域下，不需要按
// 身份再分层；金币缓存仍要求已登录才落盘。
const (
	coinCachePrefix    = "coin:"
	readingCachePrefix = "reading:"
	laterCachePrefix   = "later:"
	readCachePrefix    = "read:"
	detailCachePrefix  = "detail:"

	localScope = "local"
)

// cacheScope 返回本机缓存的作用域。
func (a *app) cacheScope() string { return localScope }

// persistableScope 返回可以写入磁盘的作用域。
func (a *app) persistableScope() string { return localScope }

func (a *app) coinCacheKey() string {
	if a.auth == nil || !a.auth.loggedIn() {
		return ""
	}
	return coinCachePrefix + a.persistableScope()
}

func (a *app) readingCacheKey() string {
	return readingCachePrefix + a.persistableScope()
}

// postReadKey 是帖子已读状态的存储键。
func (a *app) postReadKey() string { return readCachePrefix + a.persistableScope() }

// watchLaterCacheKey 是稍后再看的存储键。
func (a *app) watchLaterCacheKey() string { return laterCachePrefix + a.persistableScope() }

// detailScopePrefix 是本机详情缓存的键前缀。
func detailScopePrefix(scope string) string {
	return detailCachePrefix + scope + ":"
}

// detailCacheKey 是某篇帖子在本机缓存里的键。
func (a *app) detailCacheKey(shortID string) string {
	if shortID == "" {
		return ""
	}
	return detailScopePrefix(a.cacheScope()) + shortID
}

// formatCacheTime 是缓存时间在界面上的统一写法。秒没有意义，统一到分钟。
func formatCacheTime(t time.Time) string {
	if t.IsZero() {
		return "未知时间"
	}
	return t.Local().Format("2006-01-02 15:04")
}
