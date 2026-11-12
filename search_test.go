package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 线上实测的 search 响应形状：帖子 / 收藏是分页对象，用户 / 节点 / 标签是
// 裸数组（没有分页字段）。文档只给了参数表，这里用真实形状锁住解析。
const (
	searchPostsJSON = `{
	  "posts":[
	    {"id":"t1","short_id":"gJj0q6p","title":"职场中 大伙都是怎么应付不专业的同事的？",
	     "node":{"id":"n1","name":"职场那些事","slug":"workplace-stories"},
	     "author":{"id":"u1","username":"AIqiaozhi","avatar_url":"https://r2.2libra.com/a.png"},
	     "created_at":"2026-10-28T01:53:15.124Z","comment_count":36},
	    {"id":"t2","short_id":"3ouZ4Sq","title":"【吐槽】职场小登有点看不懂现在领导的操作了",
	     "node":{"id":"n2","name":"办公日常","slug":"office-life"},
	     "author":{"id":"u2","username":"Oleander"},"created_at":"2026-10-28T06:09:37.254Z","comment_count":10}
	  ],
	  "total":60,"page":1,"limit":20,"total_pages":3
	}`
	searchUsersJSON = `[
	  {"id":"u1","username":"jiabinlan","avatar_url":"https://r2.2libra.com/u1.png","is_pro":false},
	  {"id":"u2","username":"jiadapeng","avatar_url":null,"is_pro":true}
	]`
	searchNodesJSON = `[
	  {"id":"n1","name":"职场发展","slug":"career","parent_slug":null,"post_count":369},
	  {"id":"n2","name":"职场那些事","slug":"workplace-stories","parent_slug":"career","post_count":181}
	]`
	searchTagsJSON = `[
	  {"id":"g1","name":"职场","post_count":262},
	  {"id":"g2","name":"职场压力","post_count":8}
	]`
)

func TestDecodeSearchResults(t *testing.T) {
	posts := decodeSearch(searchPosts, []byte(searchPostsJSON), 1)
	if len(posts.hits) != 2 || posts.total != 60 || posts.totalPages != 3 {
		t.Fatalf("帖子结果 = %+v", posts)
	}
	if posts.hits[0].item.ShortID != "gJj0q6p" || posts.hits[0].item.Author != "AIqiaozhi" {
		t.Errorf("帖子行没有归一：%+v", posts.hits[0].item)
	}
	if !strings.Contains(posts.hits[0].item.Subtitle, "职场那些事") {
		t.Errorf("帖子副标题缺少节点：%q", posts.hits[0].item.Subtitle)
	}

	users := decodeSearch(searchUsers, []byte(searchUsersJSON), 1)
	if len(users.hits) != 2 || users.hits[0].username != "jiabinlan" {
		t.Fatalf("用户结果 = %+v", users)
	}
	if users.hits[0].url != "https://2libra.com/user/jiabinlan/about" {
		t.Errorf("用户链接 = %q", users.hits[0].url)
	}
	if users.totalPages != 1 || users.total != 2 {
		t.Errorf("用户搜索没有分页对象，应一次返回全部：%+v", users)
	}

	nodes := decodeSearch(searchNodes, []byte(searchNodesJSON), 1)
	if len(nodes.hits) != 2 || nodes.hits[1].name != "职场那些事" || nodes.hits[1].count != 181 {
		t.Fatalf("节点结果 = %+v", nodes)
	}
	if nodes.hits[1].url != "https://2libra.com/node/career/workplace-stories" {
		t.Errorf("子节点链接 = %q，期望带上父节点", nodes.hits[1].url)
	}
	if nodes.hits[0].url != "https://2libra.com/node/career" {
		t.Errorf("顶层节点链接 = %q", nodes.hits[0].url)
	}

	tags := decodeSearch(searchTags, []byte(searchTagsJSON), 1)
	if len(tags.hits) != 2 || tags.hits[0].name != "职场" || tags.hits[0].count != 262 {
		t.Fatalf("标签结果 = %+v", tags)
	}
	if tags.hits[0].url != "https://2libra.com/tag/%E8%81%8C%E5%9C%BA" {
		t.Errorf("标签链接 = %q", tags.hits[0].url)
	}
}

