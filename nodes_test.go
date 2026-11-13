package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 线上顶层 /v1/nodes 的响应：数组里每个节点自带 children，一次就能拿到
// 父子关系，不需要按 parent 逐个再问。
func TestNodeTreeFromEmbeddedChildren(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/nodes" || r.URL.RawQuery != "" {
			t.Errorf("不该按 parent 再问：%s", r.URL.String())
		}
		io.WriteString(w, envelopeJSON(`[
			{"id":"n-life","slug":"daily-life","name":"生活日常","parent_slug":null,"children":[
				{"id":"n-personal","slug":"personal-life","name":"个人生活","parent_slug":"daily-life","post_count":458},
				{"id":"n-pet","slug":"pet","name":"养宠","parent_id":"n-life","post_count":5}
			]},
			{"id":"n-work","slug":"career","name":"职场发展","parent_slug":null,"children":[
				{"id":"n-office","slug":"office-life","name":"办公日常","parent_slug":"career","post_count":132}
			]},
			{"id":"n-q","slug":"q-and-a","name":"问与答","children":[]}
		]`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	nodes, err := a.client.loadNodeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("请求次数 = %d，期望 1（children 已在顶层响应里）", n)
	}
	byID := map[string]composeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if len(nodes) != 6 {
		t.Fatalf("节点数 = %d，期望 6：%+v", len(nodes), nodes)
	}
	// 子节点紧跟自己的父节点，显示成「父 · 子」。
	if nodes[0].ID != "n-life" || nodes[1].ID != "n-personal" || nodes[2].ID != "n-pet" {
		t.Errorf("父子顺序不对：%+v", nodes[:3])
	}
	if got := byID["n-personal"]; got.ParentName != "生活日常" || got.parentDisplay() != "生活日常 · 个人生活" {
		t.Errorf("子节点没有带上父节点名：%+v", got)
	}
	// 子响应缺 parent_slug 时用父节点的 slug 补上。
	if got := byID["n-pet"]; got.ParentSlug != "daily-life" || got.parentDisplay() != "生活日常 · 养宠" {
		t.Errorf("缺 parent_slug 的子节点归错了父节点：%+v", got)
	}
}

// 退回旧形状时，子节点查询的线上响应是 {items:[...]} 而不是数组；这个 bug
// 以前会让所有子节点静默消失。
func TestNodeTreeChildItemsShape(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.Query().Get("parent") {
		case "":
			// 顶层没有 children 字段：只能按 parent 再问。
			io.WriteString(w, envelopeJSON(`[
				{"id":"top1","slug":"career","name":"职场发展","post_count":369},
				{"id":"top2","slug":"games","name":"游戏娱乐"}
			]`))
		case "career":
			io.WriteString(w, envelopeJSON(`{"items":[
				{"id":"c1","slug":"workplace-stories","name":"职场那些事","parent_slug":"career","post_count":181},
				{"id":"c2","slug":"job-hunting","name":"求职","parent_slug":"career","post_count":52}
			],"total":2,"page":1,"limit":50,"total_pages":1}`))
		default:
			io.WriteString(w, envelopeJSON(`{"items":[]}`))
		}
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	nodes, err := a.client.loadNodeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("节点数 = %d，期望 4（2 个顶层 + career 的 2 个子节点）：%+v", len(nodes), nodes)
	}
	byID := map[string]composeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if got := byID["c1"]; got.ParentName != "职场发展" || got.parentDisplay() != "职场发展 · 职场那些事" {
		t.Errorf("items 形状的子节点没有解析出来：%+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "/v1/nodes?" {
		t.Errorf("第一个请求应是顶层节点：%v", seen)
	}
}

