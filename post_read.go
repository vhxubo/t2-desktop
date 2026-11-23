package main

import (
	"log"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 帖子与评论的本地已读状态。
//
// 点开一篇帖子或一条评论就算读过：列表里把读过的那一行置灰，读过的不再和
// 新内容抢注意力。状态只记在本机（cache/offline.json 的 read:<scope>），
// 服务端没有已读接口，也不去改动服务端数据。作用域与阅读记录相同（见
// accountScope）：换账号后不会把上一个账号读过的东西置灰。
//
// 只保留最近 postReadLimit 条：记录本身很小，但没有上限会一直增长；超过
// 上限时丢掉最早读过的那些，它们最多重新显示为未读。

const postReadLimit = 2000

// postReadRecord 存一条已读记录。评论记录也放在这里，ShortID 字段承载的
// 是评论 id；落盘字段名保持 short_id 不变，兼容旧缓存。
type postReadRecord struct {
	ShortID string    `json:"short_id"`
	ReadAt  time.Time `json:"read_at"`
}

type postReadData struct {
	Records  []postReadRecord `json:"records"`
	Comments []postReadRecord `json:"comments,omitempty"`
}

// loadPostReads 确保内存里的已读表属于当前账号。作用域没变时直接返回；
// 新启动、登录 / 退出后从 cache/offline.json 读一次。
func (a *app) loadPostReads() {
	scope := a.accountScope()
	if a.readLoaded && a.readFor == scope {
		return
	}
	a.readFor = scope
	a.readLoaded = true
	a.readAt = nil
	a.commentReadAt = nil
	key := a.postReadKey()
	if key == "" || a.kv == nil {
		return
	}
	var data postReadData
	if _, ok := a.kv.get(key, &data); !ok {
		return
	}
	a.readAt = readRecordsToMap(data.Records)
	a.commentReadAt = readRecordsToMap(data.Comments)
	a.trimPostReads()
	a.trimCommentReads()
}

// readRecordsToMap 把落盘记录收进 id → 时间的表；同一个 id 写了多次时取
// 更晚的时间，裁剪时不会先丢掉最近读过的那条。老缓存没有 comments 字段，
// 解出来是空表，不影响已读帖子。
func readRecordsToMap(records []postReadRecord) map[string]time.Time {
	m := make(map[string]time.Time, len(records))
	for _, r := range records {
		if r.ShortID == "" {
			continue
		}
		if old, ok := m[r.ShortID]; !ok || r.ReadAt.After(old) {
			m[r.ShortID] = r.ReadAt
		}
	}
	return m
}

// isPostRead 报告这篇帖子在当前账号下是否已经读过。没有 short_id 的行
// （合集等）一律按未读处理。
func (a *app) isPostRead(shortID string) bool {
	if shortID == "" {
		return false
	}
	a.loadPostReads()
	_, ok := a.readAt[shortID]
	return ok
}

// isCommentRead 报告这条评论在当前账号下是否已经点开过。没有评论 id 的
// 行一律按未读处理。
func (a *app) isCommentRead(id string) bool {
	if id == "" {
		return false
	}
	a.loadPostReads()
	_, ok := a.commentReadAt[id]
	return ok
}

// markPostRead 记下一篇刚打开的帖子。已经读过的不重复写盘：已读是单向
// 状态，重复打开不需要更新记录。
func (a *app) markPostRead(shortID string) {
	if shortID == "" {
		return
	}
	a.loadPostReads()
	if _, ok := a.readAt[shortID]; ok {
		return
	}
	if a.readAt == nil {
		a.readAt = make(map[string]time.Time)
	}
	a.readAt[shortID] = time.Now()
	a.trimPostReads()
	a.saveReads()
	a.redrawReadLists()
}

// markCommentRead 记下一条刚点开过的评论。和帖子一样是单向状态，重复打开
// 不写盘。
func (a *app) markCommentRead(id string) {
	if id == "" {
		return
	}
	a.loadPostReads()
	if _, ok := a.commentReadAt[id]; ok {
		return
	}
	if a.commentReadAt == nil {
		a.commentReadAt = make(map[string]time.Time)
	}
	a.commentReadAt[id] = time.Now()
	a.trimCommentReads()
	a.saveReads()
	a.redrawReadLists()
}

// saveReads 把帖子与评论的已读表一起落盘：两者共用 read:<scope> 这个键。
func (a *app) saveReads() {
	key := a.postReadKey()
	if key == "" || a.kv == nil {
		return
	}
	data := postReadData{
		Records:  postReadRecords(a.readAt, postReadLimit),
		Comments: postReadRecords(a.commentReadAt, postReadLimit),
	}
	if err := a.kv.put(key, data, time.Now()); err != nil {
		log.Printf("已读状态写入失败：%v", err)
	}
}

// trimPostReads / trimCommentReads 在内存里也只保留最近 postReadLimit 条，
// 读盘损坏写了超量记录时内存占用同样有上限。
func (a *app) trimPostReads() {
	a.readAt = trimReadMap(a.readAt)
}

func (a *app) trimCommentReads() {
	a.commentReadAt = trimReadMap(a.commentReadAt)
}

func trimReadMap(reads map[string]time.Time) map[string]time.Time {
	if len(reads) <= postReadLimit {
		return reads
	}
	kept := make(map[string]time.Time, postReadLimit)
	for _, r := range postReadRecords(reads, postReadLimit) {
		kept[r.ShortID] = r.ReadAt
	}
	return kept
}

// postReadRecords 把已读表整理成可落盘的记录：新读的在前，最多 keep 条。
// 时间相同时按 id 排序，保证同一次落盘的顺序稳定。
func postReadRecords(reads map[string]time.Time, keep int) []postReadRecord {
	records := make([]postReadRecord, 0, len(reads))
	for id, at := range reads {
		records = append(records, postReadRecord{ShortID: id, ReadAt: at})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].ReadAt.Equal(records[j].ReadAt) {
			return records[i].ShortID < records[j].ShortID
		}
		return records[i].ReadAt.After(records[j].ReadAt)
	})
	if keep >= 0 && len(records) > keep {
		records = records[:keep]
	}
	return records
}

// redrawReadLists 让还开着的内容列表窗口按最新已读状态重建。窗口没开时
// 是空操作。只在主线程调用。
func (a *app) redrawReadLists() {
	a.redrawProfileWindow()
	a.redrawSearchWindow()
	a.redrawUserCardWindow()
}

// postReadInk 返回列表文字在已读 / 未读时用的颜色。已读退到四级文字色，
// 和未读的正文色拉开明显的差距；卡片底色与交互都和未读时一样。
// 帖子的标题与正文摘要、点开过的评论正文都用它。
func postReadInk(tk *lobetheme.Tokens, read bool) ui.Color {
	if read {
		return tk.TextQuaternary
	}
	return tk.Text
}
