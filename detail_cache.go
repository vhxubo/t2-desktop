package main

import (
	"log"
	"time"
)

// 帖子正文与第一页评论的本地缓存。
//
// 打开过的帖子会把正文和第一页评论写进 cache/offline.json；下次从阅读
// 历史或列表再打开时先显示缓存，网络请求回来后再覆盖。弱网 / 无网时
// 正文立刻可读，不再先给用户一页"加载失败"。
//
// 缓存按 short_id 全局共享（帖子是公开内容），但只保留最近打开的若干篇；
// 清空阅读历史时会一并删除，避免留下没有人再引用的正文。

const detailCacheMax = 60

type cachedDetail struct {
	Topic           topicDetail `json:"topic"`
	Replies         []replyItem `json:"replies,omitempty"`
	ReplyLoaded     bool        `json:"reply_loaded,omitempty"`
	ReplyPage       int         `json:"reply_page,omitempty"`
	ReplyTotal      int         `json:"reply_total,omitempty"`
	ReplyTotalPages int         `json:"reply_total_pages,omitempty"`
	ReplyNoMore     bool        `json:"reply_no_more,omitempty"`
	SavedAt         time.Time   `json:"saved_at"`
}

func (a *app) readCachedDetail(shortID string) (cachedDetail, bool) {
	if a.kv == nil || shortID == "" {
		return cachedDetail{}, false
	}
	var c cachedDetail
	if _, ok := a.kv.get(detailCachePrefix+shortID, &c); !ok {
		return cachedDetail{}, false
	}
	if c.Topic.ShortID == "" || c.SavedAt.IsZero() {
		return cachedDetail{}, false
	}
	return c, true
}

func (a *app) hasCachedDetail(shortID string) bool {
	if a.kv == nil || shortID == "" {
		return false
	}
	return a.kv.has(detailCachePrefix + shortID)
}

// preloadDetailCache 在新建 detailState 后调用：有缓存就先把正文和第一页
// 评论摆上去。它不改变 loading 标记，后续网络请求照常进行。
//
// 这里不写「正在刷新」之类的提示：缓存正文与网络刷新几乎同时到，先加一行
// 再把它拿掉会让正文整块上下跳一下。只有刷新真的失败时，markDetailOffline
// 才在顶部说明当前是本地内容。
func (a *app) preloadDetailCache(d *detailState) {
	if d == nil {
		return
	}
	c, ok := a.readCachedDetail(d.shortID)
	if !ok {
		return
	}
	topic := c.Topic
	d.topic = &topic
	d.replyList = c.Replies
	d.replyLoaded = c.ReplyLoaded || len(c.Replies) > 0
	d.replyPage = c.ReplyPage
	if d.replyPage < 1 {
		d.replyPage = 1
	}
	d.replyTotal = c.ReplyTotal
	d.replyTotalPages = c.ReplyTotalPages
	d.replyNoMore = c.ReplyNoMore
	d.cachedAt = c.SavedAt
	d.fromCache = true
	a.prefetchItemAvatars([]Item{{
		Author:       topic.Author,
		AuthorAvatar: topic.Avatar,
	}})
	a.prefetchReplyAvatars(c.Replies)
}

// saveCachedDetail 写回当前正文与第一页评论。只有拿到正文时才写；续拉到
// 第 2 页以后不把整段多页评论塞进缓存，保留原来缓存的第一页。
func (a *app) saveCachedDetail(d *detailState) {
	if a.kv == nil || d == nil || d.topic == nil || d.shortID == "" {
		return
	}
	key := detailCachePrefix + d.shortID
	now := time.Now()
	c := cachedDetail{Topic: *d.topic, SavedAt: now}
	if d.replyPage <= 1 {
		c.Replies = d.replyList
		c.ReplyLoaded = d.replyLoaded
		c.ReplyPage = d.replyPage
		if c.ReplyPage < 1 {
			c.ReplyPage = 1
		}
		c.ReplyTotal = d.replyTotal
		c.ReplyTotalPages = d.replyTotalPages
		c.ReplyNoMore = d.replyNoMore
	} else if old, ok := a.readCachedDetail(d.shortID); ok {
		// 用户已经翻到后面的页：沿用旧缓存里的第一页，别把 90 条评论写回去。
		c.Replies = old.Replies
		c.ReplyLoaded = old.ReplyLoaded
		c.ReplyPage = old.ReplyPage
		c.ReplyTotal = old.ReplyTotal
		c.ReplyTotalPages = old.ReplyTotalPages
		c.ReplyNoMore = old.ReplyNoMore
	}
	if err := a.kv.put(key, c, now); err != nil {
		log.Printf("帖子正文缓存写入失败：%v", err)
		return
	}
	a.kv.prune(detailCachePrefix, detailCacheMax)
}

// markDetailOffline 在网络刷新失败、但页面上已有缓存正文时调用：保留正文，
// 在顶部说明显示的是本地缓存，而不是把整页替换成错误。
func (a *app) markDetailOffline(d *detailState) {
	if d == nil {
		return
	}
	if d.cachedAt.IsZero() {
		d.cacheNote = "网络不可用，显示上次加载的内容"
		return
	}
	d.cacheNote = "网络不可用，显示 " + formatCacheTime(d.cachedAt) + " 的本地缓存"
}
