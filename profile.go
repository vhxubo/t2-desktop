package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 「我的」窗口的数据层与界面状态。窗口本身在 profile_ui.go：它是独立窗口
// （和通知、设置一样），入口是标题栏上登录账号的头像。
//
// 四个标签页对应这几组只读端点：
//
//	概览        GET /v1/users/:userName（最近统计）
//	我的内容    GET /v1/me/topics、/v1/me/favorites、/v1/me/bookmarks、/v1/me/collections
//	我的回复    GET /v1/users/:userName/replies
//	权限与账号  GET /v1/users/:userName + 令牌里的 scope
//
// 金币不在这里：金币页是独立的金币窗口（见 coins_window.go），入口是标题栏
// 上登录账号左侧的金币数。本窗口不展示余额、签到，也不提供金币相关的入口。
//
// 「我的内容」一页里有四个分段：帖子、收藏、书签、合集。这四类原先占着主
// 窗口的侧边栏，现在只在这里出现。
//
// 这里没有任何写操作：发帖、互动等操作都在各自的窗口或站点完成。

// 弹窗的默认尺寸。它和通知窗口一样用 StateKey 记住位置与大小，这里只是
// 首次打开时的默认值。
const (
	profileWindowWidth  = 520
	profileWindowHeight = 640
	profileWinMinWidth  = 430
	profileWinMinHeight = 480

	// profileReplyPageSize 是「我的回复」每页条数：该端点默认 20、上限 50。
	profileReplyPageSize = 20
)

// 弹窗里的标签页。金币页不在这个窗口里，去独立的金币窗口看。
const (
	profileTabOverview = iota
	profileTabContent
	profileTabReplies
	profileTabAccount
)

var profileTabLabels = []string{"概览", "我的内容", "我的回复", "权限与账号"}

// 「我的内容」里的四个分段。
const (
	profileContentTopics = iota
	profileContentFavorites
	profileContentBookmarks
	profileContentCollections
	profileContentCount
)

// profileContentLabels 是分段按钮上的短标签；spec 里的 Label 是完整说法，
// 用在错误提示里（「需要登录后才能查看「我的收藏」」）。
var profileContentLabels = []string{"帖子", "收藏", "书签", "合集"}

// profileContentSpecs 是「我的内容」四个分段的端点。字段名与分页方式沿用
// 接口文档：帖子与合集整页返回，收藏与书签按页返回。
var profileContentSpecs = []tabSpec{
	{"我的帖子", "/v1/me/topics", "", "auto", true, pagingOff, kindTopic},
	{"我的收藏", "/v1/me/favorites", "page=1&limit=50", "posts", true, pagingPages, kindTopic},
	{"我的书签", "/v1/me/bookmarks", "page=1&limit=50", "items", true, pagingPages, kindComment},
	{"我的合集", "/v1/me/collections", "", "", true, pagingOff, kindCollection},
}

// profileNow 单独提出来给测试替换：注册天数按本地时间算。
var profileNow = func() time.Time { return time.Now() }

// profileScopeLabels 把授权范围翻成一句人话。未列出的范围原样展示，新
// scope 上线时不会显示成空白，也不会假装认识它。
var profileScopeLabels = map[string]string{
	"profile":       "读取个人资料与头像",
	"read":          "读取公开内容",
	"read:me":       "读取我的内容（收藏 / 书签 / 通知）",
	"read:economy":  "读取金币余额、流水与签到状态",
	"write:comment": "发表评论",
	"write:topic":   "发表帖子",
	"write:economy": "金币变动（签到 / 打赏 / 解锁）",
	"interact":      "收藏、关注等互动",
	"upload":        "上传图片",
}

func profileScopeLabel(scope string) string {
	if label, ok := profileScopeLabels[scope]; ok {
		return label
	}
	return scope
}

// accountProfile 是 GET /v1/users/:userName 里窗口用得到的字段。接口不返回
// 邮箱、金币与简介正文，所以这里也只有公开资料。
type accountProfile struct {
	Name        string
	DisplayName string
	Avatar      string
	UserNumber  int
	CreatedAt   string
	Level       int
	IsPro       bool
	Followers   int
	Following   int
	HomeView    int

	// 以下字段「我的」窗口不展示，用户资料卡（user_card.go）要用：
	// ID 是关注端点需要的用户 uuid（用户名只用于展示与链接），Role 是站点
	// 角色，Badges 是佩戴中的徽章，HasReadme 只说明有没有填写个人简介 ——
	// 接口不下发简介正文。
	ID        string
	Role      string
	Badges    []badgeInfo
	HasReadme bool
	// IsFollowing 表示当前登录用户是否已关注他；FollowKnown 说明响应里到底
	// 带没带这个字段 —— 匿名响应不带，不能当成「未关注」。
	IsFollowing bool
	FollowKnown bool
}

