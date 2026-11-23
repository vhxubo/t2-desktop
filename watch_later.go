package main

import (
	"log"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
)

// 稍后再看（Watch Later）与浏览历史是同一扇窗口里的两页，数据都只存在本机：
//
//   - 浏览历史就是 reading_history.go 里的阅读记录（每打开一篇记一条）。
//   - 稍后再看是本文件：用户主动标记「等会儿再读」的帖子，挂在侧边栏入口上，
//     不依赖服务端的收藏 / 书签。
//
// 两页都按当前账号隔离（见 accountScope）：换账号后看不到上一个账号读过、
// 攒过什么；未登录时记在 anonymous 下。清空也只清当前账号。
//
// 打开一篇稍后再看的帖子之后怎么处理，由 watch_later_prefs.go 的偏好决定：
// 默认「打开即移除」；打开「保留」后留在列表里并标成已读，已读与未读分开
// 显示（未读在前）。这样同一个功能既能当临时队列，也能当长期待读清单。
const watchLaterLimit = 200

type watchLaterRecord struct {
	ShortID   string      `json:"short_id"`
	Title     string      `json:"title,omitempty"`
	Author    string      `json:"author,omitempty"`
	Avatar    string      `json:"avatar,omitempty"`
	Badges    []badgeInfo `json:"badges,omitempty"`
	IsPro     bool        `json:"is_pro,omitempty"`
	Node      string      `json:"node,omitempty"`
	URL       string      `json:"url,omitempty"`
	CommentID string      `json:"comment_id,omitempty"`
	AddedAt   time.Time   `json:"added_at"`
	// ReadAt 是保留模式下「打开过」的时间。零值表示未读；默认模式（打开即
	// 移除）下这一项没有意义，记录会直接消失。
	ReadAt time.Time `json:"read_at,omitempty"`
}

type watchLaterData struct {
	Records []watchLaterRecord `json:"records"`
}

// loadWatchLater 确保内存里的稍后再看属于当前账号。作用域没变时直接返回；
// 新启动、登录 / 退出后从 cache/offline.json 读一次。
func (a *app) loadWatchLater() {
	scope := a.accountScope()
	if a.laterLoaded && a.laterFor == scope {
		return
	}
	a.laterFor = scope
	a.laterLoaded = true
	a.later = nil
	a.laterList.ScrollTo(0, ui.Start)

	key := a.watchLaterCacheKey()
	if key == "" || a.kv == nil {
		return
	}
	var data watchLaterData
	if _, ok := a.kv.get(key, &data); !ok {
		return
	}
	a.later = normalizeWatchLaterRecords(data.Records)
	a.prefetchWatchLaterAvatars()
}

