package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 两个名单端点的路径、列表字段与容错解码。文档里的列表字段都是 items，
// 不能套用通用「取 items / posts / list」的猜法。
func TestMentionEndpointContract(t *testing.T) {
	var paths atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/meta/guardians":
			fmt.Fprint(w, envelopeJSON(`{"items":[{"id":"g1","username":"Jimmy","unknown":1},`+
				`{"id":"g2","username":"守护者"}],"total":2}`))
		case "/api/v1/topics/JBYGf2A/participants":
			fmt.Fprint(w, envelopeJSON(`{"items":[{"id":"u1","username":"alice",`+
				`"avatar_url":"https://x/a.png","is_alias":false},`+
				`{"id":"u2","username":"小号","is_alias":true,"new_field":"忽略我"}],"total":2}`))
		default:
			t.Errorf("请求了未约定的路径：%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}

	gs, err := c.fetchGuardians(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := paths.Load(); got != "/api/v1/meta/guardians" {
		t.Errorf("守护者路径 = %v", got)
	}
	if len(gs.People) != 2 || gs.Total != 2 || gs.People[0].Username != "Jimmy" {
		t.Fatalf("守护者名单解析错误：%+v", gs)
	}

	ps, err := c.fetchParticipants(t.Context(), "JBYGf2A")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths.Load(); got != "/api/v1/topics/JBYGf2A/participants" {
		t.Errorf("参与人路径 = %v", got)
	}
	if len(ps.People) != 2 || ps.Total != 2 {
		t.Fatalf("参与人名单解析错误：%+v", ps)
	}
	if ps.People[0].Avatar != "https://x/a.png" || ps.People[0].IsAlias {
		t.Errorf("参与人字段解析错误：%+v", ps.People[0])
	}
	if ps.People[1].Username != "小号" || !ps.People[1].IsAlias {
		t.Errorf("分身字段解析错误：%+v", ps.People[1])
	}
}

// 正文拼串规则：@ 后必须补空格；邮箱里的 @ 不算；大小写不敏感；
// 自己（含换用的身份）要排掉；单条最多 50 个会通知。
func TestMentionTextRules(t *testing.T) {
	if got := mentionAt(" jimmy "); got != "@jimmy " {
		t.Errorf("mentionAt = %q，期望 @jimmy ", got)
	}
	if got := mentionAt("@jimmy"); got != "@jimmy " {
		t.Errorf("mentionAt 去重 @ 出错：%q", got)
	}
	if mentionKey(" Jimmy ") != "jimmy" || mentionKey("@JIMMY") != "jimmy" {
		t.Error("用户名比较没有忽略大小写 / @")
	}
	cases := []struct {
		body string
		want int
	}{
		{"", 0},
		{"@jimmy 你好", 1},
		{"@a @b @c", 3},
		{"jimmy@example.com 里有一个 @", 0},
		{"@jimmy，你好", 1}, // 客户端按保守口径计名额
		{"结尾的 @", 0},
	}
	for _, tc := range cases {
		if got := countMentions(tc.body); got != tc.want {
			t.Errorf("countMentions(%q) = %d，期望 %d", tc.body, got, tc.want)
		}
	}
	if got := mentionSlots(strings.Repeat("@a ", 49)); got != 1 {
		t.Errorf("剩余名额 = %d，期望 1", got)
	}
	if got := mentionSlots(strings.Repeat("@a ", 60)); got != 0 {
		t.Errorf("超额剩余名额 = %d，期望 0", got)
	}

	people := []mentionPerson{
		{Username: "jimmy"}, {Username: "Jimmy"}, // 自己 + 大小写重名
		{Username: "bystander"}, {Username: "alice"},
	}
	exclude := map[string]bool{"jimmy": true, "bystander": true}
	if got := buildMentions(people, exclude, 50); got != "@alice " {
		t.Errorf("buildMentions = %q，期望 @alice ", got)
	}
	// 名额只剩 1 时不能因为传了整份名单就突破 50 个上限。
	if got := buildMentions([]mentionPerson{{Username: "a"}, {Username: "b"}}, nil, 1); got != "@a " {
		t.Errorf("buildMentions 没有按名额截断：%q", got)
	}
	if got := pickRandomMention(people, exclude); got != "alice" {
		t.Errorf("随机挑人排掉自己后只剩 alice，实际 %q", got)
	}
	if got := pickRandomMention([]mentionPerson{{Username: "jimmy"}}, exclude); got != "" {
		t.Errorf("只剩自己时应隐藏入口，实际 %q", got)
	}
}

// 评论框里点开 @ 气泡：名单取一次，能逐人插入，也能拼出 @所有人 /
// @随机守护者。名单里包含自己时拼串要排掉。
func mentionUIApp(t *testing.T, server *httptest.Server) (*app, *ui.Tester) {
	t.Helper()
	a := openTestDetail(singleAccountWindow(t, "jimmy"))
	a.autoLoad = true
	a.client = &Client{HTTP: server.Client(), Base: server.URL + "/api", Auth: a.auth}
	// 让评论框展开，@ 入口才会出现；草稿保持为空便于断言插入结果。
	a.detail.replyTo.ID = "r1"
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	return a, tt
}

func mentionTestServer(t *testing.T, guardians, participants string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/meta/guardians":
			calls.Add(1)
			fmt.Fprint(w, envelopeJSON(guardians))
		case "/api/v1/topics/JBYGf2A/participants":
			calls.Add(1)
			fmt.Fprint(w, envelopeJSON(participants))
		default:
			// 详情页的其它加载（徽章等）也落在这个假服务上，但不算名单请求。
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func waitMentionLoaded(t *testing.T, a *app) {
	t.Helper()
	waitFor(t, func() bool {
		return a.detail.mentions.guardiansLoaded && a.detail.mentions.participantsLoaded
	}, "名单没有加载回来")
}

// clearMentionDraft 用退格把草稿里的提及整块清掉，保持输入控件与状态同步。
func clearMentionDraft(t *testing.T, a *app, tt *ui.Tester) {
	t.Helper()
	for i := 0; i < 10 && a.detail.draft != ""; i++ {
		tt.Key(0, ui.KeyBackspace)
		tt.Frame()
	}
	if a.detail.draft != "" {
		t.Fatalf("草稿没清干净：%q", a.detail.draft)
	}
}

// typeAt 聚焦评论框并键入 @，打开光标处的候选面板。
func typeAt(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点不到评论输入框：%v", err)
	}
	tt.Type("@")
	tt.Frame()
}

func TestMentionPanelInsertText(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[{"id":"g1","username":"jimmy"},{"id":"g2","username":"守护姐姐"}],"total":2}`,
		`{"items":[{"id":"u1","username":"jimmy"},{"id":"u2","username":"alice"},`+
			`{"id":"u3","username":"小号","is_alias":true}],"total":3}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)

	openAt := func() {
		if err := tt.Click("写评论"); err != nil {
			t.Fatalf("点不到评论输入框：%v", err)
		}
		tt.Type("@")
		tt.Frame()
	}
	openAt()
	waitMentionLoaded(t, a)
	tt.Frame()
	if _, ok := tt.Find("提及候选面板"); !ok {
		t.Fatalf("气泡没有打开：%q", tt.Texts())
	}
	if _, ok := tt.Find("@提及"); ok {
		t.Errorf("工具条上不该还有 @ 入口：%q", tt.Texts())
	}
	if _, ok := tt.Find("@所有人"); !ok {
		t.Fatalf("气泡没有列出 @所有人：%q", tt.Texts())
	}
	if _, ok := tt.Find("@随机守护者"); !ok {
		t.Fatalf("气泡没有列出 @随机守护者：%q", tt.Texts())
	}
	quickRect, ok := tt.Find("@抽奖助手")
	if !ok {
		t.Fatalf("气泡没有列出 @抽奖助手：%q", tt.Texts())
	}
	// 三个快捷项连成列表最前面三行（顺序固定），用户行排在它们后面。
	allRect, ok := tt.Find("@所有人")
	if !ok {
		t.Fatalf("列表里找不到 @所有人：%q", tt.Texts())
	}
	guardianRect, ok := tt.Find("@随机守护者")
	if !ok {
		t.Fatalf("列表里找不到 @随机守护者：%q", tt.Texts())
	}
	aliceRect, ok := tt.Find("@alice")
	if !ok {
		t.Fatalf("列表里找不到 @alice：%q", tt.Texts())
	}
	if !(allRect.Y < guardianRect.Y && guardianRect.Y < quickRect.Y && quickRect.Y < aliceRect.Y) {
		t.Errorf("快捷行没有排在用户列表最前面：%v / %v / %v / %v",
			allRect, guardianRect, quickRect, aliceRect)
	}
	_, aliceShown := tt.Find("@alice")
	_, aliasShown := tt.Find("@小号")
	if !aliceShown || !aliasShown {
		t.Fatalf("气泡没有列出参与人：%q", tt.Texts())
	}

	// 逐人插入：用户名后自动补空格。
	if err := tt.Click("@alice"); err != nil {
		t.Fatalf("点不到参与人行：%v", err)
	}
	if a.detail.draft != "@alice " {
		t.Errorf("逐人插入的正文 = %q，期望 @alice ", a.detail.draft)
	}

	// @所有人：排掉自己，保留分身名；一次拼进正文。每一轮用退格把上一轮
	// 插入的提及整块清掉，回到空草稿（不能绕过输入控件直接改草稿）。
	clearMentionDraft(t, a, tt)
	openAt()
	tt.Frame()
	if err := tt.Click("@所有人"); err != nil {
		t.Fatalf("点不到 @所有人：%v", err)
	}
	if a.detail.draft != "@alice @小号 " {
		t.Errorf("@所有人 拼串 = %q，期望 @alice @小号 ", a.detail.draft)
	}

	// @随机守护者：只排除自己，剩下唯一一位守护者。
	clearMentionDraft(t, a, tt)
	openAt()
	tt.Frame()
	if err := tt.Click("@随机守护者"); err != nil {
		t.Fatal(err)
	}
	if a.detail.draft != "@守护姐姐 " {
		t.Errorf("随机守护者拼串 = %q，期望 @守护姐姐 ", a.detail.draft)
	}

	// @抽奖助手：默认补全「抽 1 个」，使用者在正文里改数字就能改人数。
	clearMentionDraft(t, a, tt)
	openAt()
	tt.Frame()
	if err := tt.Click("@抽奖助手"); err != nil {
		t.Fatalf("点不到 @抽奖助手：%v", err)
	}
	if a.detail.draft != "@抽奖助手 抽 1 个 " {
		t.Errorf("抽奖助手拼串 = %q，期望 @抽奖助手 抽 1 个 ", a.detail.draft)
	}
	// 名单全程只取一次：反复开关气泡、插入都没有重复请求。
	if n := calls.Load(); n != 2 {
		t.Errorf("名单请求次数 = %d，期望两个端点各一次", n)
	}
}