// profileState 是「我的」窗口的全部界面状态。只有 UI 线程读写；后台请求
// 完成后通过 publish 回填，并用每类数据的 seq 丢弃过期结果。
type profileState struct {
	tab int

	// 账号资料（用户名 / 昵称 / 用户 ID / 注册时间）
	infoSeq     uint64
	infoLoading bool
	infoLoaded  bool
	infoErr     string
	user        accountProfile

	// 我的内容：帖子 / 收藏 / 书签 / 合集各有一份列表状态，切换分段时保留
	// 已经拉到的内容，不重新请求。加载走主窗口同一套通用加载（loadSpec）。
	contentKind int
	content     [profileContentCount]tabState

	// 我的回复
	replySeq     uint64
	replyLoading bool
	replyLoaded  bool
	replyErr     string
	replyMoreErr string
	replies      []Item
	replyPage    int
	replyTotal   int
	replyNoMore  bool
	replyList    ui.ListState
}

func newProfileState() *profileState {
	return &profileState{}
}

/* ------------------------------------------------------------------
   端点契约
---------------------------------------------------------------- */

// profileRepliesSpec 是「我的回复」的端点：公开资料里该用户发布的评论。
// 它需要用户名而不是「我」，因此调用方要先知道当前账号的昵称。
func profileRepliesSpec(name string, page int) tabSpec {
	return tabSpec{
		Label:   "我的回复",
		Path:    "/v1/users/" + url.PathEscape(name) + "/replies",
		Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(profileReplyPageSize),
		ListKey: "items",
		Paging:  pagingPages,
		Kind:    kindComment,
	}
}

func (c *Client) fetchProfileReplies(ctx context.Context, name string, page int) (pageResult, error) {
	return c.fetch(ctx, profileRepliesSpec(name, page), page)
}

// fetchAccountProfile 读公开资料。它只要 read / profile 之一即可，登录后
// 照常带令牌：站点会把 show_setting 标成 true。路径复用 follow.go 的
// userPath，昵称转义规则只保留一处。
func (c *Client) fetchAccountProfile(ctx context.Context, name string) (accountProfile, error) {
	if name == "" {
		return accountProfile{}, errors.New("还不知道当前账号的用户名")
	}
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(userPath(name)), token)
	})
	if err != nil {
		return accountProfile{}, err
	}
	return decodeAccountProfile(d), nil
}

func decodeAccountProfile(d json.RawMessage) accountProfile {
	o := asObject(d)
	if o == nil {
		return accountProfile{}
	}
	// 有的接口把账号包在 user / profile 里，顶层字段优先。
	merged := object{}
	for k, v := range o {
		merged[k] = v
	}
	for _, key := range []string{"user", "profile", "account"} {
		if sub := o.sub(key); sub != nil {
			for k, v := range sub {
				if _, exists := merged[k]; !exists {
					merged[k] = v
				}
			}
		}
	}
	return accountProfile{
		Name:        merged.str("username", "name"),
		DisplayName: merged.str("display_name", "displayName", "nickname"),
		Avatar:      merged.str("avatar_url", "avatarUrl", "avatar"),
		UserNumber:  merged.num("user_number", "userNumber", "number"),
		CreatedAt:   merged.str("created_at", "createdAt"),
		Level:       merged.num("level"),
		IsPro:       merged.flag("is_pro", "isPro"),
		Followers:   merged.num("follower_count", "followers_count", "followers"),
		Following:   merged.num("following_count", "followings_count", "following"),
		HomeView:    merged.num("home_view", "homeView", "home_views"),
		ID:          merged.str("id"),
		Role:        merged.str("role"),
		Badges:      decodeBadges(merged),
		HasReadme:   merged.flag("has_readme", "hasReadme"),
		IsFollowing: merged.flag("is_following"),
		FollowKnown: merged.has("is_following"),
	}
}

/* ------------------------------------------------------------------
   展示辅助
---------------------------------------------------------------- */

// profileTime 解析注册时间：接口给的是 ISO 8601，历史数据里也有空格分隔的
// 写法，两种都认。
func profileTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// profileRegisteredText 是「注册时间 + 注册天数」那一行。天数按自然日算，
// 注册当天就记为 1 天；解析不了时间时只说明未知，不编一个数字。
func profileRegisteredText(raw string) string {
	t, ok := profileTime(raw)
	if !ok {
		if raw == "" {
			return ""
		}
		return "注册时间 " + raw
	}
	days := int(profileNow().Sub(t).Hours() / 24)
	if days < 0 {
		days = 0
	}
	return fmt.Sprintf("注册 %s · %d 天", t.Format("2006-01-02"), days+1)
}

// profileIdentityLine 是头像下面那行：用户 ID 与注册信息，中间用竖线隔开，
// 和站点的资料卡一个意思。
func profileIdentityLine(u accountProfile) string {
	parts := make([]string, 0, 2)
	if u.UserNumber > 0 {
		parts = append(parts, "用户 ID: #"+strconv.Itoa(u.UserNumber))
	} else {
		parts = append(parts, "用户 ID: —")
	}
	if reg := profileRegisteredText(u.CreatedAt); reg != "" {
		parts = append(parts, reg)
	}
	return strings.Join(parts, "  |  ")
}

