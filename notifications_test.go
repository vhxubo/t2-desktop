package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   已读接口：POST /v1/me/notifications/read
------------------------------------------------------------------ */

type readCall struct {
	method      string
	path        string
	body        string
	contentType string
	authorized  string
}

// readRecorder 记录每次已读请求，用来钉死路径、请求体与鉴权头。
func readRecorder(t *testing.T) (*httptest.Server, func() []readCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []readCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, readCall{
			method:      r.Method,
			path:        r.URL.Path,
			body:        string(body),
			contentType: r.Header.Get("Content-Type"),
			authorized:  r.Header.Get("Authorization"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, envelopeJSON(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []readCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]readCall(nil), calls...)
	}
}

func readClient(srv *httptest.Server) *Client {
	return &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
}

// 单条 / 多条已读必须打到开放 API 的兼容端点，请求体字段名是 id（不是 ids）。
// 服务端会静默丢弃未知字段：写成 ids 就等于什么都没传，请求会退化成「全部已读」，
// 而且不报错——这个测试是唯一能拦住它的地方。
func TestMarkNotificationsReadRequest(t *testing.T) {
	srv, calls := readRecorder(t)
	c := readClient(srv)

	if err := c.markNotificationsRead(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("请求次数 = %d，期望 1", len(got))
	}
	call := got[0]
	if call.method != http.MethodPost || call.path != "/api/v1/me/notifications/read" {
		t.Errorf("请求 = %s %s，期望 POST /api/v1/me/notifications/read", call.method, call.path)
	}
	if call.body != `{"id":["a","b"]}` {
		t.Errorf("请求体 = %s，期望 {\"id\":[\"a\",\"b\"]}", call.body)
	}
	if call.contentType != "application/json" {
		t.Errorf("Content-Type = %q", call.contentType)
	}
	if call.authorized != "Bearer tok" {
		t.Errorf("Authorization = %q", call.authorized)
	}
	// 只允许一个键，且必须是 id：多写一个 ids 也会被静默丢弃。
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.body), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["id"]; !ok || len(body) != 1 {
		t.Errorf("请求体字段必须是唯一的 id：%v", body)
	}
}

// 全部已读沿用「不带 body」的旧用法；{} 虽然也被接受，但没有必要冒多传字段的险。
func TestMarkAllNotificationsReadSendsNoBody(t *testing.T) {
	srv, calls := readRecorder(t)
	c := readClient(srv)

	if err := c.markAllNotificationsRead(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("请求次数 = %d，期望 1", len(got))
	}
	if got[0].path != "/api/v1/me/notifications/read" || got[0].body != "" {
		t.Errorf("全部已读请求 = %s body=%q，期望同路径且无 body", got[0].path, got[0].body)
	}
	if got[0].contentType != "" {
		t.Errorf("无 body 的请求不该声明 Content-Type：%q", got[0].contentType)
	}
}

// 空列表绝不能发请求：id: [] 会被服务端判成 400，且它不等于「全部已读」。
func TestMarkNotificationsReadSkipsEmpty(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"", "  "}} {
		srv, calls := readRecorder(t)
		if err := readClient(srv).markNotificationsRead(context.Background(), ids); err != nil {
			t.Fatalf("空列表不该出错：%v", err)
		}
		if got := calls(); len(got) != 0 {
			t.Errorf("id 列表 %v 发出了 %d 个请求", ids, len(got))
		}
	}
}

