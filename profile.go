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
// 六个标签页对应这几组端点：
//
//	概览        GET /v1/users/:userName（最近统计与六维属性）
//	个人简介    GET /v1/users/:userName/about（正文；本人可编辑，见 about_edit.go）
//	我的内容    GET /v1/users/:userName/topics、/v1/me/favorites、/v1/me/bookmarks、/v1/me/collections
//	我的回复    GET /v1/users/:userName/replies
//	徽章        GET /v1/me/badges、/v1/me/badges/equipped；佩戴 / 取消佩戴（见 badges.go）
//	权限与账号  GET /v1/users/:userName + 令牌里的 scope
//
// 合集除了读，还能在这里改名（PATCH /v1/me/collections/:id）与删除
// （DELETE /v1/me/collections/:id），点开一个合集看它里面的帖子（见
// collection_browse.go 的 GET /v1/collections/:id/topics）。
//
// 金币不在这里：金币页是独立的金币窗口（见 coins_window.go），入口是标题栏
// 上登录账号左侧的金币数。本窗口不展示余额、签到，也不提供金币相关的入口。
//
// 「我的内容」一页里有四个分段：帖子、收藏、书签、合集。这四类原先占着主
// 窗口的侧边栏，现在只在这里出现。
//
// 这里的写操作是「徽章」里的佩戴切换（scope interact 或 write:economy，不扣金币）
// 与「个人简介」的编辑（POST /v1/me/about，每次 500 金币，见 about_edit.go）；
// 发帖、互动等其它操作仍在各自的窗口或站点完成。

// 弹窗的默认尺寸。它和通知窗口一样用 StateKey 记住位置与大小，这里只是
// 首次打开时的默认值。
const (
	profileWindowWidth  = 520
	profileWindowHeight = 640
	profileWinMinWidth  = 430
	profileWinMinHeight = 480

	// profileReplyPageSize 是「我的回复」每页条数：该端点默认 50、上限 50。
	profileReplyPageSize = 50
	// profileTopicCollLimit 是按合集名筛选「我的帖子」时每页条数：这条端点
	// 与用户主题的其它列表一致，默认 50、上限 50。
	profileTopicCollLimit = 50
	// profileTopicCollAll 是下拉框里的「全部合集」哨兵：选中它等于不筛选。
	// 合集名本身 ≤30 字，这个值不会和真实合集撞上。
	profileTopicCollAll = "全部合集"
)

// 弹窗里的标签页。金币页不在这个窗口里，去独立的金币窗口看。
const (
	profileTabOverview = iota
	profileTabAbout
	profileTabContent
	profileTabReplies
	profileTabBadges
	profileTabAccount
	profileTabFilter
)

var profileTabLabels = []string{"概览", "个人简介", "我的内容", "我的回复", "徽章", "权限与账号", "内容屏蔽"}

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

// profileContentSpecs 是「我的内容」四个分段里三个固定端点。帖子分段要拼
// 当前账号的用户名、还支持按合集名筛选，走 profileTopicsSpec /
// profileTopicCollSpec，由 profileSpecFor 按当前状态选用。
var profileContentSpecs = []tabSpec{
	{}, // profileContentTopics：动态端点，见 profileTopicsSpec
	{Label: "我的收藏", Path: "/v1/me/favorites", Query: "page=1&limit=50", ListKey: "posts", NeedsAuth: true, Paging: pagingPages, Kind: kindTopic},
	// 书签接口上限已放宽到 100；收藏仍是 50。
	{Label: "我的书签", Path: "/v1/me/bookmarks", Query: "page=1&limit=100", ListKey: "items", NeedsAuth: true, Paging: pagingPages, Kind: kindComment},
	{Label: "我的合集", Path: "/v1/me/collections", NeedsAuth: true, Paging: pagingOff, Kind: kindCollection},
}

// profileTopicsSpec 是「我的帖子」的端点契约：走公开用户主题列表
// GET /v1/users/:userName/topics，分页、列表字段 posts，带完整字段
// （comment_count / latest_commented_at / is_user_pinned）。
// 编辑器「插入帖子」选择器用的 /v1/me/topics 只有 20 条、没有回复数，
// 不是这里的列表端点（见 me_topics.go）。用户名走路径转义。
func profileTopicsSpec(name string, page int) tabSpec {
	return tabSpec{
		Label:     "我的帖子",
		Path:      "/v1/users/" + url.PathEscape(name) + "/topics",
		Query:     "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(profileTopicCollLimit),
		ListKey:   "posts",
		NeedsAuth: true,
		Paging:    pagingPages,
		Kind:      kindTopic,
	}
}

