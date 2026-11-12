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
// 四个标签页对应四组只读端点：
//
//	概览与资产  GET /v1/me/coins/balance、/v1/me/sign、/v1/me/coins/transactions
//	我的帖子    GET /v1/me/topics
//	我的回复    GET /v1/users/:userName/replies
//	权限与账号  GET /v1/users/:userName + 令牌里的 scope
//
// 这里没有任何写操作：签到、打赏、发帖都引导到站点完成。

// 弹窗的默认尺寸。它和通知窗口一样用 StateKey 记住位置与大小，这里只是
// 首次打开时的默认值。
const (
	profileWindowWidth  = 520
	profileWindowHeight = 640
	profileWinMinWidth  = 430
	profileWinMinHeight = 480

	// profileReplyPageSize 是「我的回复」每页条数：该端点默认 20、上限 50。
	profileReplyPageSize = 20
	// profileTxShown 是「金币明细」展开后直接列出的最近几条。
	profileTxShown = 6
	// profileListLimit 是「我的帖子」的条数上限：/v1/me/topics 不分页，只作为
	// 选择器使用，整页返回，这里只展示最新的这些条，其余引导到站点。
	profileListLimit = 30
)

// 弹窗里的四个标签页。
const (
	profileTabOverview = iota
	profileTabTopics
	profileTabReplies
	profileTabAccount
)

var profileTabLabels = []string{"概览与资产", "我的帖子", "我的回复", "权限与账号"}

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

	// 金币余额与今日签到
	coinSeq     uint64
	coinLoading bool
	coinLoaded  bool
	coinErr     string
	balance     int
	balanceOK   bool
	sign        coinSignStatus

	// 最近金币明细：点「展开」才拉，收起后保留已拉到的数据
	txSeq     uint64
	txLoading bool
	txLoaded  bool
	txOpen    bool
	txErr     string
	txItems   []coinTransaction

	// 我的帖子
	topicSeq     uint64
	topicLoading bool
	topicLoaded  bool
	topicErr     string
	topics       []Item
	topicList    ui.ListState

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

// profileTopicsSpec 是「我的帖子」的端点。它不参与侧边栏的 tabSpecs，
// 只是复用通用的 fetch / decodePage：不分页、列表字段按 auto 识别。
func profileTopicsSpec() tabSpec {
	return tabSpec{
		Label:     "我的帖子",
		Path:      "/v1/me/topics",
		ListKey:   "auto",
		NeedsAuth: true,
		Paging:    pagingOff,
		Kind:      kindTopic,
	}
}

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

func (c *Client) fetchProfileTopics(ctx context.Context) (pageResult, error) {
	return c.fetch(ctx, profileTopicsSpec(), 1)
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

// profileSignText 是一行签到状态，措辞与金币页保持一致。
func profileSignText(sign coinSignStatus, errText string) string {
	switch {
	case errText != "":
		return "签到状态暂不可用"
	case sign.Known && sign.Signed:
		if sign.Streak > 0 {
			return fmt.Sprintf("今日已签到 · 连续 %d 天", sign.Streak)
		}
		return "今日已签到"
	case sign.Known:
		return "今日未签到"
	}
	return "签到状态加载中…"
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
	if !ps.coinLoaded && !ps.coinLoading {
		a.loadProfileCoins()
	}
	switch ps.tab {
	case profileTabOverview:
		if ps.txOpen && !ps.txLoaded && !ps.txLoading {
			a.loadProfileTransactions()
		}
	case profileTabTopics:
		if !ps.topicLoaded && !ps.topicLoading {
			a.loadProfileTopics()
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

func (a *app) loadProfileCoins() {
	ps := a.profile
	if ps == nil || ps.coinLoading {
		return
	}
	ps.coinSeq++
	seq := ps.coinSeq
	ps.coinLoading = true
	ps.coinErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		balance, balanceErr := a.client.fetchCoinBalance(ctx)
		sign, signErr := a.client.fetchCoinSign(ctx)
		a.publish(func() {
			if a.profile != ps || ps.coinSeq != seq {
				return
			}
			ps.coinLoading = false
			ps.coinLoaded = true
			switch {
			case balanceErr == nil:
				ps.balance, ps.balanceOK, ps.coinErr = balance, true, ""
			default:
				ps.coinErr = friendlyError(balanceErr)
			}
			if signErr == nil {
				ps.sign = sign
			}
		})
	}()
}

func (a *app) loadProfileTransactions() {
	ps := a.profile
	if ps == nil || ps.txLoading {
		return
	}
	ps.txSeq++
	seq := ps.txSeq
	ps.txLoading = true
	ps.txErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchCoinTransactions(ctx, 1, "", "")
		a.publish(func() {
			if a.profile != ps || ps.txSeq != seq {
				return
			}
			ps.txLoading = false
			ps.txLoaded = true
			if err != nil {
				ps.txErr = friendlyError(err)
				return
			}
			items := res.Items
			if len(items) > profileTxShown {
				items = items[:profileTxShown]
			}
			ps.txItems = items
			ps.txErr = ""
			a.prefetchCoinTransactions(items)
		})
	}()
}

func (a *app) loadProfileTopics() {
	ps := a.profile
	if ps == nil || ps.topicLoading {
		return
	}
	ps.topicSeq++
	seq := ps.topicSeq
	ps.topicLoading = true
	ps.topicErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchProfileTopics(ctx)
		a.publish(func() {
			if a.profile != ps || ps.topicSeq != seq {
				return
			}
			ps.topicLoading = false
			ps.topicLoaded = true
			if err != nil {
				ps.topicErr = friendlyError(err)
				return
			}
			items := res.Items
			if len(items) > profileListLimit {
				items = items[:profileListLimit]
			}
			ps.topics = items
			ps.topicErr = ""
			ps.topicList.ScrollTo(0, ui.Start)
			a.prefetchItemAvatars(items)
		})
	}()
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

// reloadProfile 是窗口里的「刷新」：保留当前标签页与明细的展开状态，其余
// 状态清空后按需重拉。
func (a *app) reloadProfile() {
	ps := a.profile
	if ps == nil {
		return
	}
	tab, txOpen := ps.tab, ps.txOpen
	*a.profile = *newProfileState()
	a.profile.tab, a.profile.txOpen = tab, txOpen
	a.ensureProfileLoaded()
}