// 100 条是服务端上限：超过就分片，单条请求永远不超限。
func TestMarkNotificationsReadBatchesOverLimit(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "id-" + strconv.Itoa(i)
		}
		return out
	}
	cases := []struct {
		name  string
		count int
		sizes []int
	}{
		{"100 条", 100, []int{100}},
		{"101 条", 101, []int{100, 1}},
		{"250 条", 250, []int{100, 100, 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := readRecorder(t)
			if err := readClient(srv).markNotificationsRead(context.Background(), many(tc.count)); err != nil {
				t.Fatal(err)
			}
			got := calls()
			if len(got) != len(tc.sizes) {
				t.Fatalf("分片数 = %d，期望 %d", len(got), len(tc.sizes))
			}
			seen := 0
			for i, call := range got {
				var body struct {
					ID []string `json:"id"`
				}
				if err := json.Unmarshal([]byte(call.body), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.ID) != tc.sizes[i] {
					t.Errorf("第 %d 片 = %d 条，期望 %d", i, len(body.ID), tc.sizes[i])
				}
				seen += len(body.ID)
			}
			if seen != tc.count {
				t.Errorf("共提交 %d 条，期望 %d", seen, tc.count)
			}
		})
	}
}

func TestNotificationReadBatches(t *testing.T) {
	batches := notificationReadBatches([]string{" b ", "a", "b", "c", "a", ""})
	if len(batches) != 1 || len(batches[0]) != 3 {
		t.Fatalf("去重后应只剩 3 条：%v", batches)
	}
	want := []string{"b", "a", "c"}
	for i := range want {
		if batches[0][i] != want[i] {
			t.Fatalf("去重后 = %v，期望 %v（保持原顺序）", batches[0], want)
		}
	}
	// 空输入返回 nil：调用方据此完全不发请求。
	if got := notificationReadBatches([]string{"", " "}); got != nil {
		t.Errorf("空输入应返回 nil：%v", got)
	}
}

/* ------------------------------------------------------------------
   消息 → 展示字段（对齐参考扩展的 describeNotification）
------------------------------------------------------------------ */

func mustObject(t *testing.T, raw string) object {
	t.Helper()
	var o object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("测试数据不是对象：%v", err)
	}
	return o
}

func TestNotificationViewReplyTarget(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n1","type":"reply","is_read":false,"created_at":"2026-11-11T01:00:00Z",
		"from_user":{"username":"alice"},
		"payload":{"postShortId":"wEOIDnn","postNodeSlug":"social-observation",
			"commentId":"c1","postTitle":"社会观察","commentText":"说得对:feel_wronged:",
			"commentLevel":1}
	}`))
	if n == nil {
		t.Fatal("消息解码失败")
	}
	v := notificationViewOf(n)
	if v.TypeLabel != "回复" || v.Tone != "info" {
		t.Errorf("类型 = %q / %q", v.TypeLabel, v.Tone)
	}
	if v.ActorName != "alice" || v.ActorIsAlias {
		t.Errorf("操作者 = %q alias=%v", v.ActorName, v.ActorIsAlias)
	}
	if v.Action != "回复了你的评论" {
		t.Errorf("action = %q", v.Action)
	}
	if v.TargetTitle != "社会观察" {
		t.Errorf("目标标题 = %q", v.TargetTitle)
	}
	if v.TargetURL != "https://2libra.com/post/social-observation/wEOIDnn?commentId=c1" {
		t.Errorf("目标地址 = %q", v.TargetURL)
	}
	if v.Excerpt != "说得对" {
		t.Errorf("摘要 = %q（表情短码应被清掉）", v.Excerpt)
	}
}

func TestNotificationViewReactionAggregates(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n2","type":"reaction","payload":{"reactionType":"comment","related_id":"c9",
			"relations":[{"emoji":"👍","username":"a","avatar_url":"https://r2.2libra.com/a.png"},
				{"emoji":"👍","username":"b"}],
			"reactions":{"👍":3}}
	}`))
	v := notificationViewOf(n)
	if v.ActorName != "" {
		t.Errorf("表态是聚合事件，不该把 from_user 当操作者：%q", v.ActorName)
	}
	if v.Action != "你的评论收到了新表态" {
		t.Errorf("action = %q", v.Action)
	}
	if v.Reactions != "👍3（@a、@b）" {
		t.Errorf("表态汇总 = %q", v.Reactions)
	}
	// 界面用结构化视图给点名的用户画头像：relations 里带头像地址时一并保留。
	if len(v.ReactionGroups) != 1 || v.ReactionGroups[0].Emoji != "👍" ||
		v.ReactionGroups[0].Count != 3 || len(v.ReactionGroups[0].Users) != 2 ||
		v.ReactionGroups[0].Users[0].Name != "a" ||
		v.ReactionGroups[0].Users[0].Avatar != "https://r2.2libra.com/a.png" ||
		v.ReactionGroups[0].Users[1].Name != "b" {
		t.Errorf("表态聚合视图 = %+v", v.ReactionGroups)
	}
	// related_id 是评论 id，但没有帖子 short_id 可拼：回到消息中心而不是
	// 拼出一个打不开的链接。
	if v.TargetURL != siteBaseURL+"/notifications" {
		t.Errorf("缺少帖子定位时应回到消息中心：%q", v.TargetURL)
	}
}