// profileSpecFor 返回「我的内容」某个分段在给定状态下真正该用的端点。
// 帖子一旦应用了合集筛选，就换成 profileTopicCollSpec（由调用方处理），
// 这里给出的是未筛选的列表端点。
func (a *app) profileSpecFor(ps *profileState, kind, page int) tabSpec {
	if kind == profileContentTopics {
		return profileTopicsSpec(a.topicCollName(ps), page)
	}
	return profileContentSpecs[kind]
}

// profileNow 单独提出来给测试替换：注册天数按本地时间算。
var profileNow = func() time.Time { return time.Now() }

// profileScopeLabel 把授权范围翻成一句人话（映射表见 login_window.go 的
// scopeLabels，登录窗口的「本次申请」用的是同一份）。未列出的范围原样展示。
func profileScopeLabel(scope string) string {
	if label := scopeLabel(scope); label != "" {
		return label
	}
	return scope
}

// accountProfile 是 GET /v1/users/:userName 里窗口用得到的字段。接口不返回
// 邮箱、金币与简介正文，所以这里也只有公开资料；简介正文在单独的 about
// 端点（见 user_card.go 的 userAbout）。
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

	// Attributes 是 GET /v1/users/:userName 的 user_attributes 六维属性；
	// AttributesKnown 区分「没有这个字段」与「六项都是 0」，后者仍然画图。
	Attributes      []profileAttribute
	AttributesKnown bool

	// 以下字段「我的」窗口不展示，用户资料卡（user_card.go）要用：
	// ID 是关注端点需要的用户 uuid（用户名只用于展示与链接），Role 是站点
	// 角色，Badges 是佩戴中的徽章，HasReadme 只说明有没有填写个人简介 ——
	// 正文要另读 GET /v1/users/:userName/about。
	ID        string
	Role      string
	Badges    []badgeInfo
	HasReadme bool
	// IsFollowing 表示当前登录用户是否已关注他；FollowKnown 说明响应里到底
	// 带没带这个字段 —— 匿名响应不带，不能当成「未关注」。
	IsFollowing bool
	FollowKnown bool
}

// roleGuardian 是站点角色里的「守护者」。守护者和 Pro 会员一样有 2Libra
// 官方图床的上传权限，每天 20 张的配额由服务端控制，客户端不另计数。
const roleGuardian = "guardian"

// canUseOfficialHost 判断这个账号能不能用 2Libra 官方图床：Pro 会员，或
// 站点角色是守护者（role=guardian），两者任一即可。role 与 is_pro 一样来自
// /v1/me 与公开资料共用的解码（见 decodeAccountProfile）。
func (p accountProfile) canUseOfficialHost() bool {
	return p.IsPro || p.Role == roleGuardian
}