// 抽奖助手快捷行只给自己的帖子：服务端只认帖主发的一级评论，别人的帖子和
// 匿名帖写了也不开奖，不该摆一个点了没反应的入口。其它快捷行不受影响。
func TestLotteryQuickEntryOnlyForOwnPost(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[{"id":"g2","username":"守护姐姐"}],"total":1}`,
		`{"items":[{"id":"u1","username":"jimmy"},{"id":"u2","username":"alice"}],"total":2}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)
	typeAt(t, tt)
	waitMentionLoaded(t, a)
	tt.Frame()
	if !tt.HasText("@抽奖助手") {
		t.Fatalf("自己的帖子没有抽奖助手快捷行：%q", tt.Texts())
	}

	for _, author := range []string{"alice", "匿名"} {
		a.detail.topic.Author = author
		tt.Frame()
		if tt.HasText("@抽奖助手") {
			t.Errorf("作者为 %q 时不该出现抽奖助手快捷行：%q", author, tt.Texts())
		}
		if !tt.HasText("@所有人") || !tt.HasText("@随机守护者") {
			t.Errorf("作者不是自己时其它快捷行应保留：%q", tt.Texts())
		}
	}
}

// 键入 @ 应当直接弹出候选面板，并按已输入的前缀过滤；选中时把 @前缀
// 整段替换成 @名字 （而不是在后面追加，留下两个 @）。
func TestMentionOpensOnTypingAt(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[{"id":"g2","username":"守护姐姐"}],"total":1}`,
		`{"items":[{"id":"u1","username":"jimmy"},{"id":"u2","username":"alice"},`+
			`{"id":"u3","username":"小号","is_alias":true}],"total":3}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)

	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点不到评论输入框：%v", err)
	}
	tt.Type("@")
	tt.Frame()
	waitMentionLoaded(t, a)
	tt.Frame()
	if _, ok := tt.Find("提及候选面板"); !ok {
		t.Fatalf("键入 @ 后没有弹出候选：%q", tt.Texts())
	}
	if _, ok := tt.Find("@所有人"); !ok {
		t.Fatalf("候选面板缺少 @所有人：%q", tt.Texts())
	}

	tt.Type("ali")
	tt.Frame()
	if _, ok := tt.Find("@alice"); !ok {
		t.Fatalf("按前缀 ali 没有筛出 alice：%q", tt.Texts())
	}
	if _, ok := tt.Find("@小号"); ok {
		t.Errorf("按前缀 ali 不该还显示小号：%q", tt.Texts())
	}

	if err := tt.Click("@alice"); err != nil {
		t.Fatalf("点不到候选行：%v", err)
	}
	if a.detail.draft != "@alice " {
		t.Errorf("选中候选后的正文 = %q，期望 @alice （@前缀要被替换）", a.detail.draft)
	}
}

