package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 「用户资料」窗口的数据层与界面状态。窗口本身在 user_card_ui.go：点任何
// 一处的统一用户标识（头像 + 昵称）打开，入口见 user_ui.go 的 userTag。
//
// 五组只读端点拼出一张完整的资料卡：
//
//	资料    GET /v1/users/:userName
//	介绍    GET /v1/users/:userName/about   （匿名可读，正文是 Markdown）
//	主题    GET /v1/users/:userName/topics
//	回复    GET /v1/users/:userName/replies
//	徽章    GET /v1/users/:userName/badges 与 /badges/equipped
//
// 关注是这里唯一的互动写操作，走 follow.go 的同一个提交路径（scope interact）；
// 本人还可以编辑介绍（POST /v1/me/about，见 about_edit.go）。
//
// 简介正文在单独的 about 端点，字段与实测说明见下面 userAbout 的注释；
// 资料里的 has_readme 只说明有没有。资料卡同样没有「获赞」这类聚合数字，
// 统计只展示接口真给了的字段。

// 窗口的默认尺寸。和通知、我的两个窗口一样用 StateKey 记住位置与大小，
// 这里只是首次打开时的默认值。
const (
	userCardWindowWidth  = 480
	userCardWindowHeight = 640
	userCardMinWidth     = 380
	userCardMinHeight    = 420

	// userCardPageSize 是两个列表每页的条数：两个端点都默认 50、上限 50。
	userCardPageSize = 50
	// userCardTopicCollAll 是主题页合集下拉的「全部合集」哨兵，选中它等于
	// 不筛选。合集名最长 30 字，不会和真实名字撞上。
	userCardTopicCollAll = "全部合集"
	// userCardAvatar 是资料头里头像的边长。资料头不再用整条横幅，头像稍小
	// 一号，和「我的」窗口的 48、列表行的 18 处在同一档，头部更紧凑。
	userCardAvatar = 56
	// userCardBioFontSize 是「个人简介」正文的字号：比昵称小一号，与统计
	// 块的字号对齐。
	userCardBioFontSize float32 = 12
	// userCardBioMaxHeight 是「个人简介」内容区的高度上限（DIP）。介绍可以
	// 写很长，超过就在块内滚动，后面的统计、标签页与列表不会被挤出窗口。
	userCardBioMaxHeight float32 = 132
	// userCardBioMinHeight 是窗口很矮时介绍内容区仍保留的高度：至少还能看到
	// 两三行，而不是整块消失。
	userCardBioMinHeight float32 = 48
	// userCardBioReserved 是窗口里除介绍内容区以外大致要占的高度（标题栏、
	// 资料头、统计、标签页、列表首行与分隔线）。介绍区取剩余高度，并夹在
	// 上面两个上下限之间；这只是估计，块上的 MinHeight(0) 兜住估偏的情况。
	userCardBioReserved float32 = 380
)

// 资料卡里的五个标签页。前两个是列表（各自对应一个端点），第三个是站点
// 六项属性的雷达图（数据来自 about 响应里的 achievement_count），第四个是
// 这个人的徽章墙，第五个是他的公开合集（点开一个就换成合集里的帖子，见
// collection_browse.go）。
const (
	userCardTabTopics = iota
	userCardTabReplies
	userCardTabAttrs
	userCardTabBadges
	userCardTabCollections
)

var userCardTabLabels = []string{"他发布的主题", "他的最新回复", "属性雷达", "他的徽章", "他的合集"}

// userCardListKind 区分资料卡里的三个列表：它们只有端点和行渲染不同。
type userCardListKind int

const (
	userCardTopics userCardListKind = iota
	userCardReplies
	userCardCollections
)