// profileState 是「我的」窗口的全部界面状态。只有 UI 线程读写；后台请求
// 完成后通过 publish 回填，并用每类数据的 seq 丢弃过期结果。
type profileState struct {
	tab int
	// tabSeen 记上一次真正展示过的标签：切进「内容屏蔽」时据此强制刷新名单。
	tabSeen int

	// 账号资料（用户名 / 昵称 / 用户 ID / 注册时间）
	infoSeq     uint64
	infoLoading bool
	infoLoaded  bool
	infoErr     string
	user        accountProfile

	// 头像：更换 / 重置入口在资料头的气泡里（见 avatar.go）。选好图片先进
	// 裁剪编辑器，确认后才上传并扣金币（见 avatar_crop.go）。
	avatarOpen bool
	avatarBusy bool
	avatarErr  string
	avatarCrop avatarCropState

	// 个人简介：正文在单独的 about 端点（见 user_card.go），切到那个标签页
	// 才拉一次。本人可以编辑，保存一次消耗 500 金币（见 about_edit.go）。
	aboutSeq     uint64
	aboutLoading bool
	aboutLoaded  bool
	aboutErr     string
	about        userAbout

	aboutEditOpen bool
	aboutDraft    string
	aboutPublic   bool
	aboutBusy     bool
	aboutErrEdit  string
	aboutKey      string
	aboutSig      string
	aboutGen      uint64
	// aboutTpl 是编辑框顶部「个人常量 / 展示组件」快捷模板的开合与选区状态。
	aboutTpl aboutTemplateState

	// 我的内容：帖子 / 收藏 / 书签 / 合集各有一份列表状态，切换分段时保留
	// 已经拉到的内容，不重新请求。加载走主窗口同一套通用加载（loadSpec）。
	contentKind int
	content     [profileContentCount]tabState

	// 帖子分段上方的合集下拉：选项从 GET /v1/me/collections 读，不手输。
	// collNames 存选项与加载状态（失败退避）；topicColl 是已经生效的合集名
	// （空 = 不筛选），与未筛选的 content[profileContentTopics] 各用一份状态，
	// 清掉筛选就回到原列表。topicCollDraft 是下拉当前选中的值（哨兵或合集名）：
	// 两者分开是为了让「全部合集」在下拉里有可显示的值，同时筛选状态仍用
	// 空串表示「不过滤」。
	topicColl      string
	topicCollDraft string
	topicCollSt    tabState
	collNames      collNamesState

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

	// 徽章墙：GET /v1/me/badges 与 /v1/me/badges/equipped 一起加载。
	badgeSeq     uint64
	badgeLoading bool
	badgeLoaded  bool
	badgeErr     string
	badges       []badgeWallEntry
	// badgeBusyID 是正在提交佩戴 / 取消佩戴的徽章 uuid；一次只提交一枚，
	// 避免连点造成重复扣币。
	badgeBusyID string
	// bookmarkBusy 是正在移除的书签评论 id（「我的内容 - 书签」里直接移除，
	// 见 bookmarks.go）；一次只提交一条。
	bookmarkBusy string

	// 合集（「我的内容 - 合集」）：
	//	collBrowse 非空表示点开了一个合集，正在看它里面的帖子；返回时置空。
	//	collEdit / collDraft 是改名对话框的状态（正在改的合集 id 与输入框内容），
	//	collDelete 是正在确认删除的合集 id，collBusy 是正在提交改名 / 删除的
	//	合集 id —— 一次只提交一个，避免连点。
	collBrowse *collectionBrowse
	collEdit   string
	collDraft  string
	collDelete string
	collBusy   string
}

func newProfileState() *profileState {
	return &profileState{}
}

// collNamesState 是合集下拉选项的加载状态。选项由视图每帧调用的 ensure 拉起，
// 所以失败必须退避：不然接口一挂，每帧都会重发注定失败的请求。
type collNamesState struct {
	loaded   bool
	loading  bool
	triedAt  time.Time
	failures int
	seq      uint64
	names    []string
}

// collNamesRetryAfter 是第 failures 次失败后的退避：10s / 20s / 40s，封顶 1 分钟。
func collNamesRetryAfter(failures int) time.Duration {
	delay := 10 * time.Second
	for i := 1; i < failures && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	return delay
}

// collNamesDue 报告现在能不能发起加载。成功后 loaded 为真就不再拉；失败后
// 在退避窗口内跳过，超时才重试。
func (st *collNamesState) collNamesDue(now time.Time) bool {
	if st.loaded || st.loading {
		return false
	}
	if st.triedAt.IsZero() {
		return true
	}
	return now.Sub(st.triedAt) >= collNamesRetryAfter(st.failures)
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

// profileTopicCollSpec 是按合集名筛选「我的帖子」的端点契约。筛选参数
// collection 只在公开用户主题端点上有；未筛选的列表也用它（见
// profileTopicsSpec），编辑器选择器才用 /v1/me/topics。合集名进 query 用
// QueryEscape，带空格 / 中文 / & 的名字不能改变参数边界。
func profileTopicCollSpec(name, collection string, page int) tabSpec {
	return tabSpec{
		Label:   "按合集筛选的帖子",
		Path:    "/v1/users/" + url.PathEscape(name) + "/topics",
		Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(profileTopicCollLimit) + "&collection=" + url.QueryEscape(collection),
		ListKey: "posts",
		Paging:  pagingPages,
		Kind:    kindTopic,
	}
}

// topicCollName 是当前账号在筛选端点路径里要用的用户名。令牌里的用户名优先，
// 资料尚未拉到时退回 /v1/me 里读到的；两者都没有就不发请求，避免拼出
// /v1/users//topics 这种会 404 的路径。
func (a *app) topicCollName(ps *profileState) string {
	if name := a.auth.username(); name != "" {
		return name
	}
	if ps != nil {
		return ps.user.Name
	}
	return ""
}

// ensureProfileCollNames 在帖子分段视图里确保合集下拉的选项已加载。可每帧调用：
// 未到重试时间的失败会被 collNamesDue 挡掉，成功一次后不再请求。
func (a *app) ensureProfileCollNames() {
	ps := a.profile
	if ps == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	st := &ps.collNames
	if !st.collNamesDue(time.Now()) {
		return
	}
	st.loading = true
	st.triedAt = time.Now()
	st.seq++
	seq := st.seq
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchMyCollections(ctx)
		a.publish(func() {
			if a.profile != ps || st.seq != seq {
				return
			}
			st.loading = false
			if err != nil {
				if !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				st.failures++
				st.triedAt = time.Now()
				// 补完授权后要立刻重跑，清掉刚记下的退避时间。
				a.handleAuthError(err)
				return
			}
			st.failures = 0
			st.loaded = true
			st.names = make([]string, 0, len(items))
			for _, it := range items {
				if it.Name != "" {
					st.names = append(st.names, it.Name)
				}
			}
			// 选中的合集已经不在列表里（改名或删掉）：清掉筛选，别留一个
			// 永远搜不到结果的名字在框里。
			if ps.topicColl != "" && !containsString(st.names, ps.topicColl) {
				a.applyProfileTopicColl(profileTopicCollAll)
			}
		})
	}()
}

