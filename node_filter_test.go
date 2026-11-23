package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ---------------- 数据层 ---------------- */

func TestHomeQueryDefaultUnchanged(t *testing.T) {
	if got, want := homeQuery(allNodes()), "page=1&limit=50&parent_slug=All&sort=value"; got != want {
		t.Fatalf("默认 query = %q，期望 %q", got, want)
	}
}

func TestHomeQueryParentAndNode(t *testing.T) {
	if got, want := homeQuery(parentFilter(nodeRef{Slug: "life"})),
		"page=1&limit=50&parent_slug=life&sort=value"; got != want {
		t.Errorf("父分类 query = %q，期望 %q", got, want)
	}
	if got, want := homeQuery(nodeFilterOf(nodeRef{ID: "uuid-work", Sort: nodeSortLatest})),
		"page=1&limit=50&node_id=uuid-work&sort=latest"; got != want {
		t.Errorf("子节点 query = %q，期望 %q", got, want)
	}
	// 脏数据兜回价值优先，不会把 sort= 拼成空串。
	if got, want := homeQuery(nodeFilterOf(nodeRef{ID: "uuid-work", Sort: "bad"})),
		"page=1&limit=50&node_id=uuid-work&sort=value"; got != want {
		t.Errorf("非法排序 query = %q，期望 %q", got, want)
	}
}

func TestHomeQueryPageTwoKeepsFilter(t *testing.T) {
	a := testApp(t)
	a.tabs[0].homeFilterNode = nodeFilterOf(nodeRef{ID: "uuid-work"})
	spec := a.specFor(0)
	if !spec.FilterByNode {
		t.Fatal("首页 spec 应标记 FilterByNode")
	}
	url := endpoint(apiBaseURL, spec, 2)
	if want := "https://2libra.com/api/v1/topics?limit=50&node_id=uuid-work&page=2&sort=value"; url != want {
		t.Fatalf("第 2 页 URL = %q，期望 %q", url, want)
	}
	// 社区讨论走固定的 special 维度 query，不能被首页筛选污染。
	if got := endpoint(apiBaseURL, a.specFor(5), 1); !strings.Contains(got, "dimensions=special") ||
		!strings.Contains(got, "parent_slug=All") {
		t.Errorf("社区讨论的 query 被改掉了：%q", got)
	}
}

func TestNodeFilterLabel(t *testing.T) {
	cases := []struct {
		f    nodeFilter
		want string
	}{
		{allNodes(), "全部节点"},
		{parentFilter(nodeRef{Slug: "life", Name: "生活"}), "生活"},
		{parentFilter(nodeRef{Slug: "life"}), "life"},
		{nodeFilterOf(nodeRef{ID: "n", Slug: "work", Name: "职场", ParentName: "生活"}), "生活 · 职场"},
		{nodeFilterOf(nodeRef{ID: "n", Name: "职场"}), "职场"},
	}
	for _, c := range cases {
		if got := nodeFilterLabel(c.f); got != c.want {
			t.Errorf("标签 = %q，期望 %q", got, c.want)
		}
	}
}

