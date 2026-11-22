package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 关注与粉丝名单（api-read.md）
//
//	GET /v1/me/follows/following     我关注的人（scope read:me）
//	GET /v1/me/follows/followers     关注我的人（scope read:me）
//	GET /v1/me/follows/unread-count  关注流未读数（scope read:me）
//
// 三个端点都只读「自己」：路径里没有用户参数，也不存在读别人名单的公开
// 端点。所以入口只在「我的」窗口的统计里，用户资料卡上的关注数仍然只是
// 数字。
//
// items[] 的字段是 id / username / avatar_url / is_pro / is_following /
// followed_at；is_following 一律表示「我是否关注了这一行的人」，在 followers
// 列表里用它决定按钮是「回关」还是「已关注」。
//
// 未读数按 last_read_at 计算：不传时服务端恒回 0，所以上次读取时间记在
// 本机（cache/offline.json 的 follows:read:<scope>），打开「动态」时更新。
// 未读数作为侧边栏「动态」一项的角标显示。

const followsPageSize = 50

// 两个名单：我关注的人、关注我的人。
const (
	followsFollowing = iota
	followsFollowers
	followsKindCount
)

var followsTabLabels = [followsKindCount]string{"我关注的人", "关注我的人"}

// followsEmptyText 是名单为空时的两行说明。
var followsEmptyText = [followsKindCount][2]string{
	{"还没有关注任何人。", "在用户资料卡上点「关注」，这里就会出现。"},
	{"还没有人关注你。", "别人关注你之后，这里会列出他们。"},
}

// followUser 是一行名单。FollowKnown 区分「服务端说不关注」和「响应里没有
// 这个字段」：后者不该把按钮画成「回关」。
type followUser struct {
	ID          string
	Username    string
	Avatar      string
	IsPro       bool
	IsFollowing bool
	FollowKnown bool
	FollowedAt  string
}

type followPage struct {
	Items      []followUser
	Total      int
	Page       int
	Limit      int
	TotalPages int
}

// followsListPath 是名单端点。第 1 页与后续页都带 page / limit，两个端点
// 的默认与上限都是 50。
func followsListPath(kind, page int) string {
	base := "/v1/me/follows/following"
	if kind == followsFollowers {
		base = "/v1/me/follows/followers"
	}
	if page < 1 {
		page = 1
	}
	return fmt.Sprintf("%s?page=%d&limit=%d", base, page, followsPageSize)
}

// followsUnreadPath 是关注流未读数。lastReadAt 为空时不带参数：服务端会
// 回 0，调用方因此不必在第一次使用时发一个注定是 0 的请求。
func followsUnreadPath(lastReadAt string) string {
	p := "/v1/me/follows/unread-count"
	if lastReadAt == "" {
		return p
	}
	q := url.Values{}
	q.Set("last_read_at", lastReadAt)
	return p + "?" + q.Encode()
}

func (c *Client) fetchFollows(ctx context.Context, kind, page int) (followPage, error) {
	return withAuthRetry(c, ctx, func(token string) (followPage, error) {
		d, err := c.getJSON(ctx, c.apiURL(followsListPath(kind, page)), token)
		if err != nil {
			return followPage{}, err
		}
		return decodeFollows(d), nil
	})
}

func decodeFollows(d json.RawMessage) followPage {
	var out followPage
	o := asObject(d)
	if o == nil {
		return out
	}
	out.Total = rawInt(o["total"])
	out.Page = rawInt(o["page"])
	out.Limit = rawInt(o["limit"])
	out.TotalPages = rawInt(o["total_pages"])
	for _, raw := range rawArray(o["items"]) {
		u := asObject(raw)
		if u == nil {
			continue
		}
		name := u.str("username", "name")
		if name == "" {
			continue
		}
		out.Items = append(out.Items, followUser{
			ID:          u.str("id"),
			Username:    name,
			Avatar:      authorAvatar(u),
			IsPro:       u.flag("is_pro"),
			IsFollowing: u.flag("is_following"),
			FollowKnown: u.has("is_following"),
			FollowedAt:  u.str("followed_at"),
		})
	}
	if out.Page == 0 {
		out.Page = 1
	}
	if out.Limit == 0 {
		out.Limit = followsPageSize
	}
	return out
}

func (c *Client) fetchFollowsUnread(ctx context.Context, lastReadAt string) (int, error) {
	return withAuthRetry(c, ctx, func(token string) (int, error) {
		d, err := c.getJSON(ctx, c.apiURL(followsUnreadPath(lastReadAt)), token)
		if err != nil {
			return 0, err
		}
		return decodeUnreadCount(d), nil
	})
}

