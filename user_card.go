package main

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

// 「用户资料」窗口的数据层与界面状态。窗口本身在 user_card_ui.go：点任何
// 一处的统一用户标识（头像 + 昵称）打开，入口见 user_ui.go 的 userTag。
//
// 三组只读端点拼出一张完整的资料卡：
//
//	资料    GET /v1/users/:userName
//	主题    GET /v1/users/:userName/topics
//	回复    GET /v1/users/:userName/replies
//
// 关注是这里唯一的写操作，走 follow.go 的同一个提交路径（scope interact）。
//
// 接口不下发简介正文，只给 has_readme 这个布尔值，所以「个人简介」一块只说明
// 「填写过」并给出到站点的出口，不编造正文；同样也没有「获赞」这类聚合数字，
// 统计只展示接口真给了的字段。

// 窗口的默认尺寸。和通知、我的两个窗口一样用 StateKey 记住位置与大小，
// 这里只是首次打开时的默认值。
const (
	userCardWindowWidth  = 480
	userCardWindowHeight = 640
	userCardMinWidth     = 380
	userCardMinHeight    = 420

	// userCardPageSize 是两个列表每页的条数：两个端点都默认 20、上限 50。
	userCardPageSize = 20
	// userCardBannerHeight 是顶部横幅的高度：装饰用，同时是拖动窗口的区域。
	userCardBannerHeight = 96
	// userCardAvatar 是资料卡里大头像的边长。
	userCardAvatar = 64
)

// 资料卡里的两个标签页。
const (
	userCardTabTopics = iota
	userCardTabReplies
)

var userCardTabLabels = []string{"他发布的主题", "他的最新回复"}

// userCardListKind 区分资料卡里的两个列表：它们只有端点和行渲染不同。
type userCardListKind int

const (
	userCardTopics userCardListKind = iota
	userCardReplies
)

// userCardSpec 是「某人发布的主题 / 评论」的端点。两者都要 username 而不是
// 「我」，因此昵称必须转义，避免特殊字符改变请求路径。
func userCardSpec(name string, kind userCardListKind, page int) tabSpec {
	if kind == userCardReplies {
		return tabSpec{
			Label:   userCardTabLabels[userCardTabReplies],
			Path:    "/v1/users/" + url.PathEscape(name) + "/replies",
			Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(userCardPageSize),
			ListKey: "items",
			Paging:  pagingPages,
			Kind:    kindComment,
		}
	}
	return tabSpec{
		Label:   userCardTabLabels[userCardTabTopics],
		Path:    "/v1/users/" + url.PathEscape(name) + "/topics",
		Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(userCardPageSize),
		ListKey: "posts",
		Paging:  pagingPages,
		Kind:    kindTopic,
	}
}

// userCardList 是一份列表的加载状态：两个标签页各有一份，互不影响。
type userCardList struct {
	seq     uint64
	loading bool
	loaded  bool
	err     string
	moreErr string
	items   []Item
	page    int
	total   int
	noMore  bool
	list    ui.ListState
}

// userCardState 是「用户资料」窗口的全部界面状态。只有 UI 线程读写；后台
// 请求完成后通过 publish 回填，并用每类数据的 seq 丢弃过期结果。
type userCardState struct {
	// username 是正在展示的用户（公开标识）。换人时整份状态重建。
	username string

	profileSeq     uint64
	profileLoading bool
	profileLoaded  bool
	profileErr     string
	user           accountProfile

	tab     int
	topics  userCardList
	replies userCardList

	// follow 是当前登录用户对该用户的关注状态，沿用帖子详情的同一套字段。
	follow followState
}

func newUserCardState(username string) *userCardState {
	return &userCardState{username: username}
}

// list 返回某一类列表的状态，供加载与渲染共用。
func (cs *userCardState) list(kind userCardListKind) *userCardList {
	if kind == userCardReplies {
		return &cs.replies
	}
	return &cs.topics
}

// title 是资料卡的主标题：优先展示名，其次用户名，最后退回一个占位。
func (cs *userCardState) title() string {
	if cs.user.DisplayName != "" {
		return cs.user.DisplayName
	}
	if cs.user.Name != "" {
		return cs.user.Name
	}
	if cs.username != "" {
		return cs.username
	}
	return "用户资料"
}

/* ------------------------------------------------------------------
   加载：每个请求都有 seq，过期的结果直接丢弃
--------------------------------------------------------------- */

// ensureUserCardLoaded 是守卫式任务：窗口打开时把首屏需要的数据补齐。
// 它只发起还没拉过的请求，因此可以在每帧里调用。
func (a *app) ensureUserCardLoaded() {
	cs := a.userCard
	if cs == nil || !a.autoLoad || cs.username == "" {
		return
	}
	if !cs.profileLoaded && !cs.profileLoading {
		a.loadUserCardProfile()
	}
	// 两个标签页在打开时各拉一页：统计里的主题数与回复数要立刻有数，
	// 切标签也不用等。
	for _, kind := range []userCardListKind{userCardTopics, userCardReplies} {
		if l := cs.list(kind); !l.loaded && !l.loading {
			a.loadUserCardList(kind, 1)
		}
	}
}