func TestNotificationViewFollowTarget(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n3","type":"follow","payload":{"followerUsername":"bob"}
	}`))
	v := notificationViewOf(n)
	if v.Action != "关注了你" || v.Tone != "primary" {
		t.Errorf("follow = %q / %q", v.Action, v.Tone)
	}
	if v.ActorName != "bob" {
		t.Errorf("操作者 = %q", v.ActorName)
	}
	if v.TargetURL != "https://2libra.com/user/bob/about" {
		t.Errorf("关注目标 = %q", v.TargetURL)
	}
}

func TestNotificationViewSystemAndReward(t *testing.T) {
	sys := notificationViewOf(decodeNotificationObject(mustObject(t, `{
		"id":"s1","type":"system","payload":{"error":"RSS 解析失败","feedUrl":"https://x/feed",
			"consecutiveFailures":3}
	}`)))
	if sys.Detail != "RSS 解析失败" || sys.DetailTone != "danger" {
		t.Errorf("系统消息 = %q / %q", sys.Detail, sys.DetailTone)
	}
	if len(sys.Meta) != 2 || !strings.Contains(sys.Meta[0], "RSS") {
		t.Errorf("系统消息 meta = %v", sys.Meta)
	}
	if sys.TargetURL != siteBaseURL+"/notifications" {
		t.Errorf("没有目标时回到消息中心：%q", sys.TargetURL)
	}

	reward := notificationViewOf(decodeNotificationObject(mustObject(t, `{
		"id":"r1","type":"get_reward","payload":{"rewardType":"post","amount":5}
	}`)))
	if reward.Action != "打赏了你的帖子" || reward.Detail != "+5 金币" {
		t.Errorf("打赏 = %q / %q", reward.Action, reward.Detail)
	}
	if reward.Excerpt != "" {
		t.Errorf("帖子打赏不该展示评论正文：%q", reward.Excerpt)
	}
}

func TestNotificationViewUnknownType(t *testing.T) {
	v := notificationViewOf(decodeNotificationObject(mustObject(t, `{
		"id":"u1","type":"mystery_new_type","payload":{}
	}`)))
	if v.Kind != "unknown" || v.Tone != "muted" || v.Action != "发来一条新消息" {
		t.Errorf("未知类型应退回「一条新消息」：%+v", v)
	}
	if v.TypeLabel != "mystery_new_type" {
		t.Errorf("未知类型标签 = %q", v.TypeLabel)
	}
}

func TestNotificationNativeNotice(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n1","type":"reply","payload":{"postShortId":"wEOIDnn","postNodeSlug":"social",
			"postTitle":"社会观察","commentText":"说得对"}
	}`))
	n.FromUser = mustObject(t, `{"username":"alice"}`)
	notice := notificationNative(n)
	if notice.Title != "@alice 回复了你" {
		t.Errorf("通知标题 = %q", notice.Title)
	}
	if !strings.Contains(notice.Body, "《社会观察》") || !strings.Contains(notice.Body, "说得对") {
		t.Errorf("通知正文 = %q", notice.Body)
	}
	if notice.ID != "t2-notification:n1" {
		t.Errorf("通知 id = %q", notice.ID)
	}
	if notice.Target != "https://2libra.com/post/social/wEOIDnn" {
		t.Errorf("点击目标 = %q", notice.Target)
	}
}

