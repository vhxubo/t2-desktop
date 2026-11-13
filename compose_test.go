package main

import (
	"context"
	"encoding/json"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 幂等键必须是 [A-Za-z0-9_-]{1,255}（index.md 4.8）。
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// loginApp 造一个已登录的 app，并把网络指向测试服务器：发帖、节点与列表
// 请求都落在 srv 上，不会碰到真实站点。
func loginApp(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	a := testApp(t)
	a.client.Base = srv.URL
	if err := a.auth.storeToken(tokenData{
		AccessToken: "test-token",
		TokenType:   "Bearer",
		Username:    "reader",
		Scope:       "read profile write:topic write:economy",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// composeReady 让发帖窗口有节点可选：无头测试不自动加载，直接摆好列表。
// 列表按 /v1/nodes 的顺序：顶层分类后面跟着它的子节点。
func composeReady(a *app) *composeState {
	if a.compose == nil {
		a.compose = newComposeState()
	}
	a.compose.nodes = []composeNode{
		{ID: "uuid-forum", Slug: "forum", Name: "社区"},
		{ID: "uuid-community", Slug: "community", Name: "社区讨论", ParentSlug: "forum", ParentName: "社区"},
		{ID: "uuid-life", Slug: "life", Name: "生活"},
		{ID: "uuid-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"},
	}
	a.compose.nodeLoaded = true
	a.compose.nodeID = "uuid-community"
	return a.compose
}

// 节点用两列级联选择：第一列分类，第二列节点；选中后触发框显示父/子名字，
// 提交用的仍是 uuid。
func TestComposeNodeCascader(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.nodes = append(st.nodes, composeNode{ID: "uuid-q", Slug: "q-and-a", Name: "问与答"})
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	// 节点默认收起：说明里标出手动指定的节点，但不画级联选择。
	if _, ok := tt.Find("由 AI 判定分类"); ok {
		t.Errorf("默认不该画出节点级联选择：%q", tt.Texts())
	}
	if err := tt.Click("手动指定节点"); err != nil {
		t.Fatalf("找不到手动指定节点入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if !st.nodeManual {
		t.Fatalf("点「手动指定节点」后应展开：%+v", st)
	}
	if !tt.HasText("社区 · 社区讨论") {
		t.Fatalf("触发框没有显示当前节点：%q", tt.Texts())
	}

	if err := tt.Click("社区 · 社区讨论"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"社区", "生活", "社区讨论"} {
		if _, ok := tt.Find(want); !ok {
			t.Errorf("展开后缺少 %q：%q", want, tt.Texts())
		}
	}

	// 点第一列的分类只展开，不选中；第二列随之换成它的子节点。
	if err := tt.Click("生活"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("职场"); !ok {
		t.Fatalf("切到「生活」后没有显示它的子节点：%q", tt.Texts())
	}
	if st.nodeID != "uuid-community" {
		t.Errorf("点分类就改了选中节点：%q", st.nodeID)
	}

	if err := tt.Click("职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.nodeID != "uuid-work" {
		t.Errorf("选中节点 = %q，期望 uuid-work", st.nodeID)
	}
	if !tt.HasText("生活 · 职场") {
		t.Errorf("触发框没有跟着更新：%q", tt.Texts())
	}
	if _, ok := tt.Find("社区讨论"); ok {
		t.Errorf("选中后级联面板应当收起：%q", tt.Texts())
	}

	// 没有子节点的分类是叶子：点第一列直接选中。
	if err := tt.Click("生活 · 职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("问与答"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.nodeID != "uuid-q" {
		t.Errorf("叶子分类应当直接选中，实际 = %q", st.nodeID)
	}
	if !tt.HasText("问与答") || tt.HasText("社区 · 社区讨论") {
		t.Errorf("叶子分类选中后显示不对：%q", tt.Texts())
	}
}

// 节点级联选择：GET /v1/nodes 的响应 d 是数组；子节点要按 parent 再问一次，
// 拼成一份带父节点名的扁平列表。发帖必须用 uuid，所以列表项要带着 id。
func TestComposeNodesLoadTree(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("parent") {
		case "":
			io.WriteString(w, envelopeJSON(`[
				{"id":"uuid-community","slug":"community","name":"社区讨论","post_count":12,"parent_slug":null},
				{"id":"uuid-life","slug":"life","name":"生活","post_count":3}
			]`))
		case "life":
			io.WriteString(w, envelopeJSON(`[
				{"id":"uuid-work","slug":"work","name":"职场","parent_slug":"life"},
				{"id":"uuid-pet","slug":"pet","name":"萌宠"}
			]`))
		default:
			io.WriteString(w, envelopeJSON(`[]`))
		}
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.compose = newComposeState()
	a.autoLoad = true
	nodes, err := a.client.loadNodeTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("节点数 = %d，期望 4（2 个顶层 + life 的 2 个子节点）：%+v", len(nodes), nodes)
	}
	byID := map[string]composeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if got := byID["uuid-work"]; got.ParentName != "生活" || got.parentDisplay() != "生活 · 职场" {
		t.Errorf("子节点没有带上父节点名：%+v", got)
	}
	// 响应里缺 parent_slug 的子节点也要归到已知的父节点下。
	if got := byID["uuid-pet"]; got.ParentSlug != "life" || got.parentDisplay() != "生活 · 萌宠" {
		t.Errorf("缺 parent_slug 的子节点归错了父节点：%+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] != "/v1/nodes?" || seen[1] != "/v1/nodes?parent=community" &&
		seen[1] != "/v1/nodes?parent=life" {
		t.Errorf("节点请求不对：%v", seen)
	}
}

// 完整发布链路：POST /v1/topics 的路径、鉴权、必填的幂等键，以及请求体里
// 各组件按文档的字段名与取值原样出现。
func TestComposePublishSendsDocumentedPayload(t *testing.T) {
	var gotPath, gotAuth, gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/topics" {
			http.NotFound(w, r)
			return
		}
		gotPath, gotAuth, gotKey = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("请求体不是 JSON：%v", err)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, envelopeJSON(`{"short_id":"AbCd123","node":{"slug":"community","name":"社区讨论"}}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "  我的第一篇帖子  "
	st.body = "正文：**加粗**\n\n:smile:"
	st.anonymous = true

	st.voteOpen = true
	st.voteTypeIndex = 1
	st.voteTitle = "你选哪个"
	st.voteOptions = []string{"甲", "", "乙"}
	st.voteResultVisible = true
	st.voteDeadline = "2026-11-20 12:00"
	st.voteMaxChoices = 2

	st.scoreOpen = true
	st.scoreTarget = "某部电影"
	st.scoreValue = 88
	st.scoreComment = "值回票价"

	st.betOpen = true
	st.betTitle = "谁会赢"
	st.betOptions = []string{"A 队", "B 队"}

	st.redeemOpen = true
	st.redeemCodes = "CODE-1\nCODE-2，CODE-3"
	st.codeDescription = "先到先得"
	st.maxRedeemPerUser = 2
	st.redPacket = true
	st.redPacketIndex = 2 // 288

	st.poolOpen = true
	st.poolCoins = 500
	st.poolProbability = 0.2

	st.guardOpen = true
	st.guardChoice = guardTypeLabels[0] // 所有人可见：不发送 access_guard_*

	var done sync.WaitGroup
	done.Add(1)
	a.onPostPublished = func(created createdTopic, err error) {
		if err != nil {
			t.Errorf("发表失败：%v", err)
		}
		done.Done()
	}
	// 发帖会扣金币：成功后必须发出全局金币刷新事件。
	events := make(chan coinEvent, 1)
	a.onCoinsChanged = func(event coinEvent) { events <- event }
	a.submitCompose()
	done.Wait()
	select {
	case event := <-events:
		if event != coinEventPost {
			t.Errorf("发帖事件 = %q，期望 %q", event, coinEventPost)
		}
	default:
		t.Error("发帖成功后没有发出金币刷新事件")
	}

	if gotPath != "/v1/topics" || gotAuth != "Bearer test-token" || gotKey == "" {
		t.Fatalf("路径 / 鉴权 / 幂等键不对：%q %q %q", gotPath, gotAuth, gotKey)
	}
	if !keyPattern.MatchString(gotKey) {
		t.Errorf("幂等键格式不符合 [A-Za-z0-9_-]{1,255}：%q", gotKey)
	}

	assertField(t, gotBody, "title", "我的第一篇帖子")
	assertField(t, gotBody, "node_id", "uuid-community")
	assertField(t, gotBody, "is_anonymous", true)
	// 界面已经移除的补充字段，请求体里也不该再出现。
	for _, key := range []string{"title_prefix", "tip", "contact", "status"} {
		if _, ok := gotBody[key]; ok {
			t.Errorf("已移除的字段不该发送：%s", key)
		}
	}

	vote := mapField(t, gotBody, "vote")
	assertField(t, vote, "type", "multiple")
	assertField(t, vote, "title", "你选哪个")
	assertField(t, vote, "options", []any{"甲", "乙"})
	assertField(t, vote, "result_visible", true)
	assertField(t, vote, "max_choices", float64(2))
	if d, _ := vote["vote_deadline"].(string); !strings.HasSuffix(d, "Z") || !strings.HasPrefix(d, "2026-11-20T") {
		t.Errorf("投票截止时间不是 ISO 8601 UTC：%v", vote["vote_deadline"])
	}

	score := mapField(t, gotBody, "score")
	assertField(t, score, "target_name", "某部电影")
	assertField(t, score, "score", float64(88))
	assertField(t, score, "comment", "值回票价")

	bet := mapField(t, gotBody, "bet")
	assertField(t, bet, "title", "谁会赢")
	assertField(t, bet, "options", []any{"A 队", "B 队"})

	assertField(t, gotBody, "redeem_codes", []any{"CODE-1", "CODE-2", "CODE-3"})
	assertField(t, gotBody, "max_redeem_per_user", float64(2))
	assertField(t, gotBody, "code_description", "先到先得")
	assertField(t, gotBody, "enable_red_packet", true)
	assertField(t, gotBody, "max_red_packet_amount", float64(288))

	pool := mapField(t, gotBody, "reward_pool")
	assertField(t, pool, "initial_coins", float64(500))
	assertField(t, pool, "probability", 0.2)

	// 不限制可见性时一个 access_guard_* 字段都不发。
	for k := range gotBody {
		if strings.HasPrefix(k, "access_guard_") {
			t.Errorf("未开启门槛时不该发送 %s", k)
		}
	}

	// 发表成功后：表单清空、回执留下、下一次发帖换新幂等键。
	if st.title != "" || st.body != "" || st.okShortID != "AbCd123" || st.key != "" {
		t.Errorf("发表后的状态不对：%+v", st)
	}
	if st.okURL != "https://2libra.com/post/community/AbCd123" {
		t.Errorf("回执原文链接 = %q", st.okURL)
	}
}

// 可见性门槛：下拉框存的是标签，发出去的是文档里的机器取值。
func TestComposeGuardPayload(t *testing.T) {
	st := newComposeState()
	st.nodes = []composeNode{{ID: "uuid-node", Name: "社区讨论"}}
	st.nodeID = "uuid-node"
	st.title = "标题"

	p, err := st.payload()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p["access_guard_type"]; ok {
		t.Errorf("默认不限制时不该发送 access_guard_type")
	}

	st.guardChoice = guardTypeLabels[1]
	p, err = st.payload()
	if err != nil || p["access_guard_type"] != "login" {
		t.Errorf("登录可见：%v %v", p["access_guard_type"], err)
	}

	st.guardChoice = guardTypeLabels[2]
	st.guardLevel = 5
	p, err = st.payload()
	if err != nil || p["access_guard_type"] != "level" || p["access_guard_level"] != 5 {
		t.Errorf("等级可见：%v %v", p, err)
	}

	st.guardChoice = guardTypeLabels[3]
	if _, err = st.payload(); err == nil {
		t.Errorf("按属性可见但没填属性名时应报错")
	}
	st.guardAttr = "力量"
	st.guardAttrValue = 3
	p, err = st.payload()
	if err != nil || p["access_guard_type"] != "attribute" ||
		p["access_guard_attr"] != "力量" || p["access_guard_attr_value"] != 3 {
		t.Errorf("属性可见：%v %v", p, err)
	}
}

// 校验：必填项与文档给出的上限都要在本地拦住，不能等 400。
func TestComposePayloadValidation(t *testing.T) {
	base := func() *composeState {
		st := newComposeState()
		st.nodes = []composeNode{{ID: "uuid-node", Name: "社区讨论"}}
		st.title = "标题"
		return st
	}
	cases := []struct {
		name string
		mut  func(st *composeState)
		want string
	}{
		{"空标题", func(st *composeState) { st.title = "   " }, "标题不能为空"},
		{"标题过长", func(st *composeState) { st.title = strings.Repeat("字", composeTitleMax+1) }, "标题最多"},
		{"正文过长", func(st *composeState) { st.body = strings.Repeat("字", composeContentMax+1) }, "正文最多"},
		{"投票无标题", func(st *composeState) {
			st.voteOpen = true
			st.voteOptions = []string{"甲"}
		}, "投票标题不能为空"},
		{"投票无选项", func(st *composeState) {
			st.voteOpen = true
			st.voteTitle = "选一个"
			st.voteOptions = []string{"", " "}
		}, "投票至少需要 1 个选项"},
		{"投票标题过长", func(st *composeState) {
			st.voteOpen = true
			st.voteTitle = strings.Repeat("长", composeVoteTitleMax+1)
			st.voteOptions = []string{"甲"}
		}, "投票标题最多"},
		{"投票时间格式", func(st *composeState) {
			st.voteOpen = true
			st.voteTitle = "选一个"
			st.voteOptions = []string{"甲"}
			st.voteDeadline = "明天中午"
		}, "时间格式应为"},
		{"竞猜选项不足", func(st *composeState) {
			st.betOpen = true
			st.betTitle = "谁会赢"
			st.betOptions = []string{"A 队"}
		}, "竞猜至少需要 2 个选项"},
		{"竞猜选项过多", func(st *composeState) {
			st.betOpen = true
			st.betTitle = "谁会赢"
			st.betOptions = []string{"a", "b", "c", "d", "e", "f", "g"}
		}, "竞猜最多 6 个选项"},
		{"兑换码块为空", func(st *composeState) { st.redeemOpen = true }, "兑换码块需要"},
		{"兑换码过长", func(st *composeState) {
			st.redeemOpen = true
			st.redeemCodes = strings.Repeat("x", composeCodeTextMax+1)
		}, "单个兑换码最多"},
		{"金币池越界", func(st *composeState) {
			st.poolOpen = true
			st.poolCoins = composePoolCoinsHi + 1
		}, "金币池金额应在"},
		{"金币池概率越界", func(st *composeState) {
			st.poolOpen = true
			st.poolCoins = 100
			st.poolProbability = 0.9
		}, "金币池概率应在"},
	}
	for _, c := range cases {
		st := base()
		c.mut(st)
		_, err := st.payload()
		if err == nil {
			t.Errorf("%s：应当报错但没有", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误 = %q，期望包含 %q", c.name, err.Error(), c.want)
		}
	}
	// 合法的最小表单：只有标题，节点留空也不拦。
	p, err := base().payload()
	if err != nil {
		t.Errorf("最小表单不该报错：%v", err)
	}
	if _, ok := p["node_id"]; ok {
		t.Errorf("没选节点时不该发送 node_id：%v", p)
	}
}

// 节点默认由 AI 判定：级联面板里能清除选择，清除后触发框显示「由 AI 判定
// 分类」，请求体里没有 node_id，也不会在本地报「请选择节点」。
func TestComposeNodeIsOptional(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "不挑节点的帖子"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("手动指定节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("社区 · 社区讨论"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("清除节点选择"); err != nil {
		t.Fatalf("级联面板里没有清除入口：%q", tt.Texts())
	}
	if st.nodeID != "" {
		t.Fatalf("清除后 nodeID = %q", st.nodeID)
	}
	tt.Frame()
	if !tt.HasText("由 AI 判定分类") || !tt.HasText("选填") {
		t.Errorf("清除后没有说明节点由 AI 判定：%q", tt.Texts())
	}
	// 明显的「恢复 AI 判定」入口：不用再进面板就能清掉手动选择。
	st.nodeID = "uuid-work"
	tt.Frame()
	if err := tt.Click("恢复 AI 判定"); err != nil {
		t.Fatalf("找不到恢复 AI 判定入口：%v：%q", err, tt.Texts())
	}
	if st.nodeID != "" {
		t.Errorf("点恢复后 nodeID = %q，期望留空", st.nodeID)
	}
	p, err := st.payload()
	if err != nil {
		t.Fatalf("不选节点不该拦住发布：%v", err)
	}
	if _, ok := p["node_id"]; ok {
		t.Errorf("不选节点时请求体里不该有 node_id：%v", p)
	}
}

// 正文工具条的 Markdown 动作：无选区时插入占位文字并选中；有选区时包住
// 选区；行级动作给每一行加前缀；插入后光标/选区回到正确的位置。
func TestComposeMarkdownToolbar(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		start   int
		end     int
		format  composeFormat
		want    string
		wantSel [2]int
	}{
		{"加粗无选区", "abc", 3, 3, composeBold, "abc**加粗文字**", [2]int{5, 9}},
		{"加粗包住选区", "make me bold", 5, 7, composeBold, "make **me** bold", [2]int{7, 9}},
		{"斜体", "", 0, 0, composeItalic, "*斜体文字*", [2]int{1, 5}},
		{"删除线", "hi", 0, 2, composeStrike, "~~hi~~", [2]int{2, 4}},
		{"行内代码", "x", 1, 1, composeInlineCode, "x`行内代码`", [2]int{2, 6}},
		{"标题", "标题文字", 2, 2, composeHeading, "## 标题文字", [2]int{0, 7}},
		{"引用多行", "a\nb", 0, 3, composeQuote, "> a\n> b", [2]int{0, 7}},
		{"无序列表", "a\nb", 0, 3, composeBulletList, "- a\n- b", [2]int{0, 7}},
		{"有序列表", "a\nb", 0, 3, composeOrderedList, "1. a\n2. b", [2]int{0, 9}},
		{"代码块", "", 0, 0, composeCodeBlock, "```\n代码\n```", [2]int{4, 6}},
		{"链接", "点这里", 0, 3, composeLink, "[点这里](url)", [2]int{1, 4}},
		{"图片", "", 0, 0, composeImage, "![图片说明](url)", [2]int{2, 6}},
		{"分割线", "上文", 2, 2, composeDivider, "上文\n\n---\n\n", [2]int{9, 9}},
	}
	for _, c := range cases {
		st := newComposeState()
		st.body, st.selStart, st.selEnd = c.body, c.start, c.end
		st.applyMarkdown(c.format)
		if st.body != c.want {
			t.Errorf("%s：正文 = %q，期望 %q", c.name, st.body, c.want)
			continue
		}
		if !st.pendingSel.ok || st.pendingSel.start != c.wantSel[0] || st.pendingSel.end != c.wantSel[1] {
			t.Errorf("%s：待恢复选区 = %+v，期望 %v", c.name, st.pendingSel, c.wantSel)
		}
	}
	// 中文与 emoji 按 rune 计算选区，不按字节。
	st := newComposeState()
	st.body = "你好世界"
	st.selStart, st.selEnd = 2, 4
	st.applyMarkdown(composeBold)
	if st.body != "你好**世界**" {
		t.Errorf("rune 选区错误：%q", st.body)
	}
}

// runeSel 会把反向选区与越界选区夹回正文范围。
func TestComposeRuneSelClamps(t *testing.T) {
	st := newComposeState()
	st.body = "abc"
	st.selStart, st.selEnd = 99, -5
	rs, a, b := st.runeSel()
	if string(rs) != "abc" || a != 0 || b != 3 {
		t.Errorf("选区没有夹到范围内：%d %d %q", a, b, string(rs))
	}
}

// 时间字段：本地时间进，ISO 8601 UTC 出；空值不发。
func TestComposeDeadline(t *testing.T) {
	if got, err := composeDeadline("  "); err != nil || got != "" {
		t.Errorf("空值 = %q %v", got, err)
	}
	got, err := composeDeadline("2026-11-20 12:00")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-20 12:00", time.Local)
	if got != want.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Errorf("时间 = %q，期望 %q", got, got)
	}
	if _, err := composeDeadline("2026/11/20"); err == nil {
		t.Errorf("不合法的写法应当报错")
	}
}

// 主页的悬浮按钮（FAB）是发帖入口：它在列表页渲染出来，点一下打开窗口。
func TestComposeFabOpensWindow(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{{Key: "x", Title: "列表项", Subtitle: "作者 · 刚刚"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("发帖") {
		t.Fatalf("主页没有发帖入口：%q", tt.Texts())
	}
	// 绝对定位的元素要等一帧布局稳定下来才命中；真实窗口每帧都重绘。
	tt.Frame()
	if err := tt.Click("发帖"); err != nil {
		t.Fatal(err)
	}
	// 未登录时点 FAB 先走授权，不会直接建窗口。
	if !a.hasPendingAuthorization() || a.authGate.need.Message == "" {
		t.Errorf("未登录点发帖应当登记授权：%+v", a.authGate)
	}
	if !strings.Contains(a.authGate.need.Message, "发帖") {
		t.Errorf("授权说明应提到发帖：%q", a.authGate.need.Message)
	}
	if a.compose == nil {
		t.Errorf("点发帖后应当准备好草稿状态，授权回来才能直接写")
	}
}

// FAB 画在列表之上：它的中心是强调色，周围的页面仍是底色。
func TestComposeFabPaintsAboveList(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{{Key: "x", Title: "列表项"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	r, ok := tt.Find("发帖")
	if !ok {
		t.Fatal("找不到发帖按钮")
	}
	img := tt.Image()
	cx, cy := int(r.X+r.W/2), int(r.Y+r.H/2)
	inside := img.RGBAAt(cx, cy)
	if inside.R == inside.G && inside.G == inside.B {
		t.Errorf("FAB 没有画在列表之上（中心是灰白色 %v）", inside)
	}
	// 按钮左侧紧挨着的仍是页面底色（浅色主题为纯白）。
	if out := img.RGBAAt(int(r.X)-6, cy); out.R != 255 || out.G != 255 || out.B != 255 {
		t.Errorf("FAB 左边缘之外的像素 = %v，期望页面底色", out)
	}
}

// FAB 固定在内容区右下角，不随列表滚动；窗口很窄时也不会被挤出可视区域。
func TestComposeFabStaysBottomRight(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = make([]Item, 30)
	for i := range a.tabs[0].items {
		a.tabs[0].items[i] = Item{Key: string(rune('a' + i%26)), Title: "列表项"}
	}
	a.tabs[0].loaded = true
	a.tabs[0].total = 30
	tt := ui.NewTester(a.view, 1100, 760)
	r, ok := tt.Find("发帖")
	if !ok {
		t.Fatal("找不到发帖按钮")
	}
	if r.X < 700 || r.Y < 620 {
		t.Errorf("FAB 没有贴在右下角：%+v", r)
	}
	// 滚动列表之后位置不变。
	tt.Scroll(float32(r.X), 300, 0, 400)
	r2, ok := tt.Find("发帖")
	if !ok {
		t.Fatal("滚动后找不到发帖按钮")
	}
	if !reflect.DeepEqual(r, r2) {
		t.Errorf("FAB 跟着列表滚动了：%+v → %+v", r, r2)
	}
}

// 发帖窗口：左边写作区放标题、正文与 Markdown 工具条，右边常驻附加组件栏；
// 四个补充字段不再出现，组件字段只在点开弹窗之后才画。
func TestComposeWindowLayout(t *testing.T) {
	a := testApp(t)
	// 登录后底部提示才是「Ctrl+Enter 发布」；否则它会提醒先登录。
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "reader",
		Scope: "read profile write:topic",
	}); err != nil {
		t.Fatal(err)
	}
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	for _, want := range []string{
		"节点", "选填", "默认由 AI 判定", "手动指定节点",
		"标题", "正文", "发布设置", "匿名发帖",
		"加粗", "斜体", "删除线", "代码", "引用", "代码块", "列表", "编号", "链接", "图片", "分割线",
		"编辑", "预览", "附加组件", "已添加 0 / 6 项",
		"投票", "评分", "竞猜", "兑换码 / 红包", "金币池", "可见性（谁可以看）",
		"发布", "Ctrl+Enter 发布",
	} {
		if !tt.HasText(want) {
			t.Errorf("发帖窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	// 匿名开关在右侧控制区，不再和标题挤在第一行。
	if r, ok := tt.Find("匿名发帖"); !ok || r.X < composeWindowWidth-composeSidebarWidth {
		t.Errorf("匿名发帖不在右侧栏：%+v %q", r, tt.Texts())
	}
	// 移除的补充字段不再占版面，也不留在请求体里（见 payload 的断言）。
	for _, gone := range []string{"标题前缀", "提示信息", "联系方式", "状态"} {
		if tt.HasText(gone) {
			t.Errorf("已移除的字段仍画在发帖窗口里：%q", gone)
		}
	}
	// 工具条是「头部」的一个工具栏，可访问名是「编辑工具」。
	if _, ok := tt.AccessByRole(ui.RoleToolbar, "编辑工具"); !ok {
		t.Errorf("正文工具条没有以工具栏角色出现")
	}
	// 弹窗没打开前，一块组件的字段都不画。
	for _, hidden := range []string{"投票类型", "可见范围", "初始金币", "添加此组件"} {
		if tt.HasText(hidden) {
			t.Errorf("组件弹窗没打开，却画出了 %q", hidden)
		}
	}
	// 点右侧栏的一行，它的字段以弹窗形式出现。
	openModule := func(label string, wants ...string) {
		t.Helper()
		// 每项都用一份新草稿：上一次的弹窗不会留着遮住侧栏。
		a.compose = newComposeState()
		composeReady(a)
		it := ui.NewTester(a.composeWindowView, composeWindowWidth, 1100)
		it.Frame()
		if err := it.Click(label); err != nil {
			t.Fatalf("打开 %q 的弹窗失败：%v：%q", label, err, it.Texts())
		}
		it.Frame()
		if !it.HasText("添加此组件") {
			t.Errorf("点 %q 没有打开组件弹窗：%q", label, it.Texts())
		}
		// 移除不该再藏在弹窗里：侧栏行尾直接有入口。
		if it.HasText("移除此组件") {
			t.Errorf("%q 的弹窗里还有隐藏的移除入口：%q", label, it.Texts())
		}
		for _, w := range wants {
			if !it.HasText(w) {
				t.Errorf("%q 的弹窗里缺少 %q：%q", label, w, it.Texts())
			}
		}
	}
	openModule("投票", "投票类型", "单选", "多选", "公开投票结果", "添加选项",
		"批量添加", "删除选中", "投票截止时间")
	openModule("可见性（谁可以看）", "可见范围", "所有人可见", "门槛由服务端判定")
	openModule("金币池", "初始金币", "中奖概率（0 = 默认）")
	openModule("兑换码 / 红包", "兑换码（一行一个）", "批量粘贴", "清空兑换码",
		"每人可领", "红包上限", "同时开红包")
	openModule("评分", "评分对象", "分值", "理由")
	openModule("竞猜", "竞猜标题", "参与截止时间")
}

// 附加组件在右侧栏点开弹窗里编辑：添加后随帖发送、侧栏显示已添加、结果用
// 窗口内 toast 报告。移除不用再进弹窗：侧栏行尾直接点，只是关掉开关，
// 填过的内容仍留在草稿里。
func TestComposeModuleDialogAddRemove(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("投票"); err != nil {
		t.Fatal(err)
	}
	if !st.moduleOpen || st.moduleIndex != 0 {
		t.Fatalf("没有打开投票弹窗：%+v", st)
	}
	tt.Frame()
	if err := tt.Click("添加此组件"); err != nil {
		t.Fatalf("添加组件失败：%v：%q", err, tt.Texts())
	}
	if !st.voteOpen || st.moduleOpen {
		t.Errorf("添加后状态不对：voteOpen=%v moduleOpen=%v", st.voteOpen, st.moduleOpen)
	}
	tt.Frame()
	if !tt.HasText("已添加 1 / 6 项") {
		t.Errorf("侧栏没有显示已添加的组件：%q", tt.Texts())
	}
	if !tt.HasText("已添加投票") {
		t.Errorf("添加结果没有用 toast 报告：%q", tt.Texts())
	}
	tt.Advance(6 * time.Second)
	if tt.HasText("已添加投票") {
		t.Errorf("toast 没有自己消失")
	}

	// 移除就在侧栏行尾，不必再打开弹窗；填过的字段不会丢。
	st.voteTitle = "留着的草稿"
	if st.moduleOpen {
		t.Errorf("移除前不该还开着弹窗")
	}
	if err := tt.Click("移除投票"); err != nil {
		t.Fatalf("侧栏里没有直接移除入口：%v：%q", err, tt.Texts())
	}
	if st.voteOpen || st.moduleOpen {
		t.Errorf("移除后状态不对：voteOpen=%v moduleOpen=%v", st.voteOpen, st.moduleOpen)
	}
	if st.voteTitle != "留着的草稿" {
		t.Errorf("移除组件不该清掉填过的内容：%q", st.voteTitle)
	}
	tt.Frame()
	if !tt.HasText("已移除投票") {
		t.Errorf("移除结果没有用 toast 报告：%q", tt.Texts())
	}
}

// 工具条按钮真的会改正文，并且会把选区放在插入的内容上（不用再手动挪光标）。
func TestComposeToolbarEditsBody(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	// 点进正文输入框再打字：选区是真实输入产生的，不是测试自己塞进去的。
	if err := tt.Click("正文内容"); err != nil {
		t.Fatal(err)
	}
	tt.Type("你好")
	if st.body != "你好" {
		t.Fatalf("打字没有进正文：%q", st.body)
	}
	tt.Click("加粗")
	if st.body != "你好**加粗文字**" {
		t.Fatalf("加粗没有写进正文：%q", st.body)
	}
	if st.selStart != 4 || st.selEnd != 8 {
		t.Errorf("加粗后选区 = %d..%d，期望 4..8", st.selStart, st.selEnd)
	}
	tt.Click("分割线")
	if !strings.Contains(st.body, "---") {
		t.Errorf("分割线没有写进正文：%q", st.body)
	}
}

// 工具条与视图切换只画图标：名字留在无障碍名里（读屏与点击靠它），说明放在
// 悬浮提示里——鼠标停在「加粗」上才给出它会插进正文的标记。按钮宽度正好是
// 一个图标加一圈内边距，画了文字就会宽出去。
func TestComposeEditorIconsWithTooltips(t *testing.T) {
	a := testApp(t)
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	for _, name := range []string{"加粗", "分割线", "编辑", "预览", "表情", "上传图片"} {
		r, ok := tt.Find(name)
		if !ok {
			t.Fatalf("找不到 %q：%q", name, tt.Texts())
		}
		if want := editorIconSize + 2*editorIconPadX; r.W > want+0.5 {
			t.Errorf("%q 画了文字，宽 %v 超过纯图标按钮的 %v", name, r.W, want)
		}
	}
	// 悬浮说明要等指针停留一会儿才出现，移开就收起。
	rest := func(x, y float32) {
		tt.Move(x, y)
		tt.Advance(time.Second)
		tt.Frame()
	}
	r, _ := tt.Find("加粗")
	// 图标按钮没有边框，鼠标扫过要给一圈浅底，否则一排黑图标看不出哪儿能点。
	probe := func() color.RGBA {
		return tt.Image().RGBAAt(int(r.X+3), int(r.Y+r.H/2))
	}
	plain := probe()
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if probe() == plain {
		t.Errorf("悬停在加粗上没有底色：%v", plain)
	}
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("加粗：**加粗**") {
		t.Errorf("悬浮提示没有说明加粗：%q", tt.Texts())
	}
	rest(5, 5)
	if tt.HasText("加粗：**加粗**") {
		t.Errorf("移开后悬浮提示还挂着：%q", tt.Texts())
	}
}

// 预览用与详情页相同的 Markdown 渲染；空正文给出提示而不是一块空白。
func TestComposePreview(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Click("预览")
	if !tt.HasText("还没有正文") {
		t.Errorf("空正文预览没有提示：%q", tt.Texts())
	}
	st.body = "# 一级标题\n\n正文**加粗**"
	tt.Frame()
	if !tt.HasText("一级标题") {
		t.Errorf("预览没有渲染标题：%q", tt.Texts())
	}
	if tt.HasText("**加粗**") {
		t.Errorf("预览把 Markdown 源码当纯文字显示了")
	}
}

// 发表成功之后窗口显示回执，并且能直接打开刚发的帖子。
func TestComposeSuccessOpensCreatedTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, envelopeJSON(`{"short_id":"Zz9","node":{"slug":"community"}}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "回执测试"
	var done sync.WaitGroup
	done.Add(1)
	a.onPostPublished = func(created createdTopic, err error) {
		if err != nil {
			t.Errorf("发表失败：%v", err)
		}
		done.Done()
	}
	a.submitCompose()
	done.Wait()

	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	if !tt.HasText("已发表：回执测试") {
		t.Errorf("没有显示发表回执：%q", tt.Texts())
	}
	// 回执在表单最下面，先滚到底再点。
	tt.Scroll(400, 500, 0, 4000)
	if err := tt.Click("在应用内打开"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "Zz9" {
		t.Errorf("没有打开刚发的帖子：%s %+v", a.pageName, a.detail)
	}
}

// 服务端拒绝时把原因摆出来，草稿与幂等键都保留：改完再点发布，重试用同一个键。
func TestComposeFailureKeepsDraftAndKey(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"c":403,"code":"permission_denied","m":"你暂时不能发帖","d":null,"t":0}`)
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "会被拒绝的帖子"
	st.body = "正文"

	var done sync.WaitGroup
	done.Add(1)
	a.onPostPublished = func(created createdTopic, err error) {
		if err == nil {
			t.Errorf("应当失败")
		}
		done.Done()
	}
	a.submitCompose()
	done.Wait()

	if st.err != "你暂时不能发帖" {
		t.Errorf("错误文案 = %q，期望服务端原话", st.err)
	}
	if st.title != "会被拒绝的帖子" || st.body != "正文" {
		t.Errorf("失败后草稿被清空了：%q %q", st.title, st.body)
	}
	if st.key == "" {
		t.Fatalf("失败后应当保留幂等键，便于用同一个键重试")
	}
	first := st.key

	done = sync.WaitGroup{}
	done.Add(1)
	a.submitCompose()
	done.Wait()
	if len(keys) != 2 || keys[0] != first || keys[1] != first {
		t.Errorf("重试没有沿用同一个幂等键：%v（首次 %s）", keys, first)
	}

	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	if !tt.HasText("你暂时不能发帖") {
		t.Errorf("窗口没有显示失败原因：%q", tt.Texts())
	}
}

// 缺 write:topic 时先走全局授权弹窗，而不是当成普通错误；授权成功后按同一
// 份内容自动重发。
func TestComposeMissingScopeGoesToAuthGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围：write:topic",`+
			`"required_scope":["write:topic"],"d":null,"t":0}`)
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "需要授权"

	var done sync.WaitGroup
	done.Add(1)
	a.onPostPublished = func(created createdTopic, err error) { done.Done() }
	a.submitCompose()
	done.Wait()

	if !a.authGate.open {
		t.Fatalf("缺 scope 时没有打开授权弹窗：%+v", a.authGate)
	}
	if got := a.authGate.need.Scopes; len(got) != 1 || got[0] != "write:topic" {
		t.Errorf("授权弹窗的 scope = %v", got)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("应当登记一次重发：%d", len(a.authGate.replays))
	}
	if st.title != "需要授权" {
		t.Errorf("等授权期间草稿不该丢：%q", st.title)
	}
}

// 兑换码框支持换行、逗号、中文逗号与分号分隔，空项被丢掉。
func TestComposeRedeemCodesParsed(t *testing.T) {
	st := newComposeState()
	st.redeemCodes = "A-1\n B-2 ,C-3，D-4;E-5；\n\n"
	got := st.redeemCodeList()
	want := []string{"A-1", "B-2", "C-3", "D-4", "E-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("兑换码 = %v，期望 %v", got, want)
	}
	if st2 := newComposeState(); len(st2.redeemCodeList()) != 0 {
		t.Errorf("空输入应当解析成空数组")
	}
}

// 匿名开关与默认值：新表单默认不匿名、可见性为所有人、金币池初始 100。
func TestNewComposeStateDefaults(t *testing.T) {
	st := newComposeState()
	if st.anonymous || st.guardChoice != guardTypeLabels[0] || st.poolCoins != 100 {
		t.Errorf("默认值不对：%+v", st)
	}
	if voteTypeAt(st.voteTypeIndex) != "pk" {
		t.Errorf("投票默认应为单选")
	}
	if redPacketAt(st.redPacketIndex) != 88 {
		t.Errorf("红包默认应为 88")
	}
	if len(st.voteOptions) < 1 || len(st.betOptions) < composeBetOptionsMin {
		t.Errorf("选项初始行数不对：%v %v", st.voteOptions, st.betOptions)
	}
}

// 投票与竞猜的选项是动态行：在组件弹窗里能加、能删，且卡在文档给的上下限上。
func TestComposeOptionRowsAddRemove(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.voteOpen = true
	st.voteOptions = []string{"甲"}
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 1100)
	tt.Frame()
	if err := tt.Click("投票"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("添加选项"); err != nil {
		t.Fatalf("弹窗里找不到添加选项：%v：%q", err, tt.Texts())
	}
	if len(st.voteOptions) != 2 {
		t.Fatalf("添加后行数 = %d，期望 2", len(st.voteOptions))
	}
	// 只剩一行时「删除」被禁用，避免删成空列表。
	st.voteOptions = []string{"甲"}
	tt.Frame()
	if info, ok := tt.Access("删除"); ok && !info.Disabled {
		t.Errorf("只剩一个选项时删除按钮应当禁用")
	}
	// 到上限后不再加行。
	st.voteOptions = make([]string, composeOptionMax)
	tt.Frame()
	if info, ok := tt.Access("添加选项"); ok && !info.Disabled {
		t.Errorf("选项到 %d 个后添加按钮应当禁用", composeOptionMax)
	}
}

// 「清空」把草稿与各组件一起重置，但保留节点选择。
func TestComposeClearKeepsNode(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "写了一半"
	st.body = "正文"
	st.voteOpen = true
	st.voteTitle = "投票"
	st.nodeID = "uuid-work"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	if st.title != "" || st.body != "" || st.voteOpen || st.voteTitle != "" {
		t.Errorf("清空后仍有残留：%+v", st)
	}
	if st.nodeID != "uuid-work" {
		t.Errorf("清空不该丢掉节点选择：%q", st.nodeID)
	}
}

// 窗口缩到最小尺寸时，工具条与底部按钮都还在（放不下的工具按钮收进菜单）。
func TestComposeWindowMinimumSize(t *testing.T) {
	a := testApp(t)
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWinMinWidth, composeWinMinHeight)
	tt.Frame()
	for _, want := range []string{"节点", "标题", "正文", "编辑", "预览", "发布", "清空"} {
		if _, ok := tt.Find(want); !ok {
			t.Errorf("最小窗口下找不到 %q", want)
		}
	}
	// 工具条本身还在，放不下的按钮由它的溢出菜单接管。
	if _, ok := tt.AccessByRole(ui.RoleToolbar, "编辑工具"); !ok {
		t.Errorf("最小窗口下工具条消失了")
	}
}

// 发帖窗口是独立窗口：主窗口关掉时一起关，退出登录时丢弃草稿。
func TestComposeWindowLifecycle(t *testing.T) {
	a := testApp(t)
	// 无窗口（headless）时打开只准备状态，不建窗口。
	a.showComposeWindow()
	if a.compose == nil {
		t.Fatal("打开发帖窗口后应当有草稿状态")
	}
	a.compose.title = "上一个账号的草稿"
	// 退出登录：草稿属于上一个账号，一并丢弃。
	a.onAuthChanged(nil)
	if a.compose != nil {
		t.Errorf("退出登录后应当丢弃草稿：%+v", a.compose)
	}
}

// 幂等键：同一份内容重试沿用同一个键（不会重复发帖），改过内容就换新键
// （同一个键配不同请求体会被服务端判成 409 idempotency_key_reuse）。
func TestComposeIdempotencyKeyFollowsPayload(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"c":500,"code":"internal_error","m":"服务端错误","d":null,"t":0}`)
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "标题"

	submit := func() {
		done := make(chan struct{})
		a.onPostPublished = func(created createdTopic, err error) { close(done) }
		a.submitCompose()
		<-done
	}

	submit()
	first := st.key
	submit() // 内容没变：沿用同一个键
	if st.key != first {
		t.Errorf("同一份内容重试换了键：%s → %s", first, st.key)
	}
	st.body = "补了一段正文"
	submit()
	if st.key == first {
		t.Errorf("改过内容后必须换新键，仍是 %s", first)
	}
	if len(keys) != 3 || keys[0] != first || keys[1] != first || keys[2] == first {
		t.Errorf("幂等键序列不对：%v（首个 %s）", keys, first)
	}
}

// FAB 是浮在内容之上的，不占列表的高度：滚到底时最后一行仍然延伸到窗口
// 底部，与按钮处在同一条横带上。
func TestComposeFabFloatsOverContent(t *testing.T) {
	a := testApp(t)
	for i := 0; i < 40; i++ {
		a.tabs[0].items = append(a.tabs[0].items, Item{
			Key:   "k" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Title: "第 " + string(rune('0'+i/10)) + string(rune('0'+i%10)) + " 项",
		})
	}
	a.tabs[0].loaded = true
	a.tabs[0].total = len(a.tabs[0].items)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	// 滚到底，最后一行应当贴近窗口底部（只差列表自己的下内边距与行间距）。
	tt.Scroll(500, 400, 0, 20000)
	tt.Frame()
	last, ok := tt.Find("第 39 项")
	if !ok {
		t.Fatalf("滚到底后找不到最后一行：%q", tt.Texts())
	}
	if bottom := last.Y + last.H; bottom < 760-50 {
		t.Errorf("列表没有铺到窗口底部（最后一行底边 %v），说明 FAB 占了高度", bottom)
	}
	// 按钮与最后一行在同一条横带上：FAB 是浮起来的，不是列表下面的一块。
	fab, ok := tt.Find("发帖")
	if !ok {
		t.Fatal("找不到发帖按钮")
	}
	if last.Y > fab.Y+fab.H || last.Y+last.H < fab.Y {
		t.Errorf("最后一行 %+v 与 FAB %+v 没有重叠，FAB 没有浮在内容上", last, fab)
	}
}

// 新草稿默认不指定节点：不预选、不画级联选择、不主动拉节点列表，请求体里
// 也没有 node_id，发帖后由站点 AI 判定分类。
func TestComposeDefaultsToAIClassification(t *testing.T) {
	a := testApp(t)
	st := newComposeState()
	a.compose = st
	if st.nodeID != "" || st.nodeManual {
		t.Fatalf("默认不该指定节点：%+v", st)
	}
	st.title = "让 AI 归类的帖子"
	p, err := st.payload()
	if err != nil {
		t.Fatalf("最小表单不该报错：%v", err)
	}
	if _, ok := p["node_id"]; ok {
		t.Errorf("默认不该发送 node_id：%v", p)
	}
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if !tt.HasText("发帖后由 AI 自动判定分类，无需选择。") {
		t.Errorf("没有说明 AI 判定：%q", tt.Texts())
	}
	if _, ok := tt.Find("由 AI 判定分类"); ok {
		t.Errorf("默认不该画出节点级联选择：%q", tt.Texts())
	}
	if st.nodeLoading || len(st.nodes) != 0 {
		t.Errorf("默认不该为节点列表发请求：loading=%v nodes=%d",
			st.nodeLoading, len(st.nodes))
	}
}

// 节点选择是按需展开的次要入口，收起后界面上只剩一行说明。
func TestComposeNodeManualToggle(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("手动指定节点"); err != nil {
		t.Fatalf("找不到手动指定入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if !st.nodeManual {
		t.Fatalf("展开状态没记下：%+v", st)
	}
	if _, ok := tt.Find("社区 · 社区讨论"); !ok {
		t.Errorf("展开后没有级联选择的触发框：%q", tt.Texts())
	}
	if err := tt.Click("收起节点"); err != nil {
		t.Fatalf("找不到收起入口：%v：%q", err, tt.Texts())
	}
	if st.nodeManual {
		t.Errorf("收起后 nodeManual 仍为真")
	}
	tt.Frame()
	if _, ok := tt.Find("社区 · 社区讨论"); ok {
		t.Errorf("收起后仍画着级联选择：%q", tt.Texts())
	}
}

// 匿名开关在右侧发布设置里，点一下就把 is_anonymous 带进请求体。
func TestComposeAnonymousSidebarToggle(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "匿名测试"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("匿名发帖"); err != nil {
		t.Fatalf("右侧栏没有匿名开关：%v：%q", err, tt.Texts())
	}
	if !st.anonymous {
		t.Fatalf("点匿名开关没有改状态")
	}
	p, err := st.payload()
	if err != nil || p["is_anonymous"] != true {
		t.Errorf("匿名没有进请求体：%v %v", p, err)
	}
}

// 「全部移除」是组件的批量移除入口，一次把所有开关关掉，填过的内容保留。
func TestComposeRemoveAllModules(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.voteOpen, st.scoreOpen, st.poolOpen = true, true, true
	st.voteTitle = "留着的草稿"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("全部移除附加组件"); err != nil {
		t.Fatalf("侧栏没有全部移除入口：%v：%q", err, tt.Texts())
	}
	if st.enabledModuleCount() != 0 {
		t.Errorf("全部移除后仍开着 %d 项", st.enabledModuleCount())
	}
	if st.voteTitle != "留着的草稿" {
		t.Errorf("批量移除不该清掉填过的内容：%q", st.voteTitle)
	}
	tt.Frame()
	if !tt.HasText("已移除全部附加组件") {
		t.Errorf("批量移除结果没有用 toast 报告：%q", tt.Texts())
	}
}

// 投票 / 竞猜 / 兑换码都有批量入口：一次粘贴多行加满选项，勾选后一次删掉。
func TestComposeBatchAddVoteOptions(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.voteOpen = true
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 1100)
	tt.Frame()
	if err := tt.Click("投票"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("批量添加"); err != nil {
		t.Fatalf("投票没有批量添加入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	st.voteBatchText = "甲\n乙\n丙\n"
	tt.Frame()
	if err := tt.Click("加入选项"); err != nil {
		t.Fatalf("加入选项失败：%v：%q", err, tt.Texts())
	}
	if !reflect.DeepEqual(st.voteOptions, []string{"甲", "乙", "丙"}) {
		t.Errorf("批量粘贴后的选项 = %v", st.voteOptions)
	}
	if st.voteBatchOpen || st.voteBatchText != "" {
		t.Errorf("批量录入区没有收起：%+v", st)
	}
	// 勾选两行再一次删掉：至少留一行，勾选状态跟着重置。
	if err := tt.Click("勾选投票选项 1"); err != nil {
		t.Fatalf("找不到行首勾选框：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("勾选投票选项 3"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("删除选中"); err != nil {
		t.Fatalf("找不到批量删除入口：%v：%q", err, tt.Texts())
	}
	if !reflect.DeepEqual(st.voteOptions, []string{"乙"}) {
		t.Errorf("批量删除后的选项 = %v", st.voteOptions)
	}
	for _, on := range st.voteChecked {
		if on {
			t.Errorf("批量删除后勾选状态没清空：%v", st.voteChecked)
		}
	}
}

// 竞猜的批量入口同样卡在 2～6 项：一次全选也只会删到下限。
func TestComposeBatchBetOptionsKeepMinimum(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.betOpen = true
	st.betOptions = []string{"A", "B", "C", "D"}
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 1100)
	tt.Frame()
	if err := tt.Click("竞猜"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("批量添加"); err != nil {
		t.Fatalf("竞猜没有批量添加入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	st.betChecked = []bool{true, true, true, true}
	tt.Frame()
	if err := tt.Click("删除选中"); err != nil {
		t.Fatalf("找不到批量删除入口：%v：%q", err, tt.Texts())
	}
	if len(st.betOptions) != composeBetOptionsMin {
		t.Errorf("批量删除应保留 %d 项，实际 %v", composeBetOptionsMin, st.betOptions)
	}
	tt.Frame()
	if !tt.HasText("至少保留 2 个") {
		t.Errorf("触到下限时没有说明原因：%q", tt.Texts())
	}
}

// 兑换码也有批量粘贴与一次清空：空行丢掉，已有内容不被覆盖。
func TestComposeBatchRedeemCodes(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.redeemOpen = true
	st.redeemCodes = "OLD-1"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 1100)
	tt.Frame()
	if err := tt.Click("兑换码 / 红包"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("批量粘贴兑换码"); err != nil {
		t.Fatalf("兑换码没有批量入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	st.redeemBatchText = "A-1\nB-2\n\nC-3"
	tt.Frame()
	if err := tt.Click("追加兑换码"); err != nil {
		t.Fatalf("追加失败：%v：%q", err, tt.Texts())
	}
	want := []string{"OLD-1", "A-1", "B-2", "C-3"}
	if got := st.redeemCodeList(); !reflect.DeepEqual(got, want) {
		t.Errorf("批量粘贴后的兑换码 = %v，期望 %v", got, want)
	}
	tt.Frame()
	if err := tt.Click("清空兑换码"); err != nil {
		t.Fatalf("兑换码没有清空入口：%v：%q", err, tt.Texts())
	}
	if st.redeemCodes != "" {
		t.Errorf("清空后仍留着兑换码：%q", st.redeemCodes)
	}
}

// 批量辅助函数：拆行、上限截断、下限保留都按文档给的上下限来。
func TestComposeBatchHelpers(t *testing.T) {
	lines := composeBatchLines(" 甲 \n\n乙\r\n丙")
	if !reflect.DeepEqual(lines, []string{"甲", "乙", "丙"}) {
		t.Errorf("拆行 = %v", lines)
	}
	// 默认两行都是空行时，粘贴内容直接替换占位行。
	next, added, skipped := appendComposeBatch([]string{"", ""}, lines, 1, 3)
	if !reflect.DeepEqual(next, []string{"甲", "乙", "丙"}) || added != 3 || skipped != 0 {
		t.Errorf("批量追加 = %v %d %d", next, added, skipped)
	}
	// 超过上限的部分省略，但不会把已有内容挤掉。
	next, added, skipped = appendComposeBatch([]string{"旧"}, []string{"a", "b", "c", "d"}, 1, 3)
	if !reflect.DeepEqual(next, []string{"旧", "a", "b"}) || added != 2 || skipped != 2 {
		t.Errorf("超上限 = %v %d %d", next, added, skipped)
	}
	// 一次删掉勾中的行，下限之内不再删，勾选状态全部清空。
	opts, flags, removed, held := removeComposeChecked(
		[]string{"a", "b", "c"}, []bool{true, false, true}, 2)
	if !reflect.DeepEqual(opts, []string{"b", "c"}) || removed != 1 || !held {
		t.Errorf("批量删除 = %v %v %d %v", opts, flags, removed, held)
	}
	for _, on := range flags {
		if on {
			t.Errorf("批量删除后勾选没清空：%v", flags)
		}
	}
}

// ------------------------------------------------------------------
// 小工具
// ------------------------------------------------------------------

func mapField(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("缺少对象字段 %s：%v", key, m)
	}
	return v
}

func assertField(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	if got, ok := m[key]; !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("字段 %s = %#v，期望 %#v", key, got, want)
	}
}