// 邮箱里的 @ 不算提及，不该弹面板。
func TestMentionTypingAtInEmailIgnored(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[{"id":"g2","username":"守护姐姐"}],"total":1}`,
		`{"items":[{"id":"u2","username":"alice"}],"total":1}`, &calls)
	defer srv.Close()
	_, tt := mentionUIApp(t, srv)

	if err := tt.Click("写评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("a@b.com")
	tt.Frame()
	if _, ok := tt.Find("提及候选面板"); ok {
		t.Errorf("邮箱里的 @ 不该弹候选：%q", tt.Texts())
	}
}

// @前缀 后补空格就是结束了：候选面板自动收起，空格和已写的字都留在正文里。
func TestMentionTypingAtClosesOnSpace(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[],"total":0}`,
		`{"items":[{"id":"u2","username":"alice"}],"total":1}`, &calls)
	defer srv.Close()
	_, tt := mentionUIApp(t, srv)

	if err := tt.Click("写评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("@j")
	tt.Frame()
	if _, ok := tt.Find("提及候选面板"); !ok {
		t.Fatalf("键入 @j 后没有弹候选：%q", tt.Texts())
	}
	tt.Type(" ")
	tt.Frame()
	if _, ok := tt.Find("提及候选面板"); ok {
		t.Errorf("@ 后补空格仍开着候选面板：%q", tt.Texts())
	}
}