/* ------------------------------------------------------------------
   应用层：单条 / 已读本页 / 勾选批量的落账
------------------------------------------------------------------ */

func notificationApp(t *testing.T, srv *httptest.Server) (*app, *tabState) {
	t.Helper()
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}
	// 让 refreshUnread 直接返回：测试只关心已读落账，不额外拉未读数。
	a.unreadLoading = true
	a.unread = 2
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.total = 3
	st.items = []Item{
		{Key: "n1", Notif: &notification{ID: "n1", Type: "reply"}},
		{Key: "n2", Notif: &notification{ID: "n2", Type: "mention", IsRead: true}},
		{Key: "n3", Notif: &notification{ID: "n3", Type: "system"}},
	}
	return a, st
}

func waitNotificationsRead(t *testing.T, a *app, action func()) []string {
	t.Helper()
	done := make(chan []string, 1)
	a.onNotificationsRead = func(ids []string) { done <- ids }
	action()
	select {
	case ids := <-done:
		a.onNotificationsRead = nil
		return ids
	case <-time.After(5 * time.Second):
		t.Fatal("已读请求超时")
		return nil
	}
}

func TestMarkOneNotificationRead(t *testing.T) {
	srv, calls := readRecorder(t)
	a, st := notificationApp(t, srv)

	waitNotificationsRead(t, a, func() { a.markNotificationRead("n1") })

	if !st.items[0].Notif.IsRead {
		t.Error("单条已读没有落到本地行")
	}
	if st.items[2].Notif.IsRead {
		t.Error("别的消息被误标为已读")
	}
	got := calls()
	if len(got) != 1 || got[0].body != `{"id":["n1"]}` {
		t.Fatalf("单条已读请求 = %+v", got)
	}
}

// 「已读本页」只提交当前已加载的未读消息，已读的那条不重复提交。
func TestMarkLoadedNotificationsRead(t *testing.T) {
	srv, calls := readRecorder(t)
	a, st := notificationApp(t, srv)

	ids := waitNotificationsRead(t, a, func() { a.markLoadedNotificationsRead() })
	if len(ids) != 2 {
		t.Fatalf("已读本页应提交 2 条：%v", ids)
	}
	if got := calls(); len(got) != 1 || got[0].body != `{"id":["n1","n3"]}` {
		t.Errorf("已读本页请求 = %+v", got)
	}
	for _, it := range st.items {
		if !it.Notif.IsRead {
			t.Errorf("消息 %s 仍是未读", it.Notif.ID)
		}
	}
	if a.unread != 0 {
		t.Errorf("本地未读数 = %d，期望 0", a.unread)
	}
}

// 「未读」筛选下，读掉的消息要立刻从列表消失，总数同步减少。
func TestMarkReadRemovesFromUnreadFilter(t *testing.T) {
	srv, _ := readRecorder(t)
	a, st := notificationApp(t, srv)
	st.filter = notificationFilterUnread
	st.total = 2

	waitNotificationsRead(t, a, func() { a.markNotificationRead("n1") })

	if len(st.items) != 2 {
		t.Fatalf("未读筛选下应只剩 2 条：%d", len(st.items))
	}
	for _, it := range st.items {
		if it.Notif != nil && it.Notif.ID == "n1" {
			t.Error("已读的消息没有从「未读」里移除")
		}
	}
	if st.total != 1 {
		t.Errorf("总数 = %d，期望 1", st.total)
	}
}