// loadProfileTopicColl 拉筛选结果的第 page 页，走与四个分段相同的通用加载：
// 分页、去重、限流退避与授权失败后的重跑都复用 loadSpec 的逻辑。
func (a *app) loadProfileTopicColl(page int) {
	ps := a.profile
	if ps == nil || ps.topicColl == "" || page < 1 {
		return
	}
	name := a.topicCollName(ps)
	if name == "" {
		st := &ps.topicCollSt
		st.loading = false
		st.err = "还不知道当前账号的用户名"
		return
	}
	spec := profileTopicCollSpec(name, ps.topicColl, page)
	a.loadSpec(toastProfile, &ps.topicCollSt, spec, page, nil)
}

// applyProfileTopicColl 应用或清除合集筛选。合集名变了就把旧列表整份丢掉，
// 从第 1 页重拉；选「全部合集」时只清状态，不发请求——原来的「我的帖子」
// 还在，界面直接切回去。相同名字重复提交不发请求，免得刷接口配额。
func (a *app) applyProfileTopicColl(collection string) {
	collection = strings.TrimSpace(collection)
	if collection == profileTopicCollAll {
		collection = ""
	}
	ps := a.profile
	if ps == nil || collection == ps.topicColl {
		return
	}
	ps.topicColl = collection
	if collection == "" {
		ps.topicCollDraft = profileTopicCollAll
	} else {
		ps.topicCollDraft = collection
	}
	st := &ps.topicCollSt
	st.authWait = false
	st.loaded = false
	st.items = nil
	st.selected = nil
	st.loading = false
	st.noMore = false
	st.err = ""
	st.moreErr = ""
	st.page = 1
	st.list.ScrollTo(0, ui.Start)
	if collection != "" {
		a.loadProfileTopicColl(1)
	}
}

// reloadProfileTopicColl 是筛选结果上的「刷新」：保留筛选名，列表回到第 1 页。
func (a *app) reloadProfileTopicColl() {
	ps := a.profile
	if ps == nil || ps.topicColl == "" || ps.topicCollSt.loading {
		return
	}
	st := &ps.topicCollSt
	st.authWait = false
	st.loaded = false
	st.items = nil
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.loadProfileTopicColl(1)
}

// profileActiveContent 返回某个分段当前该显示的列表状态与端点：帖子分段一旦
// 应用了合集筛选，就用筛选结果那份状态和 /v1/users/:name/topics 的 spec。
func (a *app) profileActiveContent(ps *profileState, kind int) (*tabState, tabSpec) {
	if kind < 0 || kind >= profileContentCount {
		return nil, tabSpec{}
	}
	if ps != nil && kind == profileContentTopics && ps.topicColl != "" {
		return &ps.topicCollSt, profileTopicCollSpec(a.topicCollName(ps), ps.topicColl, 1)
	}
	return &ps.content[kind], a.profileSpecFor(ps, kind, 1)
}

// loadProfileActiveContent 把加载请求派给当前该用的那份列表。重试、续拉和
// 刷新都走它，筛选状态下就不会误拉回未筛选的 topic 列表。
func (a *app) loadProfileActiveContent(kind, page int) {
	ps := a.profile
	if ps == nil || kind < 0 || kind >= profileContentCount {
		return
	}
	if kind == profileContentTopics && ps.topicColl != "" {
		a.loadProfileTopicColl(page)
		return
	}
	a.loadProfileContent(kind, page)
}

