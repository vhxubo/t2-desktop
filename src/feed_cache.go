package main

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

// 信息流首屏缓存：每个列表 tab 的第一次分页结果落进 cache/feed.json。
// 断网启动时先摆上这一份，网络回来后由静默刷新覆盖。只存第 1 页：续拉页
// 不进缓存，避免单个 tab 的缓存随着翻页无限增长。
//
// 键里带 scope：需要登录的列表（动态）用账号名，公开列表用 local；公开内容
// 不跨账号共享，也为将来多账号留了切换点。
const (
	feedCacheMaxTabs  = 64
	feedCacheMaxBytes = 256 << 10
)

type cachedFeed struct {
	Items      []Item    `json:"items"`
	Total      int       `json:"total"`
	TotalPages int       `json:"total_pages"`
	SavedAt    time.Time `json:"saved_at"`
}

// feedCacheKey 是某个列表首屏的缓存键。通知不进这份缓存：它有独立的加载
// 与静默合并链路。首页的节点筛选已经写进 Query，同一 tab 的不同筛选各自成键。
func (a *app) feedCacheKey(spec tabSpec) string {
	if spec.Path == "" || spec.Kind == kindNotification {
		return ""
	}
	scope := localCacheSegment
	if spec.NeedsAuth && a.auth != nil && a.auth.loggedIn() {
		if name := a.auth.username(); name != "" {
			scope = strings.ToLower(name)
		}
	}
	return feedCachePrefix + scope + ":" + spec.Path + "?" + spec.Query
}

func (a *app) readCachedFeed(spec tabSpec) (cachedFeed, bool) {
	if a.kv == nil {
		return cachedFeed{}, false
	}
	key := a.feedCacheKey(spec)
	if key == "" {
		return cachedFeed{}, false
	}
	var c cachedFeed
	if _, ok := a.kv.get(key, &c); !ok {
		return cachedFeed{}, false
	}
	if len(c.Items) == 0 || c.SavedAt.IsZero() {
		return cachedFeed{}, false
	}
	return c, true
}

// preloadFeedCache 在首屏请求发出前把上次的列表摆上来。它不设置 loaded：
// 那会让 ensureLoaded 以为这一页已经加载完成而不再发请求。
func (a *app) preloadFeedCache(st *tabState, spec tabSpec) {
	if st == nil || st.loaded || len(st.raw) > 0 || len(st.items) > 0 {
		return
	}
	c, ok := a.readCachedFeed(spec)
	if !ok {
		return
	}
	st.raw = append([]Item(nil), c.Items...)
	st.items, st.hidden = a.filterItems(spec, st.raw)
	st.refold(spec)
	st.total = c.Total
	st.totalPages = c.TotalPages
	st.fromCache = true
	st.cachedAt = c.SavedAt
	a.prefetchItemAvatars(st.items)
}

// saveCachedFeed 把首屏写回缓存。没有主窗口时列表不可见，也没有可阻塞的
// UI 线程：不写。这样后台回包不会在测试目录清理之后才落盘。
//
// 有窗口时序列化与 put 都放到后台协程：loadSpec / mergeFreshPage 的回调在
// UI 线程执行，同步 put 会把整份 JSON 的序列化与写盘压在界面上。
func (a *app) saveCachedFeed(spec tabSpec, items []Item, total, totalPages int) {
	if a.win == nil || a.kv == nil || len(items) == 0 {
		return
	}
	itemsCopy := append([]Item(nil), items...)
	at := time.Now()
	go func() {
		a.writeFeedCache(spec, itemsCopy, total, totalPages, at)
	}()
}

// writeFeedCache 同步写一份首屏缓存，返回是否写入。saveCachedFeed 把它放到
// 后台；测试与无窗口场景可以直接调它做确定性断言。
func (a *app) writeFeedCache(spec tabSpec, items []Item, total, totalPages int, at time.Time) bool {
	if a.kv == nil || len(items) == 0 {
		return false
	}
	key := a.feedCacheKey(spec)
	if key == "" {
		return false
	}
	c := cachedFeed{Items: items, Total: total, TotalPages: totalPages, SavedAt: at}
	raw, err := json.Marshal(c)
	if err != nil {
		log.Printf("信息流缓存序列化失败：%v", err)
		return false
	}
	if len(raw) > feedCacheMaxBytes {
		log.Printf("信息流缓存超过单键上限，跳过写入：%s（%d 字节）", key, len(raw))
		return false
	}
	if err := a.kv.put(key, c, at); err != nil {
		logCacheWriteError(err)
		return false
	}
	a.kv.prune(feedCachePrefix, feedCacheMaxTabs)
	return true
}

// feedOfflineError 判断一次首屏失败是不是「网络不可用」。errOffline 是离线
// 期间 transport 拦下的请求，比普通网络错误更明确。
func feedOfflineError(err error) bool {
	return err != nil && (errors.Is(err, errOffline) || isNetworkError(err))
}

// markFeedOffline 在首屏刷新失败、但列表来自缓存时调用：保留内容，在页头
// 标明这是本地缓存，而不是把列表换成错误页。
func (a *app) markFeedOffline(st *tabState) {
	if st == nil {
		return
	}
	if st.cachedAt.IsZero() {
		st.cacheNote = "网络不可用，显示上次加载的内容"
		return
	}
	st.cacheNote = "网络不可用，显示 " + formatCacheTime(st.cachedAt) + " 的本地缓存"
}

// clearFeedCacheState 清掉列表的缓存标记：首屏真的刷新成功，或用户主动
// 重试走错误页时调用。
func (a *app) clearFeedCacheState(st *tabState) {
	if st == nil {
		return
	}
	st.cacheNote = ""
	st.cachedAt = time.Time{}
	st.fromCache = false
}