// userCardSpec 是「某人发布的主题 / 评论 / 公开合集」的端点。三者都要
// username 而不是「我」，因此昵称必须转义，避免特殊字符改变请求路径。
func userCardSpec(name string, kind userCardListKind, page int) tabSpec {
	switch kind {
	case userCardReplies:
		return tabSpec{
			Label:   userCardTabLabels[userCardTabReplies],
			Path:    "/v1/users/" + url.PathEscape(name) + "/replies",
			Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(userCardPageSize),
			ListKey: "items",
			Paging:  pagingPages,
			Kind:    kindComment,
		}
	case userCardCollections:
		// 公开合集整页返回，文档没有分页参数；列表字段名同样没约定，用 auto
		// 兼容裸数组与 collections 包装。
		return tabSpec{
			Label:   userCardTabLabels[userCardTabCollections],
			Path:    userCollectionsPath(name),
			ListKey: "auto",
			Paging:  pagingOff,
			Kind:    kindCollection,
		}
	}
	return userCardTopicsSpec(name, page)
}

// userCardTopicsSpec 是某人发布的主题的未筛选端点：GET /v1/users/:name/topics，
// 分页、列表字段 posts。编辑器「插入帖子」用的 /v1/me/topics 只有 20 条、
// 没有回复数，不是这里的列表端点（见 me_topics.go）。
func userCardTopicsSpec(name string, page int) tabSpec {
	return tabSpec{
		Label:   userCardTabLabels[userCardTabTopics],
		Path:    "/v1/users/" + url.PathEscape(name) + "/topics",
		Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(userCardPageSize),
		ListKey: "posts",
		Paging:  pagingPages,
		Kind:    kindTopic,
	}
}

// userCardTopicCollSpec 是按合集名筛选某人主题的端点。筛选参数 collection
// 只在公开用户主题端点上有；未筛选的列表也用它（userCardTopicsSpec）。
// 用户名走路径转义，合集名进 query 用 QueryEscape，带空格 / 中文 / & 的
// 名字不能改变参数边界。
func userCardTopicCollSpec(name, collection string, page int) tabSpec {
	return tabSpec{
		Label:   "按合集筛选的帖子",
		Path:    "/v1/users/" + url.PathEscape(name) + "/topics",
		Query:   "page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(userCardPageSize) + "&collection=" + url.QueryEscape(collection),
		ListKey: "posts",
		Paging:  pagingPages,
		Kind:    kindTopic,
	}
}

// userAboutPath 是个人介绍端点。昵称同样要转义，规则与 userPath 一致。
func userAboutPath(username string) string {
	return userPath(username) + "/about"
}

// userBadgesPath 是他人徽章墙端点。路径参数接受 username 或 uuid；资料卡里
// 只有 username，照常转义，避免特殊字符改变请求路径。
func userBadgesPath(username string) string {
	return userPath(username) + "/badges"
}

// userBadgesEquippedPath 是他人当前佩戴的徽章；与徽章墙一起用，用来在墙上
// 标出哪几枚正戴着。
func userBadgesEquippedPath(username string) string {
	return userBadgesPath(username) + "/equipped"
}

// userAbout 是 GET /v1/users/:userName/about 的解码结果。
//
// 这个端点还不在开放 API 文档（v1.1）的端点总表里，字段按线上响应实测：
//
//	content            介绍正文，Markdown；没有填写时是 null
//	is_public          介绍是否公开
//	updated_at         最后更新时间（ISO 8601，UTC）
//	emojiMap           :code: → 图片地址，与帖子自己的 emojis 表同形
//	achievement_count  统计数据，键是小写（coins、tip_out_coins…），值是字符串
//
// 没填写介绍时端点返回 404 not_found「介绍不存在」，拿到的是空正文而不是
// 错误；constantsLabel 只是这些统计项在编辑器里的显示名，渲染用不到，按
// 「未知字段忽略」的约定不解析。
//
// 正文里的 {{KEY}} 是统计占位符：站点渲染前用 achievement_count 的值替换，
// 这里同样替换，否则会原样显示成「金币：{{COINS}}」。
type userAbout struct {
	Content string
	// IsPublic / PublicKnown 分开存：响应没带 is_public 时不能当成「不公开」。
	IsPublic    bool
	PublicKnown bool
	UpdatedAt   string
	// Emojis 是正文里用到的表情短码 → 图片地址；可能为空。
	Emojis map[string]string
	// Stats 是 {{KEY}} 的取值表，键统一小写，正文里的大写占位符也能命中。
	Stats map[string]string
}