// 级联选择的两列由扁平节点表拼出来：顶层分类在前，子节点挂在它下面；
// 没有子节点的顶层分类保持为叶子。
func TestNodeCascaderOptions(t *testing.T) {
	nodes := []composeNode{
		{ID: "n-forum", Slug: "forum", Name: "社区"},
		{ID: "n-community", Slug: "community", Name: "社区讨论", ParentSlug: "forum", ParentName: "社区"},
		{ID: "n-oauth", Slug: "oauth", Name: "三方应用", ParentSlug: "forum", ParentName: "社区"},
		{ID: "n-ai", Slug: "ai", Name: "AI"},
		{ID: "n-gateway", Slug: "ai-gateway", Name: "中转站", ParentSlug: "ai", ParentName: "AI"},
		{ID: "n-transfer", Slug: "transfer-station", Name: "中转站", ParentSlug: "ai", ParentName: "AI"},
		{ID: "n-q", Slug: "q-and-a", Name: "问与答"},
	}
	disambiguateNodeLabels(nodes)
	opts := nodeCascaderOptions(nodes)
	if len(opts) != 3 {
		t.Fatalf("第一列 = %d 项，期望 3 个顶层分类：%+v", len(opts), opts)
	}
	if opts[0].Value != "n-forum" || len(opts[0].Children) != 2 {
		t.Errorf("社区分类的子节点不对：%+v", opts[0])
	}
	if opts[2].Value != "n-q" || len(opts[2].Children) != 0 {
		t.Errorf("没有子节点的顶层分类应当保持为叶子：%+v", opts[2])
	}
	if got := opts[1].Children; len(got) != 2 ||
		got[0].Label != "中转站 (ai-gateway)" || got[1].Label != "中转站 (transfer-station)" {
		t.Errorf("同名节点的级联选项没有区分：%+v", got)
	}
	if label, ok := cascaderLabel(opts, "n-transfer"); !ok || label != "AI · 中转站 (transfer-station)" {
		t.Errorf("选中项显示 = %q，ok=%v", label, ok)
	}
}

// 缓存要落盘：换一个进程（新的 app 实例、同一个配置目录）仍能读到节点表。
func TestNodeCachePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	nodes := []composeNode{
		{ID: "n1", Slug: "career", Name: "职场发展"},
		{ID: "n2", Slug: "office-life", Name: "办公日常", ParentSlug: "career", ParentName: "职场发展",
			Label: "职场发展 · 办公日常 (office-life)"},
	}
	at := time.Now().Add(-time.Minute)
	first := newApp(dir)
	first.storeNodeCache(nodes, at)

	second := newApp(dir)
	second.autoLoad = false
	got, gotAt, ok := second.nodeSnapshot()
	if !ok || len(got) != 2 {
		t.Fatalf("重启后没读到节点缓存：ok=%v nodes=%+v", ok, got)
	}
	if got[1].ParentName != "职场发展" || got[1].Label == "" {
		t.Errorf("父子关系 / 去重标签没有随缓存保存：%+v", got[1])
	}
	if got[1].Label != "职场发展 · 办公日常 (office-life)" {
		t.Errorf("缓存里的显示名不对：%q", got[1].Label)
	}
	if d := gotAt.Sub(at); d > time.Second || d < -time.Second {
		t.Errorf("缓存时间戳漂移：%v", d)
	}
}

// 新鲜缓存：不发请求，同步把节点摆进发帖窗口。
func TestComposeNodesUseFreshCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, envelopeJSON(`[]`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.compose = newComposeState()
	cached := []composeNode{
		{ID: "n1", Slug: "career", Name: "职场发展"},
		{ID: "n2", Slug: "office-life", Name: "办公日常", ParentSlug: "career", ParentName: "职场发展"},
	}
	a.storeNodeCache(cached, time.Now())
	a.ensureComposeNodes()

	if n := calls.Load(); n != 0 {
		t.Errorf("新鲜缓存不该发请求，实际 %d 次", n)
	}
	if len(a.compose.nodes) != 2 || !a.compose.nodeLoaded {
		t.Fatalf("缓存没有摆进级联选择：%+v", a.compose)
	}
	// 节点默认不预选：发帖后由站点 AI 判定分类，读入缓存只补可选列表。
	if a.compose.nodeID != "" {
		t.Errorf("缓存不该预选节点 = %q，期望留空由 AI 判定", a.compose.nodeID)
	}
	if a.compose.nodeLoading {
		t.Error("命中缓存不应进入加载态")
	}
}