func TestResolveNodeRefBySlug(t *testing.T) {
	a := testApp(t)
	a.nodeCat.nodes = []composeNode{
		{ID: "n1", Slug: "life", Name: "生活"},
		{ID: "n2", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"},
	}
	got := a.resolveNodeRef(nodeRef{Slug: "work", ParentSlug: "life", Sort: nodeSortLatest})
	if got.ID != "n2" || got.ParentName != "生活" {
		t.Fatalf("按 slug 补 id / 父名失败：%+v", got)
	}
	if got.Sort != nodeSortLatest {
		t.Errorf("补 id 时把节点自己的排序丢了：%+v", got)
	}
}

func TestResolveNodeRefAmbiguousName(t *testing.T) {
	a := testApp(t)
	a.nodeCat.nodes = []composeNode{
		{ID: "a1", Slug: "one", Name: "中转站", ParentSlug: "ai", ParentName: "AI"},
		{ID: "a2", Slug: "two", Name: "中转站", ParentSlug: "ai", ParentName: "AI"},
	}
	ref := nodeRef{Name: "中转站"}
	if got := a.resolveNodeRef(ref); got.ID != "" {
		t.Errorf("同名节点不该瞎选：%+v", got)
	}
}

/* ---------------- 搜索 ---------------- */

func nodeSearchFixtures() []composeNode {
	return []composeNode{
		{ID: "n-life", Slug: "life", Name: "生活"},
		{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活", PostCount: 132},
		{ID: "n-pet", Slug: "pet", Name: "萌宠", ParentSlug: "life", ParentName: "生活", PostCount: 12},
		{ID: "n-ai", Slug: "ai", Name: "AI"},
		{ID: "n-hub", Slug: "transfer", Name: "中转站", ParentSlug: "ai", ParentName: "AI", PostCount: 8},
	}
}

func TestNodeSearchMatchesNameParentAndSlug(t *testing.T) {
	for _, q := range []string{"职场", "生活", "WORK", "transfer", "生活 · 职场"} {
		hits := nodeSearch(nodeSearchFixtures(), q)
		if len(hits) == 0 {
			t.Errorf("查询 %q 没有命中", q)
		}
	}
	if hits := nodeSearch(nodeSearchFixtures(), "不存在的节点"); len(hits) != 0 {
		t.Errorf("不该命中：%+v", hits)
	}
}

func TestNodeSearchParentFirst(t *testing.T) {
	hits := nodeSearch(nodeSearchFixtures(), "生活")
	if len(hits) < 3 || !hits[0].IsParent || hits[0].Ref.Slug != "life" {
		t.Fatalf("父分类没排在前面：%+v", hits)
	}
	if hits[1].Ref.Slug != "work" || hits[2].Ref.Slug != "pet" {
		t.Errorf("子节点顺序不对：%+v", hits)
	}
}

func TestNodeSearchComposeOnlyChildren(t *testing.T) {
	hits := searchHitsForPicker(nodeSearchFixtures(), "生活", false)
	for _, h := range hits {
		if h.IsParent {
			t.Errorf("发帖模式不该返回可选父分类：%+v", h)
		}
	}
	if len(hits) != 2 {
		t.Errorf("发帖模式结果数 = %d，期望 2", len(hits))
	}
}

func TestNodeSearchLimitAndWhitespace(t *testing.T) {
	nodes := make([]composeNode, 0, 60)
	for i := 0; i < 60; i++ {
		nodes = append(nodes, composeNode{ID: "n", Slug: "s", Name: "共同词"})
	}
	if hits := nodeSearch(nodes, "共同词"); len(hits) != nodeSearchLimit {
		t.Errorf("结果上限 = %d，期望 %d", len(hits), nodeSearchLimit)
	}
	for _, q := range []string{"", "   ", "\t"} {
		if hits := nodeSearch(nodes, q); hits != nil {
			t.Errorf("空白查询 %q 应回到级联：%+v", q, hits)
		}
	}
}

/* ---------------- 持久化 ---------------- */

func TestShortcutStorePersistAndCap(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	items := make([]nodeRef, 0, 12)
	for i := 0; i < 12; i++ {
		items = append(items, nodeRef{ID: "n" + string(rune('a'+i)), Slug: "s", Name: "节点"})
	}
	items = append(items, items[0]) // 重复项
	items[0].Sort = nodeSortLatest  // 排序跟着自己的节点落盘
	a.nodeShortcuts = items
	a.saveNodeShortcuts()

	second := newApp(dir)
	second.loadNodePrefs()
	if len(second.nodeShortcuts) != nodeShortcutMax {
		t.Fatalf("快捷入口数 = %d，期望 %d", len(second.nodeShortcuts), nodeShortcutMax)
	}
	if second.nodeShortcuts[0].Sort != nodeSortLatest {
		t.Errorf("节点的排序没持久化：%+v", second.nodeShortcuts[0])
	}
	seen := map[string]bool{}
	for _, r := range second.nodeShortcuts {
		if seen[r.key()] {
			t.Errorf("快捷入口没有去重：%+v", second.nodeShortcuts)
		}
		seen[r.key()] = true
	}
}

// 启动时首页总是默认筛选：不再按账号恢复上次筛选。
func TestStartupHomeFilterIsDefault(t *testing.T) {
	dir := t.TempDir()
	first := newApp(dir)
	first.accountID = "alice"
	first.tabs[0].homeFilterNode = nodeFilterOf(nodeRef{ID: "a1", Name: "甲"})
	first.saveNodeShortcuts()

	second := newApp(dir)
	second.accountID = "alice"
	second.loadNodePrefs()
	if !second.homeFilter().isAll() {
		t.Errorf("启动时不该恢复上次筛选：%+v", second.homeFilter())
	}
}

/* ---------------- UI ---------------- */

// nodeFeedEnv 造一个带节点表与首页列表的测试服务器，并记录收到的 query。
type nodeFeedEnv struct {
	mu      sync.Mutex
	queries []string
	srv     *httptest.Server
}

func newNodeFeedEnv(t *testing.T) *nodeFeedEnv {
	t.Helper()
	e := &nodeFeedEnv{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/nodes":
			_, _ = io.WriteString(w, envelopeJSON(`[
				{"id":"n-life","slug":"life","name":"生活","children":[
					{"id":"n-work","slug":"work","name":"职场","parent_slug":"life","post_count":132}
				]},
				{"id":"n-ai","slug":"ai","name":"AI","children":[
					{"id":"n-hub","slug":"transfer","name":"中转站","parent_slug":"ai"}
				]}
			]`))
		default:
			e.mu.Lock()
			e.queries = append(e.queries, r.URL.RawQuery)
			e.mu.Unlock()
			_, _ = io.WriteString(w, envelopeJSON(`{"posts":[{"id":"p1","short_id":"P1","title":"帖子标题",
				"author":{"username":"jimmy"},"node":{"id":"n-work","slug":"work","name":"职场","parent_slug":"life"},
				"created_at":"2026-11-01T10:00:00.000Z"}],"total":1,"page":1,"limit":50,"total_pages":1}`))
		}
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *nodeFeedEnv) lastQuery() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queries) == 0 {
		return ""
	}
	return e.queries[len(e.queries)-1]
}

