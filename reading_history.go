package main

import (
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 阅读历史保存在本机：每打开一篇帖子记一条，重复打开只更新时间并提到
// 最前面。它不依赖网络，也不属于服务端接口；离线时列表照样可读，已缓存
// 过正文的帖子还能直接打开。
//
// 记录按当前账号隔离（见 accountScope）。未登录时记在 anonymous 下；换账号
// 后不会把上一个账号读过什么展示出来。

const readingHistoryLimit = 200

type readingRecord struct {
	ShortID   string      `json:"short_id"`
	Title     string      `json:"title,omitempty"`
	Author    string      `json:"author,omitempty"`
	Avatar    string      `json:"avatar,omitempty"`
	Badges    []badgeInfo `json:"badges,omitempty"`
	Node      string      `json:"node,omitempty"`
	URL       string      `json:"url,omitempty"`
	CommentID string      `json:"comment_id,omitempty"`
	OpenedAt  time.Time   `json:"opened_at"`
}

type readingHistoryData struct {
	Records []readingRecord `json:"records"`
}

// loadReadingHistory 确保内存里的阅读历史属于当前账号。作用域没变时
// 直接返回；新启动、登录 / 退出后从 cache/offline.json 读一次。
func (a *app) loadReadingHistory() {
	scope := a.accountScope()
	if a.readingLoaded && a.readingFor == scope {
		return
	}
	a.readingFor = scope
	a.readingLoaded = true
	a.reading = nil
	a.readingList.ScrollTo(0, ui.Start)

	key := a.readingCacheKey()
	if key == "" || a.kv == nil {
		return
	}
	var data readingHistoryData
	if _, ok := a.kv.get(key, &data); !ok {
		return
	}
	a.reading = normalizeReadingRecords(data.Records)
	a.prefetchReadingAvatars()
}

// normalizeReadingRecords 去掉没有 short_id 的坏记录、按打开时间从新到旧
// 排序，并只保留最近 readingHistoryLimit 条。
func normalizeReadingRecords(in []readingRecord) []readingRecord {
	out := make([]readingRecord, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, r := range in {
		if r.ShortID == "" || seen[r.ShortID] {
			continue
		}
		seen[r.ShortID] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OpenedAt.After(out[j].OpenedAt) })
	if len(out) > readingHistoryLimit {
		out = out[:readingHistoryLimit]
	}
	return out
}

func (a *app) saveReadingHistory() {
	key := a.readingCacheKey()
	if key == "" || a.kv == nil {
		return
	}
	if err := a.kv.put(key, readingHistoryData{Records: a.reading}, time.Now()); err != nil {
		log.Printf("阅读历史写入失败：%v", err)
	}
}

// recordReading 记下（或置顶）一篇刚打开的帖子。topic 是已经拿到的正文，
// 离线从缓存打开时也能带上标题、作者和节点；没有正文时用列表行里的字段。
func (a *app) recordReading(it Item, topic *topicDetail) {
	if it.ShortID == "" {
		return
	}
	a.loadReadingHistory()
	now := time.Now()
	rec := readingRecord{
		ShortID:   it.ShortID,
		Title:     it.Title,
		Author:    it.Author,
		Avatar:    it.AuthorAvatar,
		Badges:    it.AuthorBadges,
		Node:      nodeFromSubtitlePrefix(it.SubtitlePrefix),
		URL:       it.URL,
		CommentID: it.CommentID,
		OpenedAt:  now,
	}
	for i, old := range a.reading {
		if old.ShortID != it.ShortID {
			continue
		}
		// 从链接或缓存打开时列表字段可能不全：已有的展示信息不要被空值覆盖。
		if rec.Title == "" {
			rec.Title = old.Title
		}
		if rec.Author == "" {
			rec.Author = old.Author
		}
		if rec.Avatar == "" {
			rec.Avatar = old.Avatar
		}
		if len(rec.Badges) == 0 {
			rec.Badges = old.Badges
		}
		if rec.Node == "" {
			rec.Node = old.Node
		}
		if rec.URL == "" {
			rec.URL = old.URL
		}
		if rec.CommentID == "" {
			rec.CommentID = old.CommentID
		}
		a.reading = append(a.reading[:i], a.reading[i+1:]...)
		break
	}
	if topic != nil {
		rec = readingRecordFromTopic(rec, topic)
	}
	if rec.URL == "" {
		rec.URL = topicURL(rec.ShortID, "")
	}
	a.reading = append([]readingRecord{rec}, a.reading...)
	if len(a.reading) > readingHistoryLimit {
		a.reading = a.reading[:readingHistoryLimit]
	}
	a.saveReadingHistory()
	a.prefetchAvatars(rec.Avatar)
}

// updateReadingTopic 在正文加载成功后补全打开时还缺的标题、作者与节点。
func (a *app) updateReadingTopic(shortID string, topic *topicDetail) {
	if shortID == "" || topic == nil {
		return
	}
	for i := range a.reading {
		if a.reading[i].ShortID != shortID {
			continue
		}
		next := readingRecordFromTopic(a.reading[i], topic)
		if reflect.DeepEqual(next, a.reading[i]) {
			return
		}
		a.reading[i] = next
		a.saveReadingHistory()
		return
	}
}

func readingRecordFromTopic(rec readingRecord, topic *topicDetail) readingRecord {
	if topic.Title != "" {
		rec.Title = topic.Title
	}
	if topic.Author != "" && topic.Author != "匿名" {
		rec.Author = topic.Author
	}
	if topic.Avatar != "" {
		rec.Avatar = topic.Avatar
	}
	if len(topic.Badges) > 0 {
		rec.Badges = topic.Badges
	}
	if topic.NodeName != "" {
		rec.Node = topic.NodeName
	}
	if topic.URL != "" {
		rec.URL = topic.URL
	}
	return rec
}

// nodeFromSubtitlePrefix 从列表行的「节点 · 」前缀里取出节点名。
func nodeFromSubtitlePrefix(prefix string) string {
	s := strings.TrimSpace(prefix)
	s = strings.TrimSuffix(s, "·")
	return strings.TrimSpace(s)
}

// readingItem 把一条阅读记录还原成列表行。行点击仍走 openDetail，
// openDetail 会先载入缓存的正文，再在后台刷新。
func (a *app) readingItem(r readingRecord) Item {
	title := r.Title
	if title == "" {
		title = "帖子 " + r.ShortID
	}
	it := Item{
		Key:       r.ShortID,
		ShortID:   r.ShortID,
		Title:     title,
		Author:    r.Author,
		URL:       r.URL,
		CommentID: r.CommentID,
	}
	if it.URL == "" {
		it.URL = topicURL(r.ShortID, "")
	}
	it.AuthorAvatar = r.Avatar
	it.AuthorBadges = r.Badges
	when := readingTimeText(r.OpenedAt)
	parts := []string{}
	if r.Node != "" {
		it.SubtitlePrefix = r.Node + " · "
		parts = append(parts, r.Node)
	}
	if r.Author != "" {
		parts = append(parts, r.Author)
	}
	if when != "" {
		it.SubtitleSuffix = " · " + when
		parts = append(parts, when)
	}
	it.Subtitle = strings.Join(parts, " · ")
	if a.hasCachedDetail(r.ShortID) {
		it.Meta = "已缓存正文"
	}
	return it
}

func readingTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return relativeTime(t.Format(time.RFC3339))
}

func (a *app) prefetchReadingAvatars() {
	urls := make([]string, 0, len(a.reading))
	for _, r := range a.reading {
		urls = append(urls, r.Avatar)
	}
	a.prefetchAvatars(urls...)
}

// clearReadingHistory 清空当前账号的阅读记录，以及随之失效的正文缓存。
// 只清本机数据，不影响服务端的收藏 / 书签。
func (a *app) clearReadingHistory() {
	a.reading = nil
	a.readingLoaded = true
	if key := a.readingCacheKey(); key != "" && a.kv != nil {
		if err := a.kv.delete(key); err != nil {
			log.Printf("清空阅读历史失败：%v", err)
		}
	}
	if a.kv != nil {
		if _, err := a.kv.clearPrefixes(detailCachePrefix); err != nil {
			log.Printf("清空帖子正文缓存失败：%v", err)
		}
	}
	a.readingList.ScrollTo(0, ui.Start)
	a.toast(toastSettings, "已清空阅读记录与缓存的正文")
}