// fetchUserAbout 读个人介绍。读端点匿名可用，但登录后仍照常带令牌：
// 响应可能按查看者个性化（例如自己的介绍不公开也读得到）。
func (c *Client) fetchUserAbout(ctx context.Context, name string) (userAbout, error) {
	if name == "" {
		return userAbout{}, errors.New("还不知道要读谁的个人介绍")
	}
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(userAboutPath(name)), token)
	})
	if err != nil {
		// 没有介绍（或用户不存在）时服务端是 404 not_found；这不是失败，
		// 资料卡按「没有填写」展示。其它错误才交给简介一块说明。
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return userAbout{}, nil
		}
		return userAbout{}, err
	}
	return decodeUserAbout(d), nil
}

func decodeUserAbout(d json.RawMessage) userAbout {
	o := asObject(d)
	if o == nil {
		return userAbout{}
	}
	return userAbout{
		Content:     o.str("content"),
		IsPublic:    o.flag("is_public"),
		PublicKnown: o.has("is_public"),
		UpdatedAt:   o.str("updated_at"),
		Emojis:      decodeEmojiURLs(o["emojiMap"]),
		Stats:       decodeAboutStats(o["achievement_count"]),
	}
}

// decodeAboutStats 把 achievement_count 收成 {{KEY}} 的取值表。键统一转小写，
// 值是字符串数字；将来改成数字也认。
func decodeAboutStats(raw json.RawMessage) map[string]string {
	o := asObject(raw)
	if o == nil {
		return nil
	}
	out := make(map[string]string, len(o))
	for k, v := range o {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if s != "" {
				out[strings.ToLower(k)] = s
			}
			continue
		}
		var n float64
		if json.Unmarshal(v, &n) == nil {
			out[strings.ToLower(k)] = strconv.FormatFloat(n, 'f', -1, 64)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// aboutPlaceholder 匹配介绍正文里的统计占位符，例如 {{COINS}}。
var aboutPlaceholder = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// expandAboutStats 把 {{KEY}} 换成统计值。键不区分大小写：正文写
// {{COINS}}，响应里的 achievement_count 是 coins。表里没有的占位符原样
// 保留 —— 宁可让用户看到一个占位符，也不把他写的内容悄悄删掉。
func expandAboutStats(src string, stats map[string]string) string {
	if src == "" || len(stats) == 0 {
		return src
	}
	return aboutPlaceholder.ReplaceAllStringFunc(src, func(m string) string {
		key := strings.ToLower(m[2 : len(m)-2])
		if v, ok := stats[key]; ok && v != "" {
			return v
		}
		return m
	})
}

// fetchUserBadges 读某人的完整徽章墙。徽章墙匿名可读；本人访问（带令牌且
// 是自己）会多返回 custom_fields 里的隐私条目，所以登录后照常带令牌。
func (c *Client) fetchUserBadges(ctx context.Context, username string) ([]badgeWallEntry, error) {
	if username == "" {
		return nil, errors.New("还不知道要读谁的徽章")
	}
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(userBadgesPath(username)), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeUserBadges(d), nil
}

// fetchUserEquippedBadgeIDs 读某人当前佩戴的徽章 id 集合。它与徽章墙是两条
// 端点：墙给全部徽章，这里只给正戴着的，界面据此把卡片标成「佩戴中」。
func (c *Client) fetchUserEquippedBadgeIDs(ctx context.Context, username string) (map[string]bool, error) {
	if username == "" {
		return nil, errors.New("还不知道要读谁的佩戴徽章")
	}
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(userBadgesEquippedPath(username)), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeEquippedBadgeIDs(d), nil
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

// userCardBadges 是他人徽章墙的加载状态。徽章墙与佩戴状态是两条端点，但
// 界面只展示一份结果，所以合在一个状态里，用同一个 seq 丢弃过期结果。
type userCardBadges struct {
	seq     uint64
	loading bool
	loaded  bool
	err     string
	entries []badgeWallEntry
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

	// 个人介绍正文：资料里只有 has_readme，正文要单独读，和资料同时发起。
	aboutSeq     uint64
	aboutLoading bool
	aboutLoaded  bool
	aboutErr     string
	about        userAbout
	// 编辑个人介绍（仅本人）：草稿、公开开关与提交状态。保存会消耗 500
	// 金币，审核通过后异步扣除，见 about_edit.go。
	aboutEditOpen bool
	aboutDraft    string
	aboutPublic   bool
	aboutBusy     bool
	aboutErrEdit  string
	aboutKey      string
	aboutSig      string
	aboutGen      uint64

	tab     int
	topics  userCardList
	replies userCardList
	// 主题页上方的合集下拉：选项从 GET /v1/users/:name/collections 读，
	// topicColl 是已生效的合集名（空 = 不筛选），与未筛选的 topics 各用
	// 一份列表状态；清掉筛选就回到原列表。topicCollDraft 是下拉当前值，
	// 分开存是为了「全部合集」哨兵在框里有可显示的值。
	topicColl      string
	topicCollDraft string
	topicCollSt    userCardList
	collNames      collNamesState
	// collections 是这个人的公开合集；只有切到「他的合集」才拉一次。
	collections userCardList
	// badges 是这个人的完整徽章墙；只有切到「他的徽章」才拉。
	badges userCardBadges
	// collBrowse 非空表示点开了其中一个合集，正在看它里面的帖子。
	collBrowse *collectionBrowse

	// follow 是当前登录用户对该用户的关注状态，沿用帖子详情的同一套字段。
	follow followState
}

func newUserCardState(username string) *userCardState {
	return &userCardState{username: username}
}

// list 返回某一类列表的状态，供加载与渲染共用。
func (cs *userCardState) list(kind userCardListKind) *userCardList {
	switch kind {
	case userCardReplies:
		return &cs.replies
	case userCardCollections:
		return &cs.collections
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
	// 简介正文与资料分开读：资料只说明有没有填写，正文在 about 端点。
	// 没填写时那个端点返回 404，按空介绍处理，不弹错误。
	if !cs.aboutLoaded && !cs.aboutLoading {
		a.loadUserCardAbout()
	}
	// 两个标签页在打开时各拉一页：统计里的主题数与回复数要立刻有数，
	// 切标签也不用等。合集是另一条端点（GET /v1/users/:name/collections），
	// 只有切到那个标签页才发请求。
	for _, kind := range []userCardListKind{userCardTopics, userCardReplies} {
		l, _ := userCardActiveList(cs, kind)
		if !l.loaded && !l.loading {
			a.loadUserCardActiveList(kind, 1)
		}
	}
	// 主题页的合集下拉选项与主题列表一起拉；失败按退避重试。
	if cs.tab == userCardTabTopics {
		a.ensureUserCardCollNames()
	}
	if cs.tab == userCardTabCollections {
		if l := cs.collections; !l.loaded && !l.loading {
			a.loadUserCardList(userCardCollections, 1)
		}
	}
	if cs.tab == userCardTabBadges && !cs.badges.loaded && !cs.badges.loading {
		a.loadUserCardBadges()
	}
}

// userCardActiveList 返回主题页当前该显示的列表与端点：应用了合集筛选就用
// 筛选结果那份状态和带 collection 的 spec，否则用未筛选的 topics。
func userCardActiveList(cs *userCardState, kind userCardListKind) (*userCardList, tabSpec) {
	if kind == userCardTopics && cs.topicColl != "" {
		return &cs.topicCollSt, userCardTopicCollSpec(cs.username, cs.topicColl, 1)
	}
	return cs.list(kind), userCardSpec(cs.username, kind, 1)
}

// loadUserCardActiveList 把加载派给当前该用的那份列表：续拉、重试与刷新都
// 走它，筛选状态下不会误拉回未筛选的 /v1/users/:name/topics。
func (a *app) loadUserCardActiveList(kind userCardListKind, page int) {
	cs := a.userCard
	if cs == nil {
		return
	}
	if kind == userCardTopics && cs.topicColl != "" {
		a.loadUserCardTopicColl(page)
		return
	}
	a.loadUserCardList(kind, page)
}

// ensureUserCardCollNames 在主题页确保合集下拉的选项已加载。可每帧调用：
// 未到重试时间的失败由 collNamesDue 挡掉，成功一次后不再请求。
func (a *app) ensureUserCardCollNames() {
	cs := a.userCard
	if cs == nil || !a.autoLoad || cs.username == "" {
		return
	}
	st := &cs.collNames
	if !st.collNamesDue(time.Now()) {
		return
	}
	st.loading = true
	st.triedAt = time.Now()
	st.seq++
	seq := st.seq
	name := cs.username
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchUserCollections(ctx, name)
		a.publish(func() {
			if a.userCard != cs || cs.username != name || st.seq != seq {
				return
			}
			st.loading = false
			if err != nil {
				st.failures++
				st.triedAt = time.Now()
				a.handleAuthErrorFor(toastUserCard, err, func() {
					st.triedAt = time.Time{}
					a.ensureUserCardCollNames()
				})
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
			// 选中的合集已经改名或删掉：清掉筛选，别留一个搜不到结果的名字。
			if cs.topicColl != "" && !containsString(st.names, cs.topicColl) {
				a.applyUserCardTopicColl(userCardTopicCollAll)
			}
		})
	}()
}

// loadUserCardTopicColl 拉按合集筛选结果的第 page 页，规则与
// loadUserCardList 一致：首页重建、续拉按 ID 去重、失败只记 moreErr。
func (a *app) loadUserCardTopicColl(page int) {
	cs := a.userCard
	if cs == nil || cs.username == "" || cs.topicColl == "" || page < 1 {
		return
	}
	l := &cs.topicCollSt
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
	name, collection := cs.username, cs.topicColl
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetch(ctx, userCardTopicCollSpec(name, collection, page), page)
		a.publish(func() {
			if a.userCard != cs || cs.username != name || cs.topicColl != collection || l.seq != seq {
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

// applyUserCardTopicColl 应用或清除合集筛选：换了名字就丢掉旧结果从第 1 页
// 重拉；选「全部合集」只清状态、不发请求，原来的主题列表还在。
func (a *app) applyUserCardTopicColl(collection string) {
	collection = strings.TrimSpace(collection)
	if collection == userCardTopicCollAll {
		collection = ""
	}
	cs := a.userCard
	if cs == nil || collection == cs.topicColl {
		return
	}
	cs.topicColl = collection
	if collection == "" {
		cs.topicCollDraft = userCardTopicCollAll
	} else {
		cs.topicCollDraft = collection
	}
	l := &cs.topicCollSt
	l.seq++
	l.loaded = false
	l.items = nil
	l.loading = false
	l.noMore = false
	l.err = ""
	l.moreErr = ""
	l.page = 1
	l.list.ScrollTo(0, ui.Start)
	if collection != "" {
		a.loadUserCardTopicColl(1)
	}
}

// reloadUserCardTopicColl 是筛选结果上的「刷新」：保留合集名，回到第 1 页。
func (a *app) reloadUserCardTopicColl() {
	cs := a.userCard
	if cs == nil || cs.topicColl == "" || cs.topicCollSt.loading {
		return
	}
	l := &cs.topicCollSt
	l.seq++
	l.loaded = false
	l.items = nil
	l.err = ""
	l.moreErr = ""
	l.noMore = false
	l.list.ScrollTo(0, ui.Start)
	a.loadUserCardTopicColl(1)
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

// loadUserCardAbout 读个人介绍正文。它与资料、两个列表并行，互不阻塞：
// 资料失败时介绍照样能显示，介绍失败也不影响资料与列表。
func (a *app) loadUserCardAbout() {
	cs := a.userCard
	if cs == nil || cs.username == "" || cs.aboutLoading {
		return
	}
	cs.aboutSeq++
	seq := cs.aboutSeq
	cs.aboutLoading = true
	cs.aboutErr = ""
	// 介绍正文可能带 :code: 表情；表情表整个应用只拉一次，没拉到就原样显示
	// 短码（与正文、评论一致）。
	a.ensureEmojis()
	name := cs.username
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		about, err := a.client.fetchUserAbout(ctx, name)
		a.publish(func() {
			if a.userCard != cs || cs.username != name || cs.aboutSeq != seq {
				return
			}
			cs.aboutLoading = false
			cs.aboutLoaded = true
			if err != nil {
				cs.aboutErr = friendlyError(err)
				return
			}
			cs.about = about
			cs.aboutErr = ""
		})
	}()
}

// loadUserCardBadges 读他人的徽章墙，并把佩戴状态标回墙上的条目。两个请求
// 顺序发出：徽章墙是主体，佩戴状态只决定卡片上要不要标「佩戴中」。佩戴状态
// 读失败时仍展示徽章墙，不因为一条辅助请求把整块变成错误。
func (a *app) loadUserCardBadges() {
	cs := a.userCard
	if cs == nil || cs.username == "" || cs.badges.loading {
		return
	}
	cs.badges.seq++
	seq := cs.badges.seq
	cs.badges.loading = true
	cs.badges.err = ""
	name := cs.username
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		badges, err := a.client.fetchUserBadges(ctx, name)
		if err == nil {
			if equipped, eqErr := a.client.fetchUserEquippedBadgeIDs(ctx, name); eqErr == nil {
				for i := range badges {
					badges[i].Equipped = equipped[badges[i].BadgeID]
				}
			}
		}
		a.publish(func() {
			if a.userCard != cs || cs.username != name || cs.badges.seq != seq {
				return
			}
			cs.badges.loading = false
			cs.badges.loaded = true
			if err != nil {
				cs.badges.err = friendlyError(err)
				return
			}
			cs.badges.entries = badges
			cs.badges.err = ""
			a.prefetchBadgeIcons(badges)
		})
	}()
}

// reloadUserCardBadges 丢掉已经拉到的徽章墙，重新读（徽章页的「刷新」）。
func (a *app) reloadUserCardBadges() {
	cs := a.userCard
	if cs == nil || cs.badges.loading {
		return
	}
	cs.badges.loaded = false
	cs.badges.err = ""
	cs.badges.entries = nil
	a.loadUserCardBadges()
}

// seedUserCardFollow 用资料里的 is_following 初始化关注按钮：资料响应本身
// 就带了这个字段，不必再单独请求一次用户主页。
func (a *app) seedUserCardFollow() {
	cs := a.userCard
	if cs == nil || !a.auth.loggedIn() || cs.follow.busy {
		return
	}
	if id := cs.user.ID; id != "" {
		cs.follow.userID = id
	}
	cs.follow.viewer = a.auth.username()
	if cs.user.FollowKnown {
		cs.follow.known = true
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
   关注：提交路径在 follow.go
--------------------------------------------------------------- */

// toggleUserCardFollow 关注或取消关注资料卡上的这位用户。未登录时先走全局
// 授权弹窗，授权成功后同一次操作自动重跑；缺 scope 的 403 也一样。
func (a *app) toggleUserCardFollow(follow bool) {
	cs := a.userCard
	if cs == nil {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastUserCard, "登录后才能关注这位用户。", func() {
			if a.userCard == cs {
				a.toggleUserCardFollow(follow)
			}
		})
		return
	}
	if cs.follow.userID == "" {
		cs.follow.userID = cs.user.ID
	}
	a.submitFollow(&cs.follow, follow, toastUserCard,
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
	cs.aboutSeq++
	cs.aboutLoaded = false
	cs.aboutLoading = false // 在途请求的 seq 已过期，结果会被丢弃
	cs.aboutErr = ""
	cs.about = userAbout{}
	cs.aboutEditOpen = false
	cs.aboutBusy = false
	cs.aboutErrEdit = ""
	cs.aboutKey = ""
	cs.aboutSig = ""
	cs.aboutGen++
	cs.badges.seq++
	cs.badges.loaded = false
	cs.badges.loading = false // 在途请求的 seq 已过期，结果会被丢弃
	cs.badges.err = ""
	cs.badges.entries = nil
	a.ensureUserCardLoaded()
	a.redrawUserCardWindow()
}