// decodeUnreadCount 认 unread_count / count / unread，也认裸数字：文档没有
// 给这个端点的响应示例。
func decodeUnreadCount(d json.RawMessage) int {
	if o := asObject(d); o != nil {
		return o.num("unread_count", "unread", "count", "total")
	}
	var n int
	if json.Unmarshal(d, &n) == nil {
		return n
	}
	return 0
}

/* ------------------------------------------------------------------
   窗口状态与加载
-------------------------------------------------------------- */

// followList 是一份名单的加载状态：两个分段各一份，互不影响。
type followList struct {
	seq     uint64
	loading bool
	loaded  bool
	err     string
	moreErr string
	items   []followUser
	page    int
	total   int
	noMore  bool
	list    ui.ListState
}

// followsState 是「关注与粉丝」窗口的全部界面状态。只有 UI 线程读写；后台
// 请求完成后通过 publish 回填，并用 seq 丢弃过期结果。
type followsState struct {
	tab int
	// lists 是两份名单，下标是 followsFollowing / followsFollowers。
	lists [followsKindCount]followList
	// busy 是正在提交关注 / 取关的用户 id；一次只提交一个，避免连点。
	busy string
	// viewer 记录列表属于哪个账号：换账号后整份状态重建。
	viewer string
}

// list 返回某个分段的加载状态，供加载与渲染共用。
func (fs *followsState) list(kind int) *followList {
	if kind < 0 || kind >= followsKindCount {
		return &fs.lists[followsFollowing]
	}
	return &fs.lists[kind]
}

// ensureFollowsLoaded 是守卫式任务：窗口打开时把当前分段的首屏补齐，切
// 分段时也靠它拉数据。它只发起还没拉过的请求，因此可以在每帧里调用。
func (a *app) ensureFollowsLoaded() {
	fs := a.follows
	if fs == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	if fs.viewer != a.auth.username() {
		// 换账号：上一个账号的名单不能再展示。
		*fs = followsState{tab: fs.tab, viewer: a.auth.username()}
	}
	l := fs.list(fs.tab)
	if !l.loaded && !l.loading {
		a.loadFollows(fs.tab, 1)
	}
}

// loadFollows 拉某一页名单。page==1 时重建列表，page>1 时按用户 id 去重后
// 追加；续拉失败只记 moreErr，已有内容保持不动。
func (a *app) loadFollows(kind, page int) {
	fs := a.follows
	if fs == nil || page < 1 || !a.auth.loggedIn() {
		return
	}
	l := fs.list(kind)
	if l.loading {
		return
	}
	if page == 1 {
		l.seq++
		l.loaded = false
		l.items = nil
		l.noMore = false
		l.err = ""
		l.moreErr = ""
		l.list.ScrollTo(0, ui.Start)
	}
	seq := l.seq
	l.loading = true
	l.page = page
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchFollows(ctx, kind, page)
		a.publish(func() {
			if a.follows != fs || fs.list(kind) != l || l.seq != seq {
				return
			}
			l.loading = false
			l.loaded = true
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if page == 1 {
					l.err = friendlyError(err)
				} else {
					l.moreErr = friendlyError(err)
				}
				a.handleAuthErrorFor(toastFollows, err, func() { a.loadFollows(kind, page) })
				return
			}
			if page == 1 {
				l.items = res.Items
			} else {
				seen := make(map[string]bool, len(l.items))
				for _, u := range l.items {
					seen[u.ID] = true
				}
				added := 0
				for _, u := range res.Items {
					if !seen[u.ID] {
						l.items = append(l.items, u)
						seen[u.ID] = true
						added++
					}
				}
				if added == 0 {
					l.noMore = true
				}
			}
			l.page = page
			l.total = res.Total
			if len(res.Items) == 0 || (res.TotalPages > 0 && page >= res.TotalPages) {
				l.noMore = true
			}
			l.err = ""
			l.moreErr = ""
			for _, u := range res.Items {
				a.prefetchAvatars(u.Avatar)
			}
		})
	}()
}