func nodeFeedApp(t *testing.T, e *nodeFeedEnv) *app {
	t.Helper()
	a := loginApp(t, e.srv)
	a.nodeCat.nodes = []composeNode{
		{ID: "n-life", Slug: "life", Name: "生活"},
		{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"},
	}
	a.nodeCat.loaded = true
	a.tabs[0].loaded = true
	a.tabs[0].items = []Item{itemFrom([]byte(`{"short_id":"P1","title":"帖子标题",
		"author":{"username":"jimmy"},"node":{"id":"n-work","slug":"work","name":"职场","parent_slug":"life"},
		"created_at":"2026-11-01T10:00:00.000Z"}`), kindTopic)}
	return a
}

func waitQuery(t *testing.T, e *nodeFeedEnv, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if q := e.lastQuery(); strings.Contains(q, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有等到包含 %q 的请求，最后 query = %q", want, e.lastQuery())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNodeTagClickFiltersHomepage(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("职场"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "node_id=n-work")
	if a.tab != 0 || a.tabs[0].homeFilterNode.Ref.ID != "n-work" {
		t.Errorf("Tag 点击没有应用筛选：tab=%d filter=%+v", a.tab, a.tabs[0].homeFilterNode)
	}
}

func TestNodeTagInsideRowDoesNotOpenPost(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pageName != "list" {
		t.Errorf("点行内 Tag 连带打开了帖子：pageName=%s", a.pageName)
	}
}

func TestFilterChipOpensCascade(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("整个「生活」") || !tt.HasText("职场") {
		t.Fatalf("级联没有打开：%q", tt.Texts())
	}
	if err := tt.Click("职场"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "node_id=n-work")
	if !reflect.DeepEqual(a.tabs[0].homeFilterNode.Ref, nodeRef{
		ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活",
	}) {
		t.Errorf("筛选引用不对：%+v", a.tabs[0].homeFilterNode.Ref)
	}
}

func TestFilterSearchSwitchesToResults(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("整个「生活」") {
		t.Fatalf("级联没开：%q", tt.Texts())
	}
	if err := tt.Click("搜索节点"); err != nil {
		t.Fatal(err)
	}
	tt.Type("生活")
	tt.Frame()
	if _, ok := tt.Find("整个「生活」"); ok {
		t.Errorf("搜索态还留着两列级联：%q", tt.Texts())
	}
	if !tt.HasText("分类") {
		t.Errorf("搜索结果里没有分类标记：%q", tt.Texts())
	}
}

func TestSidebarShortcutClick(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.nodeShortcuts = []nodeRef{{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活", Sort: nodeSortLatest}}
	a.navExpanded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("生活 · 职场"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "node_id=n-work")
	waitQuery(t, e, "sort=latest")
	if a.tabs[0].homeFilterNode.Ref.ID != "n-work" {
		t.Errorf("快捷入口没有筛到节点：%+v", a.tabs[0].homeFilterNode)
	}
}

// 侧边栏快捷入口选中父分类时发 parent_slug，不能因为从节点表补到了 ID
// 就误判成子节点、改发 node_id（那样会筛到具体节点且高亮也熄灭）。
func TestSidebarParentShortcutUsesParentSlug(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	// 父分类快捷入口只有 slug / name，没有 id / parent_slug；节点表里却能补到
	// id=n-life，正是原来误判的触发条件。
	a.nodeShortcuts = []nodeRef{{Slug: "life", Name: "生活", Sort: nodeSortLatest}}
	a.navExpanded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("生活"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "parent_slug=life")
	waitQuery(t, e, "sort=latest")
	if q := e.lastQuery(); strings.Contains(q, "node_id=") {
		t.Errorf("父分类快捷入口错用了 node_id：%q", q)
	}
	f := a.tabs[0].homeFilterNode
	if f.Kind != nodeFilterParent || f.Ref.Slug != "life" {
		t.Errorf("首页筛选不是父分类：%+v", f)
	}
	if a.activeNodeShortcutKey() == "" {
		t.Error("父分类快捷入口点击后没有高亮")
	}
}

func TestFilterChipClearResetsHome(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.tabs[0].homeFilterNode = nodeFilterOf(nodeRef{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"})
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("清除节点筛选"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "parent_slug=All")
	if !a.tabs[0].homeFilterNode.isAll() {
		t.Errorf("清除后没回到全部：%+v", a.tabs[0].homeFilterNode)
	}
}

func TestFilterSearchEnterSelectsFirst(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("搜索节点"); err != nil {
		t.Fatal(err)
	}
	tt.Type("职场")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	waitQuery(t, e, "node_id=n-work")
}

func TestFilterSearchEscapeClearsBeforeClose(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("搜索节点"); err != nil {
		t.Fatal(err)
	}
	tt.Type("生活")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.tabs[0].filterSearch != "" {
		t.Errorf("第一次 Esc 没清空搜索词：%q", a.tabs[0].filterSearch)
	}
	if !a.tabs[0].filterOpen {
		t.Error("第一次 Esc 不该关闭浮层")
	}
}

// 挂着节点筛选时点主页图标：回到默认筛选重新拉首屏，自定义节点高亮熄灭，
// 选中的仍是刚点过的主页图标。折叠轨与展开列表两条路径都要覆盖。
func TestHomeIconClickResetsNodeFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expanded bool
	}{
		{"折叠图标轨", false},
		{"展开导航列表", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNodeFeedEnv(t)
			a := nodeFeedApp(t, e)
			work := nodeRef{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"}
			a.nodeShortcuts = []nodeRef{work}
			a.tabs[0].homeFilterNode = nodeFilterOf(work)
			a.navExpanded = tc.expanded
			if a.activeNodeShortcutKey() == "" {
				t.Fatal("前置条件：节点快捷入口应处于高亮")
			}
			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			if err := tt.Click("首页"); err != nil {
				t.Fatal(err)
			}
			waitQuery(t, e, "parent_slug=All")
			if a.tab != 0 || a.navSel != tabID(0) {
				t.Errorf("主页图标没选中：tab=%d navSel=%q", a.tab, a.navSel)
			}
			if !a.tabs[0].homeFilterNode.isAll() {
				t.Errorf("点主页后没回到默认筛选：%+v", a.tabs[0].homeFilterNode)
			}
			if got := a.activeNodeShortcutKey(); got != "" {
				t.Errorf("自定义节点高亮没熄灭：%q", got)
			}
		})
	}
}

// 没有节点筛选时点主页图标只走原来的刷新路径，不该改动筛选状态。
func TestHomeIconClickWithoutFilterOnlyRefreshes(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("首页"); err != nil {
		t.Fatal(err)
	}
	waitQuery(t, e, "parent_slug=All")
	if !a.tabs[0].homeFilterNode.isAll() {
		t.Errorf("默认筛选被改动：%+v", a.tabs[0].homeFilterNode)
	}
}