// 参数名与取值照抄文档：q / type / page / limit，type 用 posts 这类字面值。
func TestSearchQueryParams(t *testing.T) {
	q := searchQuery(searchFavorites, " 职场 ", 2)
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("q") != "职场" || v.Get("type") != "favorites" || v.Get("page") != "2" || v.Get("limit") != "20" {
		t.Errorf("搜索参数 = %v", v)
	}
	for kind, want := range map[searchKind]string{
		searchPosts: "posts", searchUsers: "users", searchNodes: "nodes",
		searchTags: "tags", searchFavorites: "favorites",
	} {
		if got := kind.value(); got != want {
			t.Errorf("类型 %d 的取值 = %q，期望 %q", kind, got, want)
		}
	}
}

// 搜索请求要落在 /v1/search 上，并带上令牌（登录后配额更高）。
func TestSearchRunPopulatesHits(t *testing.T) {
	var gotPath, gotQuery, gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotQuery.Store(r.URL.RawQuery)
		gotAuth.Store(r.Header.Get("Authorization"))
		io.WriteString(w, envelopeJSON(searchPostsJSON))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.search = newSearchState()
	st := a.search
	st.q = "职场"
	st.kind = int(searchPosts)
	st.gen = 1
	done := make(chan error, 1)
	a.onSearchDone = func(_ string, err error) { done <- err }

	a.runSearch(st, st.gen, 1)
	if err := waitSearch(t, done); err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if gotPath.Load() != "/v1/search" {
		t.Errorf("请求路径 = %v", gotPath.Load())
	}
	if !strings.Contains(gotQuery.Load().(string), "type=posts") ||
		!strings.Contains(gotQuery.Load().(string), "q=%E8%81%8C%E5%9C%BA") {
		t.Errorf("请求参数 = %v", gotQuery.Load())
	}
	if gotAuth.Load() != "Bearer test-token" {
		t.Errorf("搜索应带令牌：%v", gotAuth.Load())
	}
	if len(st.hits) != 2 || st.total != 60 || st.totalPages != 3 || st.page != 1 {
		t.Errorf("结果状态 = hits %d total %d pages %d page %d",
			len(st.hits), st.total, st.totalPages, st.page)
	}
	if st.loading || st.firstLoading {
		t.Errorf("完成后仍在加载态：%+v", st)
	}
}

// 搜索窗口的空状态与「关键词太短」说明：都不发请求。
func TestSearchWindowEmptyStates(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	if a.search == nil {
		t.Fatal("搜索状态没有建出来")
	}
	tt := ui.NewTester(a.searchWindowView, 620, 680)
	if !tt.HasText("输入关键词开始搜索") {
		t.Errorf("空关键词没有占位说明：%q", tt.Texts())
	}
	a.search.q = "a"
	tt.Frame()
	if !tt.HasText("关键词太短") {
		t.Errorf("单字关键词应提示太短：%q", tt.Texts())
	}
	a.search.q = "ab"
	tt.Frame()
	if !tt.HasText("稍候即可搜索") && !tt.HasText("关键词太短") {
		t.Errorf("两个字的提示不对：%q", tt.Texts())
	}
}

// 结果为空的说明与结果行都要能画出来。
func TestSearchWindowRendersHits(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.q = "职场"
	st.kind = int(searchPosts)
	st.loaded = true
	st.total = 60
	st.page = 1
	st.totalPages = 3
	st.hits = decodeSearch(searchPosts, []byte(searchPostsJSON), 1).hits

	tt := ui.NewTester(a.searchWindowView, 620, 680)
	for _, want := range []string{"职场中 大伙都是怎么应付不专业的同事的？", "共 60 条", "搜索", "帖子", "用户", "节点", "标签", "收藏"} {
		if !tt.HasText(want) {
			t.Errorf("搜索窗口缺少 %q：%q", want, tt.Texts())
		}
	}

	st.hits = nil
	st.loaded = true
	tt.Frame()
	if !tt.HasText("没有找到结果") {
		t.Errorf("空结果没有说明：%q", tt.Texts())
	}
}