/* ------------------------------------------------------------------
   加载：每个请求都有 seq，过期的结果直接丢弃
---------------------------------------------------------------- */

// ensureProfileLoaded 是守卫式任务：窗口打开时把首屏需要的数据补齐。
// 它只发起还没拉过的请求，因此可以在每帧里调用。
func (a *app) ensureProfileLoaded() {
	ps := a.profile
	if ps == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	name := a.auth.username()
	if name != "" && !ps.infoLoaded && !ps.infoLoading {
		a.loadProfileInfo(name)
	}
	switch ps.tab {
	case profileTabContent:
		st := &ps.content[ps.contentKind]
		if !st.loaded && !st.loading && !st.authWait {
			a.loadProfileContent(ps.contentKind, 1)
		}
	case profileTabReplies:
		if !ps.replyLoaded && !ps.replyLoading && name != "" {
			a.loadProfileReplies(name, 1)
		}
	}
}

func (a *app) loadProfileInfo(name string) {
	ps := a.profile
	if ps == nil || name == "" || ps.infoLoading {
		return
	}
	ps.infoSeq++
	seq := ps.infoSeq
	ps.infoLoading = true
	ps.infoErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		p, err := a.client.fetchAccountProfile(ctx, name)
		a.publish(func() {
			if a.profile != ps || ps.infoSeq != seq {
				return
			}
			ps.infoLoading = false
			ps.infoLoaded = true
			if err != nil {
				ps.infoErr = friendlyError(err)
				return
			}
			ps.user = p
			ps.infoErr = ""
			a.prefetchAvatars(p.Avatar)
		})
	}()
}

// loadProfileContent 拉「我的内容」某一分段的第 page 页。它复用主窗口的
// 通用加载：四个分段的 spec 与错误、分页、去重都跟侧边栏列表同一套。
func (a *app) loadProfileContent(kind, page int) {
	ps := a.profile
	if ps == nil || kind < 0 || kind >= profileContentCount {
		return
	}
	a.loadSpec(&ps.content[kind], profileContentSpecs[kind], page,
		func() { a.loadProfileContent(kind, page) })
}

// reloadProfileContent 丢掉某一分段已拉到的内容，再从第一页重拉（分段上的
// 「刷新」）。
func (a *app) reloadProfileContent(kind int) {
	ps := a.profile
	if ps == nil || kind < 0 || kind >= profileContentCount {
		return
	}
	st := &ps.content[kind]
	if st.loading {
		return
	}
	st.authWait = false
	st.loaded = false
	st.items = nil
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.loadProfileContent(kind, 1)
}

// loadProfileReplies 拉第 page 页。page==1 时重建列表，page>1 时按 ID 去重
// 后追加；续拉失败只记 moreErr，已有内容保持不动。
func (a *app) loadProfileReplies(name string, page int) {
	ps := a.profile
	if ps == nil || name == "" || page < 1 || ps.replyLoading {
		return
	}
	if page == 1 {
		ps.replySeq++
		ps.replyLoaded = false
		ps.replies = nil
		ps.replyNoMore = false
		ps.replyErr = ""
		ps.replyMoreErr = ""
		ps.replyList.ScrollTo(0, ui.Start)
	}
	seq := ps.replySeq
	ps.replyLoading = true
	ps.replyPage = page
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchProfileReplies(ctx, name, page)
		a.publish(func() {
			if a.profile != ps || ps.replySeq != seq {
				return
			}
			ps.replyLoading = false
			ps.replyLoaded = true
			if err != nil {
				if page == 1 {
					ps.replyErr = friendlyError(err)
				} else {
					ps.replyMoreErr = friendlyError(err)
				}
				return
			}
			if page == 1 {
				ps.replies = res.Items
			} else {
				seen := make(map[string]bool, len(ps.replies))
				for _, it := range ps.replies {
					seen[it.Key] = true
				}
				added := 0
				for _, it := range res.Items {
					if !seen[it.Key] {
						ps.replies = append(ps.replies, it)
						seen[it.Key] = true
						added++
					}
				}
				if added == 0 {
					ps.replyNoMore = true
				}
			}
			ps.replyPage = page
			ps.replyTotal = res.Total
			if len(res.Items) == 0 || (res.TotalPages > 0 && page >= res.TotalPages) {
				ps.replyNoMore = true
			}
			ps.replyErr = ""
			ps.replyMoreErr = ""
			a.prefetchItemAvatars(res.Items)
		})
	}()
}

// reloadProfile 是窗口里的「刷新」：保留当前标签页与「我的内容」当前分段，
// 其余状态清空后按需重拉。
func (a *app) reloadProfile() {
	ps := a.profile
	if ps == nil {
		return
	}
	tab, content := ps.tab, ps.contentKind
	*a.profile = *newProfileState()
	a.profile.tab, a.profile.contentKind = tab, content
	a.ensureProfileLoaded()
}