// 候选面板的角必须紧贴 @ 符号（而不是居中于光标或钉在窗口角落）。
// Tester.TextCaret 是输入控件上报的真实位置，键入 @ 后它就落在 @ 旁边。
func TestMentionPanelCornerAtAt(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[],"total":0}`,
		`{"items":[{"id":"u2","username":"alice"}],"total":1}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)

	typeAt(t, tt)
	waitMentionLoaded(t, a)
	tt.Frame()
	panel, ok := tt.Find("提及候选面板")
	if !ok {
		t.Fatalf("找不到候选面板容器：%q", tt.Texts())
	}
	caret, has := tt.TextCaret()
	if !has {
		t.Fatal("输入框没有上报光标位置")
	}
	// 面板左边贴着 @；评论区在窗口底部，面板向上展开，下沿贴着 @。
	dx := int(panel.X - caret.X)
	if dx < 0 {
		dx = -dx
	}
	bottom := panel.Y + panel.H
	dy := int(bottom - caret.Y)
	if dy < 0 {
		dy = -dy
	}
	if dx > 40 || dy > 60 {
		t.Errorf("面板的角没贴住 @：panel=%+v caret=%+v dx=%d dy=%d", panel, caret, dx, dy)
	}
	if _, ok := tt.Find("@提及"); ok {
		t.Error("工具条上不该还有 @ 入口")
	}
}

// 写完的提及在编辑区里是一整块：只在 @名字 后补了空格时才成立，
// 正在输入的 @前缀 与邮箱里的 @ 都不算。
func TestMentionBlocks(t *testing.T) {
	blocks := mentionBlocks("@a @b 你好 c@d.com @j")
	if len(blocks) != 2 {
		t.Fatalf("提及块 = %+v，期望 2 块", blocks)
	}
	if blocks[0] != (mentionBlock{0, 3}) || blocks[1] != (mentionBlock{3, 6}) {
		t.Errorf("提及块范围错误：%+v", blocks)
	}
}