// 「收藏」是私人内容：未登录时先提示登录，点「去登录」走全局授权弹窗。
func TestSearchFavoritesRequiresLogin(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.q = "职场"
	st.kind = int(searchFavorites)
	tt := ui.NewTester(a.searchWindowView, 620, 680)
	if !tt.HasText("搜索自己的收藏需要登录") {
		t.Fatalf("未登录搜索收藏应提示登录：%q", tt.Texts())
	}
	if err := tt.Click("去登录"); err != nil {
		t.Fatalf("点「去登录」失败：%v", err)
	}
	if !a.authGate.open {
		t.Error("「去登录」应打开全局授权弹窗")
	}
}

// 用户结果点开的是应用内资料卡，不是浏览器。
func TestSearchUserRowOpensCard(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.q = "ji"
	st.kind = int(searchUsers)
	st.lastQ, st.lastKind = st.q, st.kind // 类型已在上一帧稳定
	st.loaded = true
	st.total = 2
	st.hits = decodeSearch(searchUsers, []byte(searchUsersJSON), 1).hits
	tt := ui.NewTester(a.searchWindowView, 620, 680)
	if _, ok := tt.Find("用户 jiabinlan"); !ok {
		t.Fatalf("用户行没有画出来：%q", tt.Texts())
	}
	if err := tt.Click("查看资料"); err != nil {
		t.Fatalf("点用户行失败：%v", err)
	}
	if a.userCard == nil || a.userCard.username != "jiabinlan" {
		t.Errorf("没有打开用户资料卡：%+v", a.userCard)
	}
}

// 防抖：连续输入只发一次请求；定时器到期后结果回到状态里。
func TestSearchDebounce(t *testing.T) {
	old := searchDebounceDelay
	searchDebounceDelay = 10 * time.Millisecond
	t.Cleanup(func() { searchDebounceDelay = old })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, envelopeJSON(searchPostsJSON))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.search = newSearchState()
	st := a.search
	done := make(chan error, 2)
	a.onSearchDone = func(_ string, err error) { done <- err }

	st.q = "职"
	a.scheduleSearch(st)
	st.q = "职场"
	a.scheduleSearch(st)
	if err := waitSearch(t, done); err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("防抖后请求次数 = %d，期望 1", n)
	}
	if len(st.hits) != 2 {
		t.Errorf("结果没有回填：%+v", st)
	}
}

// 独立限流：429 要给出可读的原因，而不是一直转圈。
func TestSearchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","dimension":"route","retry_after":30,"d":null,"t":1}`)
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.search = newSearchState()
	st := a.search
	st.q = "职场"
	st.kind = int(searchPosts)
	st.gen = 1
	done := make(chan error, 1)
	a.onSearchDone = func(_ string, err error) { done <- err }
	a.runSearch(st, st.gen, 1)
	if err := waitSearch(t, done); err == nil {
		t.Fatal("429 应返回错误")
	}
	if !strings.Contains(st.err, "请求过于频繁") {
		t.Errorf("错误说明 = %q", st.err)
	}
}

// 登录态变化后，属于上一个账号的收藏结果要被丢掉。
func TestResetSearchResults(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.q = "职场"
	st.kind = int(searchFavorites)
	st.hits = decodeSearch(searchPosts, []byte(searchPostsJSON), 1).hits
	st.loaded = true
	st.total = 60
	a.resetSearchResults()
	if len(st.hits) != 0 || st.loaded || st.total != 0 {
		t.Errorf("重置后仍留着结果：%+v", st)
	}
	if st.q != "职场" || st.kind != int(searchFavorites) {
		t.Errorf("重置不该丢掉关键词与类型：%+v", st)
	}
}

func waitSearch(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("等待搜索完成超时")
		return nil
	}
}
