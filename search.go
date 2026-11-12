package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 搜索
//
//	GET /v1/search?q=&type=&page=&limit=   scope：—（type=favorites 需要 read:me）
//
// 这个端点的限流和其它读端点分开：匿名 5 次/分、登录 20 次/分。所以输入不能
// 每次改动都发请求：至少 2 个字、300ms 以上的防抖，翻页只对帖子 / 收藏生效
// （用户、节点、标签的响应没有分页对象，一次给全）。
//
// 响应按 type 不同而不同，文档只给了参数表，所以这里按线上实测的形状解析：
//   posts / favorites  { posts: [...], total, page, limit, total_pages }
//   users              [ { id, username, avatar_url, is_pro } ]
//   nodes              [ { id, name, slug, parent_slug, post_count } ]
//   tags               [ { id, name, post_count } ]

const (
	searchWindowWidth  = 620
	searchWindowHeight = 680
	searchWinMinWidth  = 460
	searchWinMinHeight = 420

	searchPageSize  = 20
	searchQueryMin  = 2
	searchQueryMax  = 100
	searchDebounce  = 320 * time.Millisecond
	searchSearching = "正在搜索…"
)

// searchDebounceDelay 提成变量是为了测试能把它缩短。
var searchDebounceDelay = searchDebounce

// searchKind 是搜索类型，顺序与搜索窗口里的分段控件一致。
type searchKind int

const (
	searchPosts searchKind = iota
	searchUsers
	searchNodes
	searchTags
	searchFavorites
)

var searchKindLabels = []string{"帖子", "用户", "节点", "标签", "收藏"}

// searchKindValues 是接口的 type 取值，顺序与上面一致。
var searchKindValues = []string{"posts", "users", "nodes", "tags", "favorites"}

func (k searchKind) value() string {
	if k < 0 || int(k) >= len(searchKindValues) {
		return searchKindValues[0]
	}
	return searchKindValues[k]
}

// searchHit 是一条搜索结果。帖子 / 收藏复用列表行（Item）；用户、节点、标签
// 是另一类对象，各自带展示字段，行渲染见 search_ui.go。
type searchHit struct {
	kind     searchKind
	item     Item
	username string
	avatar   string
	badges   []badgeInfo
	name     string
	parent   string
	count    int
	url      string
}

// searchState 是搜索窗口的全部界面状态。只在 UI 线程读写；请求结果通过
// app.publish 回来，用 gen / seq 丢弃过期结果。
type searchState struct {
	q    string
	kind int

	// lastQ / lastKind 是上一帧见到的输入：视图每帧比较一次，变了才排防抖。
	lastQ    string
	lastKind int
	gen      uint64 // 输入代次：输入一变，旧请求作废
	seq      uint64 // 请求序号：同一代次里只有最新一次结果生效
	loading  bool
	// firstLoading 表示正在拉第 1 页（换关键词 / 换类型）。已有结果时它只是
	// 列表上方的一行进度，不是整页转圈。
	firstLoading bool
	err          string
	// note 是输入不足、未登录等「没有发请求」的说明，与 err 分开。
	note string

	hits       []searchHit
	total      int
	page       int
	totalPages int
	noMore     bool
	loaded     bool
	list       ui.ListState
	input      ui.Handle

	timer *time.Timer
}

func newSearchState() *searchState {
	return &searchState{}
}

/* ------------------------------------------------------------------
   请求
-------------------------------------------------------------- */

func searchPath() string { return "/v1/search" }

func searchQuery(kind searchKind, q string, page int) string {
	v := url.Values{}
	v.Set("q", strings.TrimSpace(q))
	v.Set("type", kind.value())
	v.Set("page", strconv.Itoa(page))
	v.Set("limit", strconv.Itoa(searchPageSize))
	return v.Encode()
}