// 选中候选后，光标停在 @名字 的空格之后；按一次退格应把整块清掉，
// 而不是一个字符一个字符往回删。
func TestMentionBlockDeletedWithOneBackspace(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[],"total":0}`,
		`{"items":[{"id":"u2","username":"alice"}],"total":1}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)

	if err := tt.Click("写评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("@")
	tt.Frame()
	waitMentionLoaded(t, a)
	tt.Frame()
	if err := tt.Click("@alice"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.detail.draft != "@alice " {
		t.Fatalf("选中候选后正文 = %q，期望 @alice ", a.detail.draft)
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if a.detail.draft != "" {
		t.Errorf("退格一次没有整块清除，正文 = %q", a.detail.draft)
	}
}

// 键盘操作：上下键切换候选，回车采纳并替换 @前缀。
func TestMentionKeyboardPick(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[],"total":0}`,
		`{"items":[{"id":"u1","username":"jimmy"},{"id":"u2","username":"alice"},`+
			`{"id":"u3","username":"bob"}],"total":3}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)

	if err := tt.Click("写评论"); err != nil {
		t.Fatal(err)
	}
	tt.Type("@")
	tt.Frame()
	waitMentionLoaded(t, a)
	tt.Frame()

	// 候选按名单顺序排除自己：alice(0)、bob(1)。Down 到 bob 后回车采纳。
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.detail.draft != "@bob " {
		t.Errorf("键盘选中的正文 = %q，期望 @bob ", a.detail.draft)
	}
}

// 名单为空或只剩自己时隐藏入口，不给出点了没反应的按钮。
func TestMentionPanelEmptyLists(t *testing.T) {
	var calls atomic.Int32
	srv := mentionTestServer(t,
		`{"items":[{"id":"g1","username":"jimmy"}],"total":1}`,
		`{"items":[{"id":"u1","username":"jimmy"}],"total":1}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)
	typeAt(t, tt)
	waitMentionLoaded(t, a)
	tt.Frame()
	if _, ok := tt.Find("@所有人"); ok {
		t.Errorf("名单只剩自己时仍画了 @所有人：%q", tt.Texts())
	}
	if _, ok := tt.Find("@随机守护者"); ok {
		t.Errorf("名单只剩自己时仍画了 @随机守护者：%q", tt.Texts())
	}
}

// 超过 50 人：服务端只会给前 50 个发通知，不再提供「@所有人」。
func TestMentionPanelOverLimitHidesEveryone(t *testing.T) {
	items := make([]string, 0, 55)
	for i := 0; i < 55; i++ {
		items = append(items, fmt.Sprintf(`{"id":"u%d","username":"user%02d"}`, i, i))
	}
	var calls atomic.Int32
	srv := mentionTestServer(t, `{"items":[],"total":0}`,
		`{"items":[`+strings.Join(items, ",")+`],"total":55}`, &calls)
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)
	typeAt(t, tt)
	waitMentionLoaded(t, a)
	tt.Frame()
	if _, ok := tt.Find("@所有人"); ok {
		t.Errorf("超过 50 人不应再提供 @所有人：%q", tt.Texts())
	}
}

// 无权访问该帖：403 permission_denied 只重试一次都不该有，并在气泡里说清。
func TestMentionParticipantsDenied(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/meta/guardians" {
			calls.Add(1)
			fmt.Fprint(w, envelopeJSON(`{"items":[],"total":0}`))
			return
		}
		if r.URL.Path == "/api/v1/topics/JBYGf2A/participants" {
			calls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"c":403,"code":"permission_denied","m":"无权访问","d":null}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	a, tt := mentionUIApp(t, srv)
	typeAt(t, tt)
	waitFor(t, func() bool { return a.detail.mentions.participantsDenied }, "403 没有落成无权状态")
	tt.Frame()
	if !tt.HasText("你没有权限查看这篇帖子的参与人") {
		t.Errorf("没有说明无权原因：%q", tt.Texts())
	}
	if _, ok := tt.Find("@所有人"); ok {
		t.Errorf("无权时仍画了 @所有人：%q", tt.Texts())
	}
	// 再画几帧也不许重试：该端点单独限流，403 是不重试的业务错误。
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Errorf("403 后仍发了请求，次数 = %d，期望 2（两个端点各一次）", n)
	}
}