// 勾选批量：只提交勾中的、仍加载且未读的消息，顺序稳定；读掉后勾选清空。
func TestMarkSelectedNotificationsRead(t *testing.T) {
	srv, calls := readRecorder(t)
	a, st := notificationApp(t, srv)
	st.selected = map[string]bool{"n3": true, "n1": true, "n2": true} // n2 已读，应被忽略

	if n := notificationSelectionCount(st); n != 2 {
		t.Fatalf("可提交的勾选数 = %d，期望 2", n)
	}
	ids := waitNotificationsRead(t, a, func() { a.markSelectedNotificationsRead() })
	if len(ids) != 2 || ids[0] != "n1" || ids[1] != "n3" {
		t.Fatalf("勾选批量提交 = %v，期望 [n1 n3]", ids)
	}
	if got := calls(); len(got) != 1 || got[0].body != `{"id":["n1","n3"]}` {
		t.Errorf("勾选批量请求 = %+v", got)
	}
	if n := notificationSelectionCount(st); n != 0 {
		t.Errorf("读掉后勾选应清空：%d", n)
	}
}

// 连续点击同一条时只发一次请求。
func TestMarkNotificationReadCoalesces(t *testing.T) {
	srv, calls := readRecorder(t)
	a, _ := notificationApp(t, srv)

	done := make(chan struct{}, 1)
	a.onNotificationsRead = func([]string) { done <- struct{}{} }
	a.markNotificationRead("n1")
	a.markNotificationRead("n1")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("已读请求超时")
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("重复点击发起了 %d 个请求", len(got))
	}
}

/* ------------------------------------------------------------------
   界面：标题栏铃铛、筛选、计数按钮与勾选框
------------------------------------------------------------------ */

// 通知是标题栏铃铛打开的独立弹窗：侧边栏不再有「通知」入口，未读数值
// 直接画在铃铛右边，列表与已读操作都在弹窗里。
func TestNotificationBellOpensDialog(t *testing.T) {
	a := notificationDialogApp(t)

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	bell, ok := tt.Find("通知（2 条未读）")
	if !ok {
		t.Fatalf("标题栏没有带未读数的铃铛：%q", tt.Texts())
	}
	if bell.Y < 0 || bell.Y+bell.H > titleBarHeight || bell.H == 0 {
		t.Errorf("铃铛没有落在标题栏里：%v", bell)
	}
	if !tt.HasText("2") {
		t.Errorf("铃铛旁没有未读数值：%q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if text == "通知" {
			t.Errorf("侧边栏仍把「通知」当成页面入口：%q", tt.Texts())
			break
		}
	}
	if a.notifOpen {
		t.Fatal("弹窗不该在点击铃铛前打开")
	}
	if err := tt.Click("通知（2 条未读）"); err != nil {
		t.Fatalf("点击铃铛失败：%v", err)
	}
	tt.Frame()
	if !a.notifOpen {
		t.Fatal("点击铃铛没有打开通知弹窗")
	}

	for _, want := range []string{
		"通知", "2 条未读",
		"全部已读", "已读本页 1", "已读选中 1",
		"@dannyng 提到了你", "这条是提及",
		"全部", "未读", "回复", "提及", "表态", "打赏",
		"原生通知不可用",
	} {
		if !tt.HasText(want) {
			t.Errorf("通知弹窗缺少文案 %q：%q", want, tt.Texts())
		}
	}

	// 弹窗有自己的关闭按钮；点窗外或 Esc 同样收起（Modal 的默认行为）。
	if err := tt.Click("关闭通知"); err != nil {
		t.Fatalf("点击关闭通知失败：%v", err)
	}
	tt.Frame()
	if a.notifOpen {
		t.Error("「关闭通知」没有收起弹窗")
	}
}

// 弹窗里的列表与操作在没有真实窗口时也能渲染出全部文案（数据层测试与
// 界面测试共用同一份状态）。
func notificationDialogApp(t *testing.T) *app {
	t.Helper()
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.unread = 2
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.total = 2
	st.items = []Item{
		{Key: "n1", Notif: &notification{
			ID: "n1", Type: "mention",
			FromUser: mustObject(t, `{"username":"dannyng"}`),
			Payload:  mustObject(t, `{"commentText":"这条是提及"}`),
		}},
		{Key: "n2", Notif: &notification{ID: "n2", Type: "reply", IsRead: true}},
	}
	st.selected = map[string]bool{"n1": true}
	return a
}

