package main

import (
	"log"
	"sort"
	"time"
)

// 评论楼层视图的本地记忆。
//
// 打开一篇帖子的评论区时，记住用户上次的浏览状态，下次回到同一篇时沿用：
// 视图模式（楼层 / 扁平）、读到的位置（行号 + 那条评论的 ID）、离开时间
// （用来把之后新增的评论标成「新」），以及哪些楼中楼被折叠收起。
//
// 状态只记在本机（cache/offline.json 的 replyview:<scope>:<shortID>），服务端
// 没有这类接口，也不去改动服务端数据。作用域与阅读记录相同（见
// accountScope）：换账号后不会把上一个账号折叠 / 读到的位置套到自己的帖子上。
// 已登录但还没拿到昵称（sessionScope）时不落盘，避免被下个账号复用。

const replyViewPrefix = "replyview:"

// replyViewMemory 是一条记忆，全部字段按当前账号 + 帖子隔离。
type replyViewMemory struct {
	// Flat 是楼层视图（false）/ 扁平时间序（true）的偏好。
	Flat bool `json:"flat,omitempty"`
	// ReadToID 是上次离开时视口顶部那条评论的 ID；scroll_idx 是它落在显示
	// 列表里的行号。两者一起把评论区恢复到上次停的位置：优先按 ID 定位
	// （列表变动后行号会漂移），行号作为兜底。
	ReadToID string `json:"read_to_id,omitempty"`
	// ScrollIdx 是离开时视口顶部的显示行索引（已剔除折叠隐藏的行）。
	ScrollIdx int `json:"scroll_idx,omitempty"`
	// ReadAt 是上次离开的时间，也是「新评论」的基线：再次打开时，创建时间
	// 晚于它的评论被标成「新」，方便一眼看到上次读过之后多出来的回复。
	ReadAt time.Time `json:"read_at,omitempty"`
	// Collapsed 是被折叠收起的评论 ID（都是带楼中楼的父评论）。
	Collapsed []string `json:"collapsed,omitempty"`
	// SavedAt 是这条记忆写入的时间，也用来判断读到的缓存是否损坏。
	SavedAt time.Time `json:"saved_at"`
}

// replyViewKey 是某篇帖子评论记忆的存储键。作用域拿不到（未登录匿名、
// sessionScope 或没有 shortID）时返回空，调用方据此跳过读写。
func (a *app) replyViewKey(shortID string) string {
	scope := a.persistableScope()
	if scope == "" || shortID == "" {
		return ""
	}
	return replyViewPrefix + scope + ":" + shortID
}

// loadReplyView 读回一篇帖子上次存下的评论记忆。没有缓存或损坏时返回
// ok=false，调用方按默认行为（楼层视图、全部展开、不标新）处理。
func (a *app) loadReplyView(shortID string) (replyViewMemory, bool) {
	key := a.replyViewKey(shortID)
	if key == "" || a.kv == nil {
		return replyViewMemory{}, false
	}
	var mem replyViewMemory
	if _, ok := a.kv.get(key, &mem); !ok {
		return replyViewMemory{}, false
	}
	if mem.SavedAt.IsZero() {
		return replyViewMemory{}, false
	}
	return mem, true
}

// saveReplyView 把当前评论区的浏览状态落盘。所有状态都从 detailState 现成
// 的字段收集：视图模式、实时停留位置、折叠集合；离开时间取现在。只写本机，
// 不影响服务端。
func (a *app) saveReplyView(d *detailState) {
	if d == nil || d.shortID == "" || a.kv == nil {
		return
	}
	key := a.replyViewKey(d.shortID)
	if key == "" {
		return
	}
	now := time.Now()
	mem := replyViewMemory{
		Flat:      d.flat,
		ReadToID:  d.stayID,
		ScrollIdx: d.stayIdx,
		ReadAt:    now,
		SavedAt:   now,
	}
	if len(d.collapsed) > 0 {
		ids := make([]string, 0, len(d.collapsed))
		for id := range d.collapsed {
			if id != "" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		mem.Collapsed = ids
	}
	if err := a.kv.put(key, mem, now); err != nil {
		log.Printf("评论视图记忆写入失败：%v", err)
	}
}

// rebuildDisplay 根据折叠集合重新计算评论的显示列表 display，并填好
// hiddenCount（每个折叠父被收起的后代条数）。collapsed 为空时 display 直接
// 复用 replyList 的切片头，逐个元素一致，不改动任何索引，现有行为完全不变。
func (d *detailState) rebuildDisplay() {
	if len(d.collapsed) == 0 {
		d.display = d.replyList
		d.hiddenCount = nil
		return
	}
	type stackEntry struct {
		id    string
		depth int
	}
	stack := make([]stackEntry, 0, 8)
	disp := make([]replyItem, 0, len(d.replyList))
	hidden := make(map[string]int)
	for _, r := range d.replyList {
		for len(stack) > 0 && r.Depth <= stack[len(stack)-1].depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			hidden[stack[len(stack)-1].id]++
			continue
		}
		disp = append(disp, r)
		if d.collapsed[r.ID] && r.HasChildren {
			stack = append(stack, stackEntry{id: r.ID, depth: r.Depth})
		}
	}
	d.display = disp
	d.hiddenCount = hidden
}

// isNewReply 报告一条评论是否该标成「新」：它的创建时间晚于基线时间。基线
// 来自上一次离开时的记忆（readBase），用来把上次读过之后新增的回复突出显示。
// 时间解析失败时保守地返回 false，不误标。
func isNewReply(createdAt string, base time.Time) bool {
	if base.IsZero() || createdAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return t.After(base)
}