func (a *app) reloadProfileActiveContent(kind int) {
	ps := a.profile
	if ps == nil || kind < 0 || kind >= profileContentCount {
		return
	}
	if kind == profileContentTopics && ps.topicColl != "" {
		a.reloadProfileTopicColl()
		return
	}
	a.reloadProfileContent(kind)
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

// fetchMyProfile 读「我的」窗口的本人资料。它不需要用户名，返回结构与
// GET /v1/users/:username 一致，但会带上 show_setting 这类本人字段；scope
// profile。失败时调用方可以退回公开资料，页面不至于空着。
func (c *Client) fetchMyProfile(ctx context.Context) (accountProfile, error) {
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL("/v1/me"), token)
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
	attrs, attrsKnown := decodeProfileAttributes(merged)
	return accountProfile{
		Name:            merged.str("username", "name"),
		DisplayName:     merged.str("display_name", "displayName", "nickname"),
		Avatar:          merged.str("avatar_url", "avatarUrl", "avatar"),
		UserNumber:      merged.num("user_number", "userNumber", "number"),
		CreatedAt:       merged.str("created_at", "createdAt"),
		Level:           merged.num("level"),
		IsPro:           merged.flag("is_pro", "isPro"),
		Followers:       merged.num("follower_count", "followers_count", "followers"),
		Following:       merged.num("following_count", "followings_count", "following"),
		HomeView:        merged.num("home_view", "homeView", "home_views"),
		Attributes:      attrs,
		AttributesKnown: attrsKnown,
		ID:              merged.str("id"),
		Role:            merged.str("role"),
		Badges:          decodeBadges(merged),
		HasReadme:       merged.flag("has_readme", "hasReadme"),
		IsFollowing:     merged.flag("is_following"),
		FollowKnown:     merged.has("is_following"),
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
	case profileTabAbout:
		if !ps.aboutLoaded && !ps.aboutLoading {
			a.loadProfileAbout()
		}
	case profileTabContent:
		st := &ps.content[ps.contentKind]
		// 「我的帖子」要拼当前账号的用户名；令牌里没有、资料也还没拉到时
		// 先不发请求，等资料回来后的下一帧再补上，避免拼出 /users//topics。
		ready := ps.contentKind != profileContentTopics || a.topicCollName(ps) != ""
		if ready && !st.loaded && !st.loading && !st.authWait {
			a.loadProfileContent(ps.contentKind, 1)
		}
		// 帖子分段的合集下拉选项：从「我的合集」端点读，失败按退避重试。
		if ps.contentKind == profileContentTopics {
			a.ensureProfileCollNames()
		}
	case profileTabReplies:
		if !ps.replyLoaded && !ps.replyLoading && name != "" {
			a.loadProfileReplies(name, 1)
		}
	case profileTabBadges:
		if !ps.badgeLoaded && !ps.badgeLoading {
			a.loadProfileBadges()
		}
	}
}

// loadProfileAbout 读本人的个人介绍正文。读端点匿名可用，这里照常带令牌：
// 自己的不公开介绍也能读到。失败后保留原因，页面上给重试入口。
func (a *app) loadProfileAbout() {
	ps := a.profile
	if ps == nil || !a.auth.loggedIn() || ps.aboutLoading {
		return
	}
	name := a.auth.username()
	if name == "" {
		return
	}
	ps.aboutSeq++
	seq := ps.aboutSeq
	ps.aboutLoading = true
	ps.aboutErr = ""
	a.ensureEmojis()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		about, err := a.client.fetchUserAbout(ctx, name)
		a.publish(func() {
			if a.profile != ps || ps.aboutSeq != seq {
				return
			}
			ps.aboutLoading = false
			ps.aboutLoaded = true
			if err != nil {
				ps.aboutErr = friendlyError(err)
				return
			}
			ps.about = about
			ps.aboutErr = ""
		})
	}()
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
		// 本人优先用 GET /v1/me（不需要用户名，字段更完整）；失败再退回公开
		// 资料端点，页面不能因为一次接口问题就整块空着。
		var p accountProfile
		var err error
		if a.auth.loggedIn() && strings.EqualFold(name, a.auth.username()) {
			p, err = a.client.fetchMyProfile(ctx)
			if err != nil {
				p, err = a.client.fetchAccountProfile(ctx, name)
			}
		} else {
			p, err = a.client.fetchAccountProfile(ctx, name)
		}
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
			// 「我的」窗口读到的资料最权威：顺手把 Pro 状态与角色写回令牌缓存，
			// 图库设置里官方图床的可用性跟着一起更新。
			if p.Name != "" && p.Name == a.auth.username() {
				a.auth.setOfficialHostAccess(p.IsPro, p.Role)
			}
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
	a.loadSpec(toastProfile, &ps.content[kind], a.profileSpecFor(ps, kind, page), page, nil)
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