// toggleFollowRow 关注 / 取关名单里的一个人。未登录先走全局授权弹窗；
// 成功后两份名单里的同一个人一起更新，「我关注的人」里取关的那一行直接
// 移出列表。
func (a *app) toggleFollowRow(kind int, u followUser) {
	fs := a.follows
	if fs == nil || u.ID == "" || fs.busy != "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastFollows, "登录后才能关注用户。", func() {
			if a.follows == fs {
				a.toggleFollowRow(kind, u)
			}
		})
		return
	}
	desired := !u.IsFollowing
	fs.busy = u.ID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), followTimeout)
	go func() {
		defer cancel()
		err := a.client.setFollow(ctx, u.ID, desired)
		a.publish(func() {
			if a.follows != fs {
				return
			}
			if fs.busy == u.ID {
				fs.busy = ""
			}
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastFollows, err, func() { a.toggleFollowRow(kind, u) }) {
					return
				}
				a.toast(toastFollows, friendlyError(err))
				return
			}
			fs.applyFollow(u.ID, desired)
			if desired {
				a.toast(toastFollows, "已关注 @"+u.Username)
			} else {
				a.toast(toastFollows, "已取消关注 @"+u.Username)
			}
			// 「我的」窗口的关注数已经过期：清掉资料，下次打开重新读。
			a.invalidateProfileInfo()
		})
	}()
}

// applyFollow 把关注状态写回两份名单。取关时从「我关注的人」里移除那一行
// （名单本身是「我关注的人」），关注数跟着减一。
func (fs *followsState) applyFollow(id string, following bool) {
	for kind := 0; kind < followsKindCount; kind++ {
		l := &fs.lists[kind]
		kept := l.items[:0]
		removed := 0
		for _, u := range l.items {
			if u.ID != id {
				kept = append(kept, u)
				continue
			}
			if kind == followsFollowing && !following {
				removed++
				continue
			}
			u.IsFollowing = following
			u.FollowKnown = true
			kept = append(kept, u)
		}
		l.items = kept
		if removed > 0 && l.total >= removed {
			l.total -= removed
		}
	}
}

// invalidateProfileInfo 让「我的」窗口下次打开时重读资料：关注 / 粉丝数
// 在名单里改过之后，概览里的数字不能再沿用旧值。
func (a *app) invalidateProfileInfo() {
	if a.profile == nil {
		return
	}
	a.profile.infoLoaded = false
}

/* ------------------------------------------------------------------
   关注流未读数
-------------------------------------------------------------- */

const followsReadPrefix = "follows:read:"

// followsReadRecord 是本机记下的「上次读关注流的时间」。
type followsReadRecord struct {
	At time.Time `json:"at"`
}

func (a *app) followsReadKey() string {
	scope := a.accountScope()
	if scope == "" {
		return ""
	}
	return followsReadPrefix + scope
}

// followsReadAt 读本机记下的上次读取时间。没有记录时返回零值：第一次用
// 这个功能时服务端会回 0，不值得发一次注定是 0 的请求。
func (a *app) followsReadAt() time.Time {
	key := a.followsReadKey()
	if key == "" || a.kv == nil {
		return time.Time{}
	}
	var rec followsReadRecord
	if _, ok := a.kv.get(key, &rec); !ok {
		return time.Time{}
	}
	return rec.At
}

// markFollowsRead 在打开「动态」时把上次读取时间推到此刻，并清掉角标。
func (a *app) markFollowsRead() {
	a.followUnread = 0
	key := a.followsReadKey()
	if key == "" || a.kv == nil {
		return
	}
	now := time.Now().UTC()
	if err := a.kv.put(key, followsReadRecord{At: now}, now); err != nil {
		return
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// refreshFollowsUnread 读一次关注流未读数，作为侧边栏「动态」的角标。
// 失败时保留上一次的数字：一个角标不值得打扰用户。
func (a *app) refreshFollowsUnread() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	at := a.followsReadAt()
	if at.IsZero() {
		return
	}
	actor := a.auth.username()
	a.followUnreadGen++
	gen := a.followUnreadGen
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		n, err := a.client.fetchFollowsUnread(ctx, at.Format(time.RFC3339))
		a.publish(func() {
			if a.followUnreadGen != gen || err != nil {
				return
			}
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			a.followUnread = n
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}

// followsTimelineTab 是侧边栏「动态」的下标：未读角标挂在它上面。
func followsTimelineTab() int {
	for i, s := range tabSpecs {
		if s.Path == "/v1/me/follows/timeline" {
			return i
		}
	}
	return -1
}

// resetFollowsUnread 清掉未读角标并丢弃在途结果：它只属于当时那个会话。
func (a *app) resetFollowsUnread() {
	a.followUnread = 0
	a.followUnreadGen++
}

// followRowSummary 是一行名单的说明文字：关注时间，缺字段时不写。
func followRowSummary(u followUser) string {
	when := relativeTime(u.FollowedAt)
	if when == "" {
		return ""
	}
	return strings.TrimSpace(when + "关注")
}

// followPageLabel 是名单标题右侧的计数：优先用服务端总数。
func followPageLabel(l *followList) string {
	n := l.total
	if n == 0 {
		n = len(l.items)
	}
	return strconv.Itoa(n)
}
