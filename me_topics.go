package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// 编辑器「插入帖子」选择器的数据层与加载状态。
//
// GET /v1/me/topics（scope profile）是选择器专用端点：固定返回最新 20 条、
// 不分页，字段只有 short_id / title / created_at / node —— 没有回复数。
// 它和「我的」/「用户」窗口展示帖子列表用的
// GET /v1/users/:userName/topics 不是一回事：后者分页、带完整字段，还能按
// 合集名筛选（见 profile.go 与 user_card.go）。
//
// 选中一条后往正文插入 [标题](https://2libra.com/post/节点/short_id)，
// 与工具条「链接」按钮同一种 Markdown 写法；应用内的 parseInAppTopicLink
// 认得这个路径，点开就地进详情。

// myTopicsPath 是我的帖子选择器端点。q 做标题关键字过滤，空串时不带参数。
// 服务端固定返回最新 20 条，这里不传 page / limit，别照搬列表端点的写法。
func myTopicsPath(q string) string {
	if q == "" {
		return "/v1/me/topics"
	}
	return "/v1/me/topics?q=" + url.QueryEscape(q)
}

// myTopicPick 是选择器用的一条帖子。只留插入链接需要的字段，其余未知字段
// 按 v1「只增字段」的约定忽略。
type myTopicPick struct {
	ShortID   string
	Title     string
	NodeSlug  string
	NodeName  string
	CreatedAt string
}

// fetchMyTopicsForPicker 读选择器的帖子列表。必须带令牌（scope profile），
// 因此走 withAuthRetry：缺 scope 的 401/403 由调用方交给授权链路重跑。
func (c *Client) fetchMyTopicsForPicker(ctx context.Context, q string) ([]myTopicPick, error) {
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(myTopicsPath(q)), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeMyTopicPicks(d), nil
}

// decodeMyTopicPicks 容错解码：d 可能是裸数组，也可能包在 posts / items /
// topics / list 里；node 里只取 slug 与 name。没有 short_id 或标题的行跳过，
// 它们没法拼出可点的链接。
func decodeMyTopicPicks(d json.RawMessage) []myTopicPick {
	list := rawArray(d)
	if len(list) == 0 {
		if o := asObject(d); o != nil {
			for _, k := range []string{"posts", "items", "topics", "list"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	out := make([]myTopicPick, 0, len(list))
	for _, raw := range list {
		o := asObject(raw)
		if o == nil {
			continue
		}
		pick := myTopicPick{
			ShortID:   o.str("short_id", "shortId", "id"),
			Title:     o.str("title"),
			CreatedAt: o.str("created_at", "createdAt"),
		}
		if node := o.sub("node"); node != nil {
			pick.NodeSlug = node.str("slug")
			pick.NodeName = node.str("name")
		}
		if pick.ShortID == "" || pick.Title == "" {
			continue
		}
		out = append(out, pick)
	}
	return out
}

// myTopicPickLink 把一条结果写成 Markdown 链接。标题里的方括号要转义，
// 否则会截断链接文字；节点 slug 一并写进路径，和站点的
// /post/<slug>/<short_id> 一致，应用内能识别并就地打开详情。
func myTopicPickLink(p myTopicPick) string {
	title := strings.NewReplacer("[", "\\[", "]", "\\]").Replace(p.Title)
	return "[" + title + "](" + topicURL(p.ShortID, p.NodeSlug) + ")"
}

// 选择器失败后的退避：10s / 20s / 40s，封顶 1 分钟。视图每帧都会调 ensure，
// 不挡住失败重试的话接口一挂就会每帧重发注定失败的请求。
const myTopicRetryMax = time.Minute

func myTopicRetryAfter(failures int) time.Duration {
	delay := 10 * time.Second
	for i := 1; i < failures && delay < myTopicRetryMax; i++ {
		delay *= 2
	}
	if delay > myTopicRetryMax {
		delay = myTopicRetryMax
	}
	return delay
}

// topicDue 报告这次查询现在能不能发起。同一个查询成功后不再重发；换了
// 关键词立即重发；失败后在退避窗口内跳过。
func (e *mdEditor) topicDue(now time.Time) bool {
	if e.topicLoading {
		return false
	}
	if e.topicLoaded && e.topicQueried == e.topicQuery {
		return false
	}
	if e.topicErr == "" {
		return true
	}
	return now.Sub(e.topicTriedAt) >= myTopicRetryAfter(e.topicFailures)
}

// ensureMyTopicPicker 是守卫式任务：面板打开、已登录且到了重试时间时才拉。
// 它可以在每帧调用，未到重试时间的失败会被 topicDue 挡掉。
func (a *app) ensureMyTopicPicker(t uploadTarget, e *mdEditor) {
	if e == nil || !e.topicOpen || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	if !e.topicDue(time.Now()) {
		return
	}
	// 换关键词做一次短防抖：第一帧记下时间，250ms 内继续打字就等打字停
	// 下来再发，避免每敲一个键都打一次接口。
	if e.topicQueried != e.topicQuery && e.topicEditAt.IsZero() {
		e.topicEditAt = time.Now()
	}
	if !e.topicEditAt.IsZero() && time.Since(e.topicEditAt) < 250*time.Millisecond {
		return
	}
	// 换关键词就丢掉上一轮的结果与失败记录，免得旧列表在搜索框里停留。
	e.topicLoaded = false
	e.topicErr = ""
	e.topicLoading = true
	e.topicTriedAt = time.Now()
	e.topicSeq++
	seq := e.topicSeq
	query := e.topicQuery
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchMyTopicsForPicker(ctx, query)
		a.publish(func() {
			if e.topicSeq != seq || !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			e.topicLoading = false
			if err != nil {
				if !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				e.topicErr = friendlyError(err)
				e.topicFailures++
				e.topicTriedAt = time.Now()
				// 补完授权后立刻重跑，清掉刚记下的退避时间。
				a.handleAuthErrorFor(a.editorToastTarget(t), err, func() {
					e.topicTriedAt = time.Time{}
					a.ensureMyTopicPicker(t, e)
				})
				return
			}
			e.topicFailures = 0
			e.topicLoaded = true
			e.topicQueried = query
			e.topicEditAt = time.Time{}
			e.topicItems = items
			e.topicErr = ""
		})
	}()
}

// reloadMyTopicPicker 手动重试：清掉退避与已加载标记，马上再拉一次。搜索
// 框的内容由绑定在下一帧写回状态，因此传 query 进来，不用等下一帧。
func (a *app) reloadMyTopicPicker(t uploadTarget, e *mdEditor, query string) {
	if e == nil {
		return
	}
	e.topicQuery = query
	// 显式重试 / 点搜索按钮不算「正在打字」：把 queried 对齐到当前词，
	// ensure 就不会再起一次防抖等待，这次立刻发出去。
	e.topicQueried = query
	e.topicTriedAt = time.Time{}
	e.topicEditAt = time.Time{}
	e.topicFailures = 0
	e.topicLoaded = false
	e.topicLoading = false
	e.topicErr = ""
	a.ensureMyTopicPicker(t, e)
}
