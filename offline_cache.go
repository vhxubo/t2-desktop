package main

import "time"

// 离线缓存的分组。本机只有一份缓存，键固定带 local 段；沿用旧键名，
// 升级后能直接读到既有数据。金币缓存仍要求已登录才落盘。
const (
	coinCachePrefix    = "coin:"
	readingCachePrefix = "reading:"
	laterCachePrefix   = "later:"
	readCachePrefix    = "read:"
	detailCachePrefix  = "detail:local:"

	localCacheSegment = "local"
)

func (a *app) coinCacheKey() string {
	if a.auth == nil || !a.auth.loggedIn() {
		return ""
	}
	return coinCachePrefix + localCacheSegment
}

func (a *app) readingCacheKey() string {
	return readingCachePrefix + localCacheSegment
}

// postReadKey 是帖子已读状态的存储键。
func (a *app) postReadKey() string { return readCachePrefix + localCacheSegment }

// watchLaterCacheKey 是稍后再看的存储键。
func (a *app) watchLaterCacheKey() string { return laterCachePrefix + localCacheSegment }

// detailCacheKey 是某篇帖子在本机缓存里的键。
func (a *app) detailCacheKey(shortID string) string {
	if shortID == "" {
		return ""
	}
	return detailCachePrefix + shortID
}

// formatCacheTime 是缓存时间在界面上的统一写法。秒没有意义，统一到分钟。
func formatCacheTime(t time.Time) string {
	if t.IsZero() {
		return "未知时间"
	}
	return t.Local().Format("2006-01-02 15:04")
}