// normalizeWatchLaterRecords 去掉没有 short_id 的坏记录、去重、按「未读在前，
// 组内加入时间从新到旧」排序，并只保留最近 watchLaterLimit 条。
func normalizeWatchLaterRecords(in []watchLaterRecord) []watchLaterRecord {
	out := make([]watchLaterRecord, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, r := range in {
		if r.ShortID == "" || seen[r.ShortID] {
			continue
		}
		seen[r.ShortID] = true
		out = append(out, r)
	}
	sortWatchLaterRecords(out)
	if len(out) > watchLaterLimit {
		out = out[:watchLaterLimit]
	}
	return out
}

// sortWatchLaterRecords 把未读排在已读前面，各自按加入时间从新到旧。默认
// 模式下所有记录的 ReadAt 都是零值，等价于纯按加入时间倒序。
func sortWatchLaterRecords(recs []watchLaterRecord) {
	sort.SliceStable(recs, func(i, j int) bool {
		unreadI, unreadJ := recs[i].ReadAt.IsZero(), recs[j].ReadAt.IsZero()
		if unreadI != unreadJ {
			return unreadI
		}
		return recs[i].AddedAt.After(recs[j].AddedAt)
	})
}

func (a *app) saveWatchLater() {
	key := a.watchLaterCacheKey()
	if key == "" || a.kv == nil {
		return
	}
	if err := a.kv.put(key, watchLaterData{Records: a.later}, time.Now()); err != nil {
		log.Printf("稍后再看写入失败：%v", err)
	}
}

// inWatchLater 报告这篇帖子此刻是否在稍后再看里。
func (a *app) inWatchLater(shortID string) bool {
	if shortID == "" {
		return false
	}
	a.loadWatchLater()
	for _, r := range a.later {
		if r.ShortID == shortID {
			return true
		}
	}
	return false
}

// watchLaterCount 是侧边栏角标上的数字：保留模式下只数未读，默认模式下整份
// 列表都是待读，直接数条目。
func (a *app) watchLaterCount() int {
	a.loadWatchLater()
	if !a.watchLaterPrefs.keepAfterOpen() {
		return len(a.later)
	}
	n := 0
	for _, r := range a.later {
		if r.ReadAt.IsZero() {
			n++
		}
	}
	return n
}

// watchLaterRecordFor 把列表行或正文整理成一条待读记录。topic 可能还没加载
// （详情页刚打开就点了「稍后再看」），这时只记列表行里已有的字段，正文回来
// 后再由 updateWatchLaterTopic 补全。
func watchLaterRecordFor(it Item, topic *topicDetail) watchLaterRecord {
	rec := watchLaterRecord{
		ShortID:   it.ShortID,
		Title:     it.Title,
		Author:    it.Author,
		Avatar:    it.AuthorAvatar,
		Badges:    it.AuthorBadges,
		IsPro:     it.AuthorIsPro,
		Node:      nodeFromSubtitlePrefix(it.SubtitlePrefix),
		URL:       it.URL,
		CommentID: it.CommentID,
		AddedAt:   time.Now(),
	}
	if topic != nil {
		rec = watchLaterRecordFromTopic(rec, topic)
	}
	if rec.URL == "" {
		rec.URL = topicURL(rec.ShortID, "")
	}
	return rec
}

func watchLaterRecordFromTopic(rec watchLaterRecord, topic *topicDetail) watchLaterRecord {
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
	if topic.AuthorIsPro {
		rec.IsPro = true
	}
	if topic.NodeName != "" {
		rec.Node = topic.NodeName
	}
	if topic.URL != "" {
		rec.URL = topic.URL
	}
	return rec
}

// addWatchLater 把一篇帖子放进稍后再看并置顶。已经在列表里时只把它提到最
// 前面，不重复记一条；重新加入视为未读（清掉已读时间）。
func (a *app) addWatchLater(it Item, topic *topicDetail) {
	if it.ShortID == "" {
		return
	}
	a.loadWatchLater()
	rec := watchLaterRecordFor(it, topic)
	for i, old := range a.later {
		if old.ShortID != it.ShortID {
			continue
		}
		// 从链接或缓存加入时展示字段可能不全：已有的信息不要被空值覆盖。
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
		if !rec.IsPro {
			rec.IsPro = old.IsPro
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
		a.later = append(a.later[:i], a.later[i+1:]...)
		break
	}
	a.later = append([]watchLaterRecord{rec}, a.later...)
	if len(a.later) > watchLaterLimit {
		a.later = a.later[:watchLaterLimit]
	}
	a.saveWatchLater()
	a.prefetchAvatars(rec.Avatar)
	a.redrawWatchLaterPage()
}

// removeWatchLater 把一条移出稍后再看。不在列表里时是空操作。
func (a *app) removeWatchLater(shortID string) {
	if shortID == "" {
		return
	}
	a.loadWatchLater()
	for i, r := range a.later {
		if r.ShortID != shortID {
			continue
		}
		a.later = append(a.later[:i], a.later[i+1:]...)
		a.saveWatchLater()
		a.redrawWatchLaterPage()
		return
	}
}

// toggleWatchLater 是详情页那枚图标的动作：不在列表里就加入，已经在列表里
// 就移出。返回操作之后这篇是否还在稍后再看里。
func (a *app) toggleWatchLater(it Item, topic *topicDetail) bool {
	if a.inWatchLater(it.ShortID) {
		a.removeWatchLater(it.ShortID)
		return false
	}
	a.addWatchLater(it, topic)
	return true
}

// handleWatchLaterOpen 在打开一篇帖子的正文时调用。默认偏好是「打开即移除」，
// 这一项从列表里消失；开了「保留」则留在原处并标成已读，未读与已读分开显示。
func (a *app) handleWatchLaterOpen(shortID string) {
	if shortID == "" {
		return
	}
	a.loadWatchLater()
	idx := -1
	for i, r := range a.later {
		if r.ShortID == shortID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	if !a.watchLaterPrefs.keepAfterOpen() {
		a.later = append(a.later[:idx], a.later[idx+1:]...)
		a.saveWatchLater()
		a.redrawWatchLaterPage()
		return
	}
	if !a.later[idx].ReadAt.IsZero() {
		return
	}
	a.later[idx].ReadAt = time.Now()
	sortWatchLaterRecords(a.later)
	a.saveWatchLater()
	a.redrawWatchLaterPage()
}

// updateWatchLaterTopic 在正文加载成功后补全加入时还缺的标题、作者与节点。
func (a *app) updateWatchLaterTopic(shortID string, topic *topicDetail) {
	if shortID == "" || topic == nil {
		return
	}
	a.loadWatchLater()
	for i := range a.later {
		if a.later[i].ShortID != shortID {
			continue
		}
		next := watchLaterRecordFromTopic(a.later[i], topic)
		if next.Title == a.later[i].Title && next.Author == a.later[i].Author &&
			next.Avatar == a.later[i].Avatar && next.Node == a.later[i].Node &&
			next.URL == a.later[i].URL {
			return
		}
		a.later[i] = next
		a.saveWatchLater()
		a.redrawWatchLaterPage()
		return
	}
}

// watchLaterItem 把一条待读记录还原成打开详情用的列表行。
func (a *app) watchLaterItem(r watchLaterRecord) Item {
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
	it.AuthorIsPro = r.IsPro
	if r.Node != "" {
		it.SubtitlePrefix = r.Node + " · "
	}
	return it
}

func (a *app) prefetchWatchLaterAvatars() {
	urls := make([]string, 0, len(a.later))
	for _, r := range a.later {
		urls = append(urls, r.Avatar)
	}
	a.prefetchAvatars(urls...)
}

// clearWatchLaterReadMarks 把保留模式留下的已读标记全部清掉，并重新按加入
// 时间排序。关掉「打开后保留」时调用：默认模式没有已读概念，留着旧标记会
// 让下次打开开关时冒出上一次的状态。
func (a *app) clearWatchLaterReadMarks() {
	a.loadWatchLater()
	changed := false
	for i := range a.later {
		if !a.later[i].ReadAt.IsZero() {
			a.later[i].ReadAt = time.Time{}
			changed = true
		}
	}
	if !changed {
		return
	}
	sortWatchLaterRecords(a.later)
	a.saveWatchLater()
	a.redrawWatchLaterPage()
}

// clearWatchLater 清空当前账号的稍后再看，只清本机数据。
func (a *app) clearWatchLater() {
	a.later = nil
	a.laterLoaded = true
	if key := a.watchLaterCacheKey(); key != "" && a.kv != nil {
		if err := a.kv.delete(key); err != nil {
			log.Printf("清空稍后再看失败：%v", err)
		}
	}
	a.laterList.ScrollTo(0, ui.Start)
	a.redrawWatchLaterPage()
	a.toast(toastMain, "已清空稍后再看")
}

// watchLaterTimeText 是行尾「加入时间」的展示文本，和阅读记录共用同一套
// 相对时间写法。
func watchLaterTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return relativeTime(t.Format(time.RFC3339))
}
