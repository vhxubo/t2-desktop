package main

import (
	"log"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
)

// 帖子的本地已读状态。
//
// 点开一篇帖子就算读过：列表里把读过的帖子标题置灰，读过的不再和新内容
// 抢注意力。状态只记在本机（cache/offline.json 的 read:<scope>），服务端
// 没有已读接口，也不去改动服务端数据。作用域与阅读记录相同（见
// accountScope）：换账号后不会把上一个账号读过的帖子置灰。
//
// 只保留最近 postReadLimit 篇：记录本身很小，但没有上限会一直增长；超过
// 上限时丢掉最早读过的那些，它们最多重新显示为未读。

const postReadLimit = 2000

type postReadRecord struct {
	ShortID string    `json:"short_id"`
	ReadAt  time.Time `json:"read_at"`
}

type postReadData struct {
	Records []postReadRecord `json:"records"`
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
	key := a.postReadKey()
	if key == "" || a.kv == nil {
		return
	}
	var data postReadData
	if _, ok := a.kv.get(key, &data); !ok {
		return
	}
	reads := make(map[string]time.Time, len(data.Records))
	for _, r := range data.Records {
		if r.ShortID == "" {
			continue
		}
		// 同一个 short_id 写了多次时以更晚的时间为准，裁剪时不会先丢掉
		// 最近读过的那条。
		if old, ok := reads[r.ShortID]; !ok || r.ReadAt.After(old) {
			reads[r.ShortID] = r.ReadAt
		}
	}
	a.readAt = reads
	a.trimPostReads()
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
	a.savePostReads()
	// 主窗口已经切到正文，其它还开着的列表窗口（「我的」、搜索、资料卡）
	// 仍停在原来的列表上，让它们按新的已读状态重建。
	a.redrawReadLists()
}

func (a *app) savePostReads() {
	key := a.postReadKey()
	if key == "" || a.kv == nil {
		return
	}
	data := postReadData{Records: postReadRecords(a.readAt, postReadLimit)}
	if err := a.kv.put(key, data, time.Now()); err != nil {
		log.Printf("帖子已读状态写入失败：%v", err)
	}
}

// trimPostReads 在内存里也只保留最近 postReadLimit 条，读盘损坏写了超量
// 记录时内存占用同样有上限。
func (a *app) trimPostReads() {
	if len(a.readAt) <= postReadLimit {
		return
	}
	reads := make(map[string]time.Time, postReadLimit)
	for _, r := range postReadRecords(a.readAt, postReadLimit) {
		reads[r.ShortID] = r.ReadAt
	}
	a.readAt = reads
}

// postReadRecords 把已读表整理成可落盘的记录：新读的在前，最多 keep 条。
// 时间相同时按 short_id 排序，保证同一次落盘的顺序稳定。
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

// postTitleColor 返回帖子标题在已读 / 未读时用的颜色。已读只把标题退到
// 次要文字色，卡片底色、正文摘要与交互都和未读时一样。
func postTitleColor(t *ui.Theme, read bool) ui.Color {
	if read {
		return t.TextMuted
	}
	return t.Text
}
