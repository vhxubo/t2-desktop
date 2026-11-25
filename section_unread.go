package main

import (
	"time"
)

// 分区未读角标：通知未读数接口的 home / community 分项以「上次读取时间」为基线
// 计算（参数 home_last_read_at / community_last_read_at），所以基线记在本机
// offline.json，按账号作用域区分，打开对应分区时推进，角标随之清零。
// 关注流「动态」有自己的专用端点与基线（见 follows.go），这里不重复拉。
const (
	sectionHome       = "home"
	sectionCommunity  = "community"
	sectionReadPrefix = "section:read:"
)

// sectionReadRecord 是本机记下的「上次读某个分区的时间」。
type sectionReadRecord struct {
	At time.Time `json:"at"`
}

func (a *app) sectionReadKey(section string) string {
	scope := a.cacheScope()
	if scope == "" {
		return ""
	}
	return sectionReadPrefix + section + ":" + scope
}

func (a *app) sectionLastRead(section string) time.Time {
	key := a.sectionReadKey(section)
	if key == "" || a.kv == nil {
		return time.Time{}
	}
	var rec sectionReadRecord
	if _, ok := a.kv.get(key, &rec); !ok {
		return time.Time{}
	}
	return rec.At
}

// sectionReadAtParam 转成接口要的 ISO 字符串；没有基线返回空串，调用方据此
// 不带该参数，让服务端用自己的口径。
func (a *app) sectionReadAtParam(section string) string {
	at := a.sectionLastRead(section)
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339)
}

// markSectionRead 在打开对应分区时推进读取基线并清掉角标。
func (a *app) markSectionRead(section string) {
	switch section {
	case sectionHome:
		a.sectionUnread.Home = 0
	case sectionCommunity:
		a.sectionUnread.Community = 0
	}
	key := a.sectionReadKey(section)
	if key == "" || a.kv == nil {
		return
	}
	now := time.Now().UTC()
	if err := a.kv.put(key, sectionReadRecord{At: now}, now); err != nil {
		return
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// markTabRead 把一次浏览页的打开转成对应的已读动作。
func (a *app) markTabRead(idx int) {
	if idx < 0 || idx >= len(tabSpecs) {
		return
	}
	s := tabSpecs[idx]
	switch {
	case s.Path == "/v1/me/follows/timeline":
		a.markFollowsRead()
	case s.FilterByNode:
		a.markSectionRead(sectionHome)
	case s.IsCommunity:
		a.markSectionRead(sectionCommunity)
	}
}

// tabUnreadCount 是侧边栏某个浏览页的红点计数。
func (a *app) tabUnreadCount(idx int) int {
	if idx < 0 || idx >= len(tabSpecs) {
		return 0
	}
	if idx == followsTimelineTab() {
		return a.followUnread
	}
	s := tabSpecs[idx]
	switch {
	case s.FilterByNode:
		return a.sectionUnread.Home
	case s.IsCommunity:
		return a.sectionUnread.Community
	}
	return 0
}