func (c *Client) search(ctx context.Context, kind searchKind, q string, page int) (searchResult, error) {
	u := c.apiURL(searchPath()) + "?" + searchQuery(kind, q, page)
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, u, token)
	})
	if err != nil {
		return searchResult{}, err
	}
	return decodeSearch(kind, d, page), nil
}

// searchResult 是归一后的一页结果。
type searchResult struct {
	hits       []searchHit
	total      int
	page       int
	totalPages int
}

// decodeSearch 按 type 解析响应：帖子 / 收藏是分页对象，其余是裸数组。
func decodeSearch(kind searchKind, d json.RawMessage, page int) searchResult {
	out := searchResult{page: page}
	switch kind {
	case searchUsers, searchNodes, searchTags:
		for _, raw := range rawArray(d) {
			o := asObject(raw)
			if o == nil {
				continue
			}
			hit := searchHit{kind: kind}
			switch kind {
			case searchUsers:
				hit.username = o.str("username")
				if hit.username == "" {
					continue
				}
				hit.avatar = authorAvatar(o)
				hit.badges = decodeBadges(o)
				hit.url = siteBaseURL + "/user/" + url.PathEscape(hit.username) + "/about"
			case searchNodes:
				hit.name = o.str("name")
				if hit.name == "" {
					continue
				}
				hit.parent = o.str("parent_slug")
				hit.count = o.num("post_count")
				slug := o.str("slug")
				hit.url = nodeSiteURL(hit.parent, slug)
			case searchTags:
				hit.name = o.str("name")
				if hit.name == "" {
					continue
				}
				hit.count = o.num("post_count")
				hit.url = siteBaseURL + "/tag/" + url.PathEscape(hit.name)
			}
			out.hits = append(out.hits, hit)
		}
		// 这三个类型没有分页对象：一次返回全部，不再续拉。
		out.total = len(out.hits)
		out.totalPages = 1
		return out
	}
	// 帖子 / 收藏：列表字段是 posts；个别版本可能包在 items 里。
	raws, meta, _ := extractList(d, "posts")
	if len(raws) == 0 {
		raws, meta, _ = extractList(d, "auto")
	}
	out.total = meta.Total
	out.page = meta.Page
	out.totalPages = meta.TotalPages
	for _, raw := range raws {
		it := itemFrom(raw, kindTopic)
		if it.Key == "" {
			continue
		}
		out.hits = append(out.hits, searchHit{kind: kind, item: it})
	}
	if out.total == 0 {
		out.total = len(out.hits)
	}
	if out.totalPages == 0 {
		out.totalPages = 1
	}
	return out
}

// nodeSiteURL 拼节点在站点上的地址：子节点是 /node/<父>/<子>，顶层是
// /node/<slug>。
func nodeSiteURL(parent, slug string) string {
	if slug == "" {
		return ""
	}
	if parent != "" && parent != slug {
		return siteBaseURL + "/node/" + url.PathEscape(parent) + "/" + url.PathEscape(slug)
	}
	return siteBaseURL + "/node/" + url.PathEscape(slug)
}

/* ------------------------------------------------------------------
   防抖与加载
-------------------------------------------------------------- */