// 过期缓存：先把旧节点摆上（不让级联选择空着），再在后台刷新并覆盖缓存。
func TestComposeNodesRefreshStaleCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, envelopeJSON(`[
			{"id":"n-new","slug":"career","name":"职场发展","children":[
				{"id":"n-office","slug":"office-life","name":"办公日常","parent_slug":"career","post_count":132}
			]}
		]`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.compose = newComposeState()
	// 一小时前的缓存：一定过期。
	a.storeNodeCache([]composeNode{{ID: "n-old", Slug: "old", Name: "旧节点"}}, time.Now().Add(-time.Hour))
	done := make(chan error, 1)
	a.onNodesLoaded = func(err error) { done <- err }

	a.ensureComposeNodes()
	// 单线程里这里能同步看到缓存被摆上：过期也要先显示。
	if len(a.compose.nodes) != 1 || a.compose.nodes[0].ID != "n-old" {
		t.Fatalf("过期缓存没有先显示：%+v", a.compose.nodes)
	}
	if !a.compose.nodeLoading {
		t.Fatal("过期缓存应在后台刷新")
	}
	if err := waitNodes(t, done); err != nil {
		t.Fatalf("刷新节点失败：%v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("刷新请求次数 = %d，期望 1", n)
	}
	if len(a.compose.nodes) != 2 || a.compose.nodes[0].ID != "n-new" {
		t.Errorf("刷新后节点没更新：%+v", a.compose.nodes)
	}
	if !nodeCacheFresh(a.nodes.at) {
		t.Error("刷新后缓存应重新变新鲜")
	}
	// 新结果也写回了缓存。
	if got, _, ok := a.nodeSnapshot(); !ok || len(got) != 2 || got[1].ID != "n-office" {
		t.Errorf("刷新结果没有写回缓存：ok=%v %+v", ok, got)
	}
}

// 同一个父节点下有同名节点（线上 AI 下有两个「中转站」）：显示名要补 slug，
// 否则级联选择的两行分不出两个，提交时还会把 uuid 换成第一个。
func TestNodeTreeDisambiguatesSameNameSiblings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, envelopeJSON(`[
			{"id":"n-ai","slug":"ai","name":"AI","children":[
				{"id":"a1","slug":"ai-gateway","name":"中转站","parent_slug":"ai"},
				{"id":"a2","slug":"transfer-station","name":"中转站","parent_slug":"ai"}
			]}
		]`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	nodes, err := a.client.loadNodeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("节点数 = %d，期望 3：%+v", len(nodes), nodes)
	}
	first, second := nodes[1], nodes[2]
	if first.ID != "a1" || second.ID != "a2" {
		t.Fatalf("子节点顺序不对：%+v", nodes)
	}
	if first.Label != "AI · 中转站 (ai-gateway)" || second.Label != "AI · 中转站 (transfer-station)" {
		t.Errorf("同名节点没有用 slug 区分：%q / %q", first.Label, second.Label)
	}
	st := newComposeState()
	st.nodes = nodes
	st.nodeID = second.ID
	if got := st.selectedNode(); got == nil || got.ID != "a2" {
		t.Errorf("选中第二个「中转站」时解析到了 %+v，期望 a2", got)
	}
}

// 刷新失败后要冷却：视图每帧重建都会调用加载，没有冷却就会对着一个失败的
// 服务端反复请求。
func TestComposeNodesFailureCooldown(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"c":500,"code":"internal_error","m":"服务端错误","d":null,"t":1}`)
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.compose = newComposeState()
	a.storeNodeCache([]composeNode{{ID: "n-old", Slug: "old", Name: "旧节点"}}, time.Now().Add(-time.Hour))
	done := make(chan error, 2)
	a.onNodesLoaded = func(err error) { done <- err }

	a.ensureComposeNodes()
	if err := waitNodes(t, done); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("失败后请求次数 = %d，期望 1", n)
	}
	// 失败后仍然保留旧节点，用户至少还能选到原来的节点。
	if len(a.compose.nodes) != 1 || a.compose.nodes[0].ID != "n-old" {
		t.Errorf("失败后丢了缓存节点：%+v", a.compose.nodes)
	}
	// 再调用一次：冷却期内不再发请求。
	a.ensureComposeNodes()
	if n := calls.Load(); n != 1 {
		t.Errorf("冷却期内不该重试，实际 %d 次", n)
	}
	// 手动重试（清掉冷却）可以立刻再试一次。
	a.compose.nodesAt = time.Time{}
	a.compose.nodeFailedAt = time.Time{}
	a.ensureComposeNodes()
	if err := waitNodes(t, done); err == nil {
		t.Fatal("手动重试的失败应返回错误")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("手动重试后请求次数 = %d，期望 2", n)
	}
}

// 缓存新鲜期要不低于文档建议的 10 分钟。
func TestNodeCacheTTLAtLeastTenMinutes(t *testing.T) {
	if nodeCacheTTL < 10*time.Minute {
		t.Fatalf("节点缓存 TTL = %v，应不少于 10 分钟", nodeCacheTTL)
	}
}

func waitNodes(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("等待节点刷新超时")
		return nil
	}
}