func (a *app) loadUserCardProfile() {
	cs := a.userCard
	if cs == nil || cs.username == "" || cs.profileLoading {
		return
	}
	cs.profileSeq++
	seq := cs.profileSeq
	cs.profileLoading = true
	cs.profileErr = ""
	name := cs.username
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		p, err := a.client.fetchAccountProfile(ctx, name)
		a.publish(func() {
			if a.userCard != cs || cs.profileSeq != seq {
				return
			}
			cs.profileLoading = false
			cs.profileLoaded = true
			if err != nil {
				cs.profileErr = friendlyError(err)
				return
			}
			cs.user = p
			cs.profileErr = ""
			a.prefetchAvatars(p.Avatar)
			a.seedUserCardFollow()
		})
	}()
}

// seedUserCardFollow 用资料里的 is_following 初始化关注按钮：资料响应本身
// 就带了这个字段，不必再单独请求一次用户主页（帖子详情那边才需要）。
func (a *app) seedUserCardFollow() {
	cs := a.userCard
	if cs == nil || !a.auth.loggedIn() || cs.follow.busy {
		return
	}
	if id := cs.user.ID; id != "" {
		cs.follow.userID = id
	}
	cs.follow.viewer = a.auth.username()
	cs.follow.need = false
	if cs.user.FollowKnown {
		cs.follow.known = true
		cs.follow.checked = true
		cs.follow.following = cs.user.IsFollowing
	}
}

// loadUserCardList 拉某一类列表的第 page 页。page==1 时重建列表，page>1 时
// 按 ID 去重后追加；续拉失败只记 moreErr，已有内容保持不动。
func (a *app) loadUserCardList(kind userCardListKind, page int) {
	cs := a.userCard
	if cs == nil || cs.username == "" || page < 1 {
		return
	}
	l := cs.list(kind)
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
	name := cs.username
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetch(ctx, userCardSpec(name, kind, page), page)
		a.publish(func() {
			if a.userCard != cs || cs.username != name || l.seq != seq {
				return
			}
			l.loading = false
			l.loaded = true
			if err != nil {
				if page == 1 {
					l.err = friendlyError(err)
				} else {
					l.moreErr = friendlyError(err)
				}
				return
			}
			if page == 1 {
				l.items = res.Items
			} else {
				seen := make(map[string]bool, len(l.items))
				for _, it := range l.items {
					seen[it.Key] = true
				}
				added := 0
				for _, it := range res.Items {
					if !seen[it.Key] {
						l.items = append(l.items, it)
						seen[it.Key] = true
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
			a.prefetchItemAvatars(res.Items)
		})
	}()
}

/* ------------------------------------------------------------------
   关注：与帖子详情共用 follow.go 的提交路径
--------------------------------------------------------------- */

// toggleUserCardFollow 关注或取消关注资料卡上的这位用户。未登录时先走全局
// 授权弹窗，授权成功后同一次操作自动重跑；缺 scope 的 403 也一样。
func (a *app) toggleUserCardFollow(follow bool) {
	cs := a.userCard
	if cs == nil {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能关注这位用户。", func() {
			if a.userCard == cs {
				a.toggleUserCardFollow(follow)
			}
		})
		return
	}
	if cs.follow.userID == "" {
		cs.follow.userID = cs.user.ID
	}
	a.submitFollow(&cs.follow, follow,
		func() bool { return a.userCard == cs },
		func() {
			if a.userCard == cs {
				a.toggleUserCardFollow(follow)
			}
		})
}

// userCardFollowing 报告关注按钮该显示「已关注」还是「关注他」。只认属于
// 当前登录账号的状态：换账号后、新状态回来之前按未关注展示。
func (a *app) userCardFollowing() bool {
	cs := a.userCard
	if cs == nil || !a.auth.loggedIn() || !cs.follow.known {
		return false
	}
	return cs.follow.viewer == a.auth.username() && cs.follow.following
}

// userCardCanFollow 判断要不要显示关注按钮：自己的资料卡不显示。
func (a *app) userCardCanFollow() bool {
	cs := a.userCard
	if cs == nil || cs.username == "" {
		return false
	}
	return !a.followAuthorIsSelf(cs.username)
}

// refreshUserCardForAuth 在登录态变化后重新读取资料：is_following 与「是不是
// 我」都跟着账号走。只在主线程调用。
func (a *app) refreshUserCardForAuth() {
	cs := a.userCard
	if cs == nil {
		return
	}
	cs.follow = followState{}
	cs.profileSeq++
	cs.profileLoaded = false
	cs.profileErr = ""
	a.ensureUserCardLoaded()
	a.redrawUserCardWindow()
}