// scheduleSearch 在输入或类型变化后重排队：先作废旧请求，再等防抖到期。
// 输入不足 2 个字时不发请求，只给出说明。
func (a *app) scheduleSearch(st *searchState) {
	if st == nil || a.search != st {
		return
	}
	st.gen++
	gen := st.gen
	st.err = ""
	st.note = ""
	query := strings.TrimSpace(st.q)
	if utf8.RuneCountInString(query) < searchQueryMin {
		st.loading = false
		st.hits = nil
		st.total = 0
		st.page = 0
		st.totalPages = 0
		st.noMore = false
		st.loaded = false
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		return
	}
	if !a.autoLoad {
		return // headless 测试自己驱动 runSearch
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(searchDebounceDelay, func() {
		a.publish(func() {
			if a.search != st || st.gen != gen {
				return
			}
			st.timer = nil
			a.runSearch(st, gen, 1)
		})
	})
}

// runSearch 发出一次搜索请求。page 为 1 时替换结果，否则追加。
func (a *app) runSearch(st *searchState, gen uint64, page int) {
	if st == nil || st.gen != gen || st.loading || page < 1 {
		return
	}
	query := strings.TrimSpace(st.q)
	if utf8.RuneCountInString(query) < searchQueryMin {
		return
	}
	if utf8.RuneCountInString(query) > searchQueryMax {
		st.note = "关键词最长 " + strconv.Itoa(searchQueryMax) + " 个字符"
		return
	}
	kind := searchKind(st.kind)
	if kind == searchFavorites && !a.auth.loggedIn() {
		return // 视图会给出登录入口
	}
	st.loading = true
	st.firstLoading = page == 1
	st.err = ""
	st.seq++
	seq := st.seq
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.search(ctx, kind, query, page)
		a.publish(func() {
			if a.search != st || st.gen != gen || st.seq != seq {
				return
			}
			st.loading = false
			st.firstLoading = false
			if err != nil {
				if a.handleAuthError(err, func() {
					a.runSearch(st, gen, page)
				}) {
					if a.onSearchDone != nil {
						a.onSearchDone(query, err)
					}
					return
				}
				if page > 1 && len(st.hits) > 0 {
					st.note = "加载下一页失败：" + friendlyError(err)
				} else {
					st.err = friendlyError(err)
				}
				if a.onSearchDone != nil {
					a.onSearchDone(query, err)
				}
				return
			}
			st.err = ""
			st.note = ""
			if page == 1 {
				st.hits = res.hits
				st.list.ScrollTo(0, ui.Start)
			} else {
				seen := make(map[string]bool, len(st.hits))
				for _, hit := range st.hits {
					seen[searchHitKey(hit)] = true
				}
				for _, hit := range res.hits {
					if key := searchHitKey(hit); !seen[key] {
						st.hits = append(st.hits, hit)
						seen[key] = true
					}
				}
			}
			st.total = res.total
			st.page = res.page
			if res.totalPages > 0 {
				st.totalPages = res.totalPages
			}
			st.noMore = len(res.hits) == 0
			st.loaded = true
			// 结果里的作者头像先预取，行渲染只读缓存。
			for _, hit := range res.hits {
				if hit.kind == searchUsers {
					a.prefetchAvatars(hit.avatar)
				}
				if hit.kind == searchPosts || hit.kind == searchFavorites {
					a.prefetchItemAvatars([]Item{hit.item})
				}
			}
			if a.onSearchDone != nil {
				a.onSearchDone(query, nil)
			}
		})
	}()
}

// searchHitKey 是结果行的去重键：帖子用 short_id，其余用名字。
func searchHitKey(hit searchHit) string {
	switch hit.kind {
	case searchPosts, searchFavorites:
		return "p:" + hit.item.Key
	case searchUsers:
		return "u:" + hit.username
	case searchNodes:
		return "n:" + hit.name
	default:
		return "t:" + hit.name
	}
}

// searchCanLoadMore 汇报是否还能续拉下一页：只有帖子 / 收藏分页。
func searchCanLoadMore(st *searchState) bool {
	if st == nil || !st.loaded || st.loading || st.noMore {
		return false
	}
	if searchKind(st.kind) != searchPosts && searchKind(st.kind) != searchFavorites {
		return false
	}
	return st.totalPages == 0 || st.page < st.totalPages
}

// resetSearchResults 丢弃当前结果并作废在途请求：登录态变化后，属于上一个
// 账号的「收藏」结果不能再留着。关键词与类型保留，用户重新输入即可。
func (a *app) resetSearchResults() {
	st := a.search
	if st == nil {
		return
	}
	st.gen++
	st.seq++
	st.loading = false
	st.firstLoading = false
	st.err = ""
	st.note = ""
	st.hits = nil
	st.loaded = false
	st.page = 0
	st.total = 0
	st.totalPages = 0
	st.noMore = false
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}