// 未登录时不发请求：点铃铛先弹全局授权窗，弹窗本身不打开（通知需要 read:me）。
func TestNotificationBellNeedsLogin(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("通知"); err != nil {
		t.Fatalf("点击铃铛失败：%v", err)
	}
	tt.Frame()
	if a.notifOpen {
		t.Error("未登录时不该打开通知弹窗")
	}
	if !a.authGate.open {
		t.Error("未登录点铃铛应弹出授权弹窗")
	}
	if !tt.HasText("登录 2Libra 后才能查看「通知」。") {
		t.Errorf("授权弹窗没有说明原因：%q", tt.Texts())
	}
}

// Esc 与「关闭通知」同义：弹窗收起，回到正在浏览的页面。
func TestNotificationDialogDismissesOnEscape(t *testing.T) {
	a := notificationDialogApp(t)
	a.notifOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.notifOpen {
		t.Error("Esc 没有收起通知弹窗")
	}
}

// 打开一条消息后弹窗自动收起；指向站点页面的消息交给系统浏览器，不留在
// 弹窗上面。
func TestNotificationTargetOpensAndClosesDialog(t *testing.T) {
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.notifOpen = true
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.items = []Item{{Key: "f1", Notif: &notification{
		ID: "f1", Type: "follow", IsRead: true,
		Payload: mustObject(t, `{"followerUsername":"bob"}`),
	}}}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("@bob 关注了你"); err != nil {
		t.Fatalf("点击消息行失败：%v", err)
	}
	tt.Frame()
	if a.notifOpen {
		t.Error("打开目标后应收起通知弹窗")
	}
	urls := tt.OpenedURLs()
	if len(urls) == 0 || urls[0] != "https://2libra.com/user/bob/about" {
		t.Errorf("消息目标没有交给系统浏览器：%v", urls)
	}
}

// 窗口再小，弹窗也要完整落在窗口里，列表在面板内滚动。
func TestNotificationDialogFitsSmallWindow(t *testing.T) {
	a := notificationDialogApp(t)
	a.notifOpen = true
	tt := ui.NewTester(a.view, 760, 520)
	tt.Frame()
	tt.Frame()
	closeR, ok := tt.Find("关闭通知")
	if !ok {
		t.Fatalf("小窗口里找不到关闭按钮：%q", tt.Texts())
	}
	if closeR.X < 0 || closeR.X+closeR.W > 760 {
		t.Errorf("弹窗横向超出窗口：%v", closeR)
	}
	row, ok := tt.Find("这条是提及")
	if !ok {
		t.Fatalf("小窗口里没有消息行：%q", tt.Texts())
	}
	if row.Y < 0 || row.Y+row.H > 520 {
		t.Errorf("消息行超出窗口：%v", row)
	}
}

// 原生通知的点击回调只带 id：进程内能查到目标时在应用内打开，查不到
// （例如点击的是上次运行留下的通知）就回到站点消息中心。
func TestNotificationNoticeTarget(t *testing.T) {
	a := testApp(t)
	a.rememberNoticeTarget("t2-notification:n1", "https://2libra.com/post/ai-tools/ABC12345")
	if got := a.notificationNoticeTarget("t2-notification:n1"); got != "https://2libra.com/post/ai-tools/ABC12345" {
		t.Errorf("点击目标 = %q", got)
	}
	if got := a.notificationNoticeTarget("t2-notification:gone"); got != siteBaseURL+"/notifications" {
		t.Errorf("查不到目标时应回到消息中心：%q", got)
	}
	// 映射有上限：超过容量后整体清空，不会无限增长。
	for i := 0; i < noticeTargetKeep+1; i++ {
		a.rememberNoticeTarget("t2-notification:bulk-"+strconv.Itoa(i), siteBaseURL+"/notifications")
	}
	if len(a.noticeTargets) > noticeTargetKeep {
		t.Errorf("映射未设上限：%d", len(a.noticeTargets))
	}
	if len(a.noticeTargets) == 0 {
		t.Error("清理后应保留最近写入的目标")
	}
}
