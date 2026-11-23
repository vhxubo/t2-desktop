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

// 马甲头像在 payload 里，专属拼写与通用拼写都要认；只认一种会让头像变空。
func TestNotificationAliasAvatarFieldNames(t *testing.T) {
	for _, tc := range []struct {
		name, field, want string
	}{
		{"专属字段", "alias_avatar_url", "https://x/alias.png"},
		{"通用字段", "avatar_url", "https://x/generic.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{
				"id":"n1","type":"reply","is_read":false,"created_at":"2026-11-11T01:00:00Z",
				"from_user":{"username":"alice"},
				"payload":{"alias_name":"小号","postShortId":"abc","%s":%q}
			}`, tc.field, tc.want)
			n := decodeNotificationObject(mustObject(t, raw))
			if n == nil {
				t.Fatal("消息解码失败")
			}
			v := notificationViewOf(n)
			if !v.ActorIsAlias {
				t.Fatal("应当识别为马甲操作者")
			}
			if v.ActorAvatar != tc.want {
				t.Errorf("马甲头像 = %q，期望 %q", v.ActorAvatar, tc.want)
			}
		})
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
	if v.Action != "表态了你的评论" {
		t.Errorf("action = %q", v.Action)
	}
	// 每种表情一条计数，人名单独去重一份：界面据此把表情合成一行、名字只写
	// 一次，不再按表情各套一对括号。
	if len(v.Reaction.Groups) != 1 || v.Reaction.Groups[0].Emoji != "👍" ||
		v.Reaction.Groups[0].Count != 3 {
		t.Errorf("表态计数 = %+v", v.Reaction.Groups)
	}
	if len(v.Reaction.People) != 2 ||
		v.Reaction.People[0].Name != "a" ||
		v.Reaction.People[0].Avatar != "https://r2.2libra.com/a.png" ||
		v.Reaction.People[1].Name != "b" {
		t.Errorf("表态人 = %+v", v.Reaction.People)
	}
	// relations 只点了两个人，计数说有三个：剩下的人用「等 3 人」带过。
	if got := notificationReactionText(v.Reaction); got != "👍3 @a、@b 等 3 人" {
		t.Errorf("表态汇总 = %q", got)
	}
	// related_id 是评论 id，但没有帖子 short_id 可拼：回到消息中心而不是
	// 拼出一个打不开的链接。
	if v.TargetURL != siteBaseURL+"/notifications" {
		t.Errorf("缺少帖子定位时应回到消息中心：%q", v.TargetURL)
	}
}

// 一条通知里只有一个人表态时，表情合并成一句、用户名只写一次：这正是
// 「👍1（@张三）· 😂1（@张三）」要消掉的样子。
func TestNotificationReactionMergesOnePerson(t *testing.T) {
	v := notificationViewOf(decodeNotificationObject(mustObject(t, `{
		"id":"n4","type":"reaction",
		"payload":{"reactionType":"post","postTitle":"一条帖子",
			"relations":[{"emoji":"👍","username":"jimmy"},
				{"emoji":"😂","username":"jimmy"}],
			"reactions":{"👍":1,"😂":1}}
	}`)))
	if got := notificationReactionText(v.Reaction); got != "👍 😂 @jimmy" {
		t.Errorf("一个人的两种表情应合并成一句：%q", got)
	}
	if len(v.Reaction.People) != 1 || v.Reaction.People[0].Name != "jimmy" {
		t.Errorf("表态人应去重：%+v", v.Reaction.People)
	}
	if others := notificationReactionOthers(v.Reaction, 1); others != 0 {
		t.Errorf("同一个人的两种表情不算「还有别人」：%d", others)
	}
	if v.Action != "表态了你的帖子" {
		t.Errorf("action = %q", v.Action)
	}
}

// 表态的人多于上限时：名字仍然只写一遍，多出来的人用「等 N 人」收尾。
func TestNotificationReactionOthersCapped(t *testing.T) {
	v := notificationViewOf(decodeNotificationObject(mustObject(t, `{
		"id":"n5","type":"reaction","payload":{"reactionType":"post",
			"relations":[{"emoji":"👍","username":"a"},{"emoji":"👍","username":"b"},
				{"emoji":"👍","username":"c"},{"emoji":"👍","username":"d"}],
			"reactions":{"👍":4}}
	}`)))
	names := v.Reaction.People
	if len(names) != 4 {
		t.Fatalf("四个人都该被点到：%+v", names)
	}
	if got := notificationReactionText(v.Reaction); got != "👍4 @a、@b、@c 等 4 人" {
		t.Errorf("表态汇总 = %q", got)
	}
	if others := notificationReactionOthers(v.Reaction, notificationReactionNamesMax); others != 4 {
		t.Errorf("还有别人时应按真实人数概括：%d", others)
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
	notice := notificationNative(n, "", "")
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
	if notice.Subtitle != "" || notice.AccountID != "" {
		t.Errorf("单账号不该写账号：%+v", notice)
	}
}

// 多账号时系统通知要写明是哪个账号收到的：账号写进副标题，通知 id 也带上
// 账号段，点击才落回正确的窗口。
func TestNotificationNativeNoticeNamesAccount(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n1","type":"reply","payload":{"postShortId":"wEOIDnn","postNodeSlug":"social",
			"postTitle":"社会观察"}
	}`))
	n.FromUser = mustObject(t, `{"username":"alice"}`)
	notice := notificationNative(n, "jimmy-1a2b3c", "jimmy")
	if notice.Subtitle != "账号 @jimmy" {
		t.Errorf("副标题 = %q，期望写明账号", notice.Subtitle)
	}
	if want := "t2-notification:jimmy-1a2b3c:n1"; notice.ID != want {
		t.Errorf("通知 id = %q，期望 %q", notice.ID, want)
	}
	if accountID, messageID := splitNotificationNoticeID(notice.ID); accountID != "jimmy-1a2b3c" || messageID != "n1" {
		t.Errorf("通知 id 拆回 = %q / %q", accountID, messageID)
	}
	// 升级前弹出的通知只有消息 id，拆不出账号，仍按旧格式认。
	if accountID, messageID := splitNotificationNoticeID("t2-notification:old"); accountID != "" || messageID != "old" {
		t.Errorf("旧格式 id 拆回 = %q / %q", accountID, messageID)
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

	waitNotificationsRead(t, a, func() { a.markNotificationRead(toastMain, "n1") })

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

	ids := waitNotificationsRead(t, a, func() { a.markLoadedNotificationsRead(toastMain) })
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

	waitNotificationsRead(t, a, func() { a.markNotificationRead(toastMain, "n1") })

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
	for _, id := range []string{"n3", "n1", "n2"} { // n2 已读，应被忽略
		setNotificationSelected(st, id, true)
	}

	if n := notificationSelectionCount(st); n != 2 {
		t.Fatalf("可提交的勾选数 = %d，期望 2", n)
	}
	ids := waitNotificationsRead(t, a, func() { a.markSelectedNotificationsRead(toastMain) })
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
	a.markNotificationRead(toastMain, "n1")
	a.markNotificationRead(toastMain, "n1")
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
   界面：标题栏铃铛与通知窗口
----------------------------------------------------------------- */

// 标题栏的铃铛带着未读数值，侧边栏不再有「通知」页面入口。
func TestNotificationBellShowsUnreadCount(t *testing.T) {
	a := notificationWindowApp(t)

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
		t.Errorf("铃铛没有未读数值：%q", tt.Texts())
	}
	// 数值是叠在铃铛右下角的红色小圆点，而不是排在铃铛右边的另一个胶囊。
	badge, ok := tt.Find("2")
	if !ok {
		t.Fatalf("找不到未读数值：%q", tt.Texts())
	}
	if cx, cy := bell.X+bell.W/2, bell.Y+bell.H/2; badge.X+badge.W/2 < cx || badge.Y+badge.H/2 < cy {
		t.Errorf("未读角标 %v 不在铃铛 %v 的右下角", badge, bell)
	}
	if badge.X < bell.X || badge.Y < bell.Y ||
		badge.X+badge.W > bell.X+bell.W || badge.Y+badge.H > bell.Y+bell.H {
		t.Errorf("未读角标 %v 超出了铃铛 %v", badge, bell)
	}
	// 角标底色必须是红色：以数值为中心数一数 14×14 里的红色像素，太少
	// 说明底色没画上或沿用了主题的强调色。
	im := tt.Image()
	cx, cy := int(badge.X+badge.W/2), int(badge.Y+badge.H/2)
	red := 0
	for y := cy - 7; y <= cy+7; y++ {
		for x := cx - 7; x <= cx+7; x++ {
			if x < 0 || y < 0 || x >= im.Bounds().Dx() || y >= im.Bounds().Dy() {
				continue
			}
			c := im.RGBAAt(x, y)
			if c.R >= 0xb0 && c.G <= 0x60 && c.B <= 0x60 {
				red++
			}
		}
	}
	if red < 40 {
		t.Errorf("未读角标里的红色像素只有 %d 个，底色可能不是红色", red)
	}
	for _, text := range tt.Texts() {
		if text == "通知" {
			t.Errorf("侧边栏仍把「通知」当成页面入口：%q", tt.Texts())
			break
		}
	}
	// headless 没有主循环：点铃铛不能去碰 mygo 的窗口，也不该改主窗口的页面。
	a.autoLoad = false
	if err := tt.Click("通知（2 条未读）"); err != nil {
		t.Fatalf("点击铃铛失败：%v", err)
	}
	tt.Frame()
	if a.notifWin != nil {
		t.Error("headless 测试里不该创建通知窗口")
	}
	if a.pageName != "list" {
		t.Errorf("点铃铛不该切走主窗口的页面：%q", a.pageName)
	}
}

// 通知窗口的 view 就是消息中心：标题栏、未读数、操作行、分段筛选与列表
// 全在这一个独立窗口里。
func TestNotificationWindowView(t *testing.T) {
	a := notificationWindowApp(t)

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"通知", "2 条未读",
		"全部已读", "已读本页 1", "已读选中 1", "刷新",
		"@dannyng 提到了你", "这条是提及",
		"全部", "未读", "回复", "提及", "表态", "打赏",
		"原生通知不可用",
	} {
		if !tt.HasText(want) {
			t.Errorf("通知窗口缺少文案 %q：%q", want, tt.Texts())
		}
	}
	// 窗口有自己的标题栏按钮：最小化与关闭。
	for _, label := range []string{"最小化", "关闭"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("通知窗口缺少「%s」按钮：%q", label, tt.Texts())
		}
	}
}

// 标题栏的未读数是一键筛选入口：点一下只看未读，再点回到全部，省得去下方
// 分段条里找「未读」。
func TestNotificationUnreadCountTogglesFilter(t *testing.T) {
	srv, _ := readRecorder(t)
	a := notificationWindowApp(t)
	a.client = readClient(srv)
	a.unreadLoading = true
	st := a.tabs[notificationTabIndex()]

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if err := tt.Click("2 条未读"); err != nil {
		t.Fatalf("点不到未读数：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if st.filter != notificationFilterUnread {
		t.Errorf("点未读数应切到「未读」筛选，实际 %d", st.filter)
	}
	if err := tt.Click("2 条未读"); err != nil {
		t.Fatalf("再点未读数失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if st.filter != notificationFilterAll {
		t.Errorf("再点应回到「全部」筛选，实际 %d", st.filter)
	}
}

// Cmd/Ctrl+R 刷新当前列表：和浏览器一致的习惯，不必去够右上角的小刷新按钮。
func TestNotificationWindowRefreshShortcut(t *testing.T) {
	srv, calls := readRecorder(t)
	a := notificationWindowApp(t)
	a.client = readClient(srv)
	a.unreadLoading = true
	st := a.tabs[notificationTabIndex()]
	st.totalPages, st.noMore = 1, true // 别让无限滚动顺手再拉一页

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyR)

	waitFor(t, func() bool {
		for _, c := range calls() {
			if c.method == http.MethodGet && strings.Contains(c.path, notificationsPath) {
				return true
			}
		}
		return false
	}, "Cmd+R 没有刷新通知列表")
}

// 一条消息就是一张和列表同宽的卡片：未读行的勾选框与「标为已读」都在卡片里，
// 已读行不再比未读行窄，也没有为按钮留出的空列。
func TestNotificationRowActionInsideCard(t *testing.T) {
	a := notificationWindowApp(t)
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()

	unread, ok := tt.Find("打开消息：提到了你")
	if !ok {
		t.Fatalf("找不到未读消息卡片：%q", tt.Texts())
	}
	read, ok := tt.Find("打开消息：回复了你")
	if !ok {
		t.Fatalf("找不到已读消息卡片：%q", tt.Texts())
	}
	if unread.X != read.X || unread.W != read.W {
		t.Errorf("已读 / 未读卡片没有对齐：未读 %v，已读 %v", unread, read)
	}
	action, ok := tt.Find("标为已读")
	if !ok {
		t.Fatalf("未读消息上没有「标为已读」：%q", tt.Texts())
	}
	if action.X < unread.X || action.X+action.W > unread.X+unread.W {
		t.Errorf("「标为已读」落在卡片外面：按钮 %v，卡片 %v", action, unread)
	}
	if action.X < unread.X+unread.W/2 {
		t.Errorf("「标为已读」应在卡片右上角：按钮 %v，卡片 %v", action, unread)
	}
	if action.Y+action.H > unread.Y+unread.H {
		t.Errorf("「标为已读」超出卡片高度：按钮 %v，卡片 %v", action, unread)
	}
}

// 消息行走 Lobe 的扁平列表语言：相邻两行之间没有留白缝隙，靠行底的一条
// 细分隔线区分，整行也不再是圆角描边卡片。这里用几何钉住「无间隙」。
func TestNotificationRowsAreFlush(t *testing.T) {
	a := notificationWindowApp(t)
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()

	unread, ok := tt.Find("打开消息：提到了你")
	if !ok {
		t.Fatalf("找不到未读消息行：%q", tt.Texts())
	}
	read, ok := tt.Find("打开消息：回复了你")
	if !ok {
		t.Fatalf("找不到已读消息行：%q", tt.Texts())
	}
	if unread.W != read.W {
		t.Errorf("两行宽度不一致：未读 %v，已读 %v", unread, read)
	}
	gap := read.Y - (unread.Y + unread.H)
	if gap < -1 || gap > 1 {
		t.Errorf("相邻消息行之间有 %vpx 缝隙，扁平列表应紧贴：未读 %v，已读 %v", gap, unread, read)
	}
}

// 未读行里的勾选框也在卡片内部：点它只改勾选，不会连带打开消息。
func TestNotificationCheckboxInsideCard(t *testing.T) {
	a := notificationWindowApp(t)
	st := a.tabs[notificationTabIndex()]
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()

	card, ok := tt.Find("打开消息：提到了你")
	if !ok {
		t.Fatalf("找不到未读消息卡片：%q", tt.Texts())
	}
	box, ok := tt.Find("勾选这条消息")
	if !ok {
		t.Fatalf("未读消息上没有勾选框：%q", tt.Texts())
	}
	if box.X < card.X || box.X+box.W > card.X+card.W {
		t.Errorf("勾选框落在卡片外面：勾选框 %v，卡片 %v", box, card)
	}
	a.pageName = "list"
	if err := tt.Click("勾选这条消息"); err != nil {
		t.Fatalf("点不到勾选框：%v", err)
	}
	if a.pageName != "list" {
		t.Errorf("点勾选框不该打开消息：%q", a.pageName)
	}
	if st.notificationChecked("n1") {
		t.Error("点一下应当取消这条消息的勾选")
	}
}

// 点「标为已读」只标记这一条，不会连带打开消息卡片。
func TestNotificationReadActionDoesNotOpenCard(t *testing.T) {
	srv, calls := readRecorder(t)
	a := notificationWindowApp(t)
	a.autoLoad = true
	a.client = readClient(srv)
	// 已读落账之后 applyNotificationRead 会再校准一次未读数：那条 GET 是角标
	// 的正常刷新，不属于这次点击的断言，但也别让它和已读请求一起进 calls()。
	// 与 notificationApp 保持一致，把后台校准挡在测试请求记录之外。
	a.unreadLoading = true
	// 一页就到底：别让列表的无限滚动在测试里顺手再拉一页，把行换掉。
	st := a.tabs[notificationTabIndex()]
	st.page, st.totalPages, st.noMore = 1, 1, true
	read := make(chan []string, 1)
	a.onNotificationsRead = func(ids []string) { read <- ids }

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	a.pageName = "list"
	if err := tt.Click("标为已读"); err != nil {
		t.Fatalf("点不到「标为已读」：%v：%q", err, tt.Texts())
	}
	select {
	case ids := <-read:
		if len(ids) != 1 || ids[0] != "n1" {
			t.Errorf("只应标记这一条：%v", ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("点「标为已读」没有发起已读请求")
	}
	if a.pageName != "list" {
		t.Errorf("点「标为已读」不该打开消息：%q", a.pageName)
	}
	if got := calls(); len(got) != 1 || got[0].body != `{"id":["n1"]}` {
		t.Errorf("已读请求 = %+v", got)
	}
}

// 表态消息的界面：一个人的两种表情合并成一行，用户名只出现一次，不套括号。
func TestNotificationReactionRowNamesOnce(t *testing.T) {
	a := notificationWindowApp(t)
	st := a.tabs[notificationTabIndex()]
	st.items = []Item{{Key: "r1", Notif: &notification{
		ID: "r1", Type: "reaction",
		Payload: mustObject(t, `{"reactionType":"post","postTitle":"一条帖子",
			"relations":[{"emoji":"👍","username":"jimmy"},
				{"emoji":"😂","username":"jimmy"}],
			"reactions":{"👍":1,"😂":1}}`),
	}}}
	a.unread = 1

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	texts := tt.Texts()
	mentions := 0
	for _, text := range texts {
		mentions += strings.Count(text, "@jimmy")
		if strings.ContainsAny(text, "（）") {
			t.Errorf("表态不该套括号：%q", text)
		}
	}
	if mentions != 1 {
		t.Errorf("用户名出现了 %d 次，应只写一次：%q", mentions, texts)
	}
	if !tt.HasText("👍") || !tt.HasText("😂") {
		t.Errorf("两种表情应合并显示在行内：%q", texts)
	}
	if tt.HasText("👍 1") {
		t.Errorf("只有一次表态时不该写数字：%q", texts)
	}
	if !tt.HasText("表态了你的帖子") {
		t.Errorf("表态消息缺少动词：%q", texts)
	}
}

// 多个人表态：名字用顿号连起来写在一处，超出上限的人用「等 N 人」概括，
// 仍然不套括号。
func TestNotificationReactionRowOthers(t *testing.T) {
	a := notificationWindowApp(t)
	st := a.tabs[notificationTabIndex()]
	st.items = []Item{{Key: "r2", Notif: &notification{
		ID: "r2", Type: "reaction",
		Payload: mustObject(t, `{"reactionType":"comment","postTitle":"一条帖子",
			"relations":[{"emoji":"👍","username":"a"},{"emoji":"👍","username":"b"},
				{"emoji":"👍","username":"c"},{"emoji":"👍","username":"d"}],
			"reactions":{"👍":4}}`),
	}}}

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	texts := tt.Texts()
	for _, want := range []string{"@a", "@b", "@c", "等 4 人"} {
		if !tt.HasText(want) {
			t.Errorf("表态行缺少 %q：%q", want, texts)
		}
	}
	if tt.HasText("@d") {
		t.Errorf("超出上限的人不该单独点名：%q", texts)
	}
	if !tt.HasText("👍 4") {
		t.Errorf("表情胶囊应带上次数：%q", texts)
	}
	for _, text := range texts {
		if strings.ContainsAny(text, "（）") {
			t.Errorf("表态不该套括号：%q", text)
		}
	}
}

// notificationWindowApp 造一份已登录、已加载一页消息的状态。窗口本身由
// 测试直接渲染 notificationsWindowView，headless 不创建真实窗口。
func notificationWindowApp(t *testing.T) *app {
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
	setNotificationSelected(st, "n1", true)
	return a
}

// 未登录时不发请求：点铃铛先弹全局授权窗，通知窗口本身不打开（通知需要
// read:me）。
func TestNotificationBellNeedsLogin(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("通知"); err != nil {
		t.Fatalf("点击铃铛失败：%v", err)
	}
	tt.Frame()
	if a.notifWin != nil {
		t.Error("未登录时不该打开通知窗口")
	}
	if !a.authGate.open {
		t.Error("未登录点铃铛应弹出授权弹窗")
	}
	if !tt.HasText("登录 2Libra 后才能查看「通知」。") {
		t.Errorf("授权弹窗没有说明原因：%q", tt.Texts())
	}
}

// 点开一条帖子 / 评论类消息，主窗口切到该帖（带评论定位），通知窗口不关；
// 其它目标留在应用内（关注开资料卡，其余留在消息中心），不外跳浏览器。
func TestNotificationTargetOpensInMainWindow(t *testing.T) {
	a := notificationWindowApp(t)
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.items = []Item{{Key: "r1", Notif: &notification{
		ID: "r1", Type: "reply", CreatedAt: "2026-11-12T04:00:00Z",
		FromUser: mustObject(t, `{"username":"alice"}`),
		Payload: mustObject(t, `{"postShortId":"wEOIDnn","postNodeSlug":"social",
			"postTitle":"社会观察","commentId":"c1","commentText":"说得对",
			"commentLevel":1}`),
	}}}

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	a.notifShown = true // 模拟窗口正开着：点开消息不该把它收起来
	// 点卡片上不属于用户标识的部分：句首的头像与昵称现在是统一用户标识的
	// 点击区，点它会打开资料卡而不是这条消息的目标。
	if err := tt.Click("《社会观察》"); err != nil {
		t.Fatalf("点击消息行失败：%v", err)
	}
	if !a.notifShown {
		t.Error("点开帖子后通知窗口不该被关掉")
	}
	if a.pageName != "detail" {
		t.Fatalf("点开消息后主窗口应进入详情页，实际 %q", a.pageName)
	}
	if a.detail == nil || a.detail.shortID != "wEOIDnn" {
		t.Fatalf("主窗口没有打开这条消息的帖子：%+v", a.detail)
	}
	if a.detail.focusID != "c1" {
		t.Errorf("评论定位 = %q，期望 c1", a.detail.focusID)
	}

	// 关注消息指向站点用户页：在应用内打开那个人的资料卡，主窗口留在列表，
	// 也不交给系统浏览器（浏览器里可能是另一个账号）。
	a.pageName = "list"
	a.detail = nil
	a.userCard = nil
	st.items = []Item{{Key: "f1", Notif: &notification{
		ID: "f1", Type: "follow", IsRead: true,
		Payload: mustObject(t, `{"followerUsername":"bob"}`),
	}}}
	tt2 := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt2.Frame()
	// 同样避开句首的用户标识：点消息类型「关注」这一处。
	if err := tt2.Click("关注"); err != nil {
		t.Fatalf("点击消息行失败：%v", err)
	}
	if a.pageName != "list" {
		t.Errorf("站点链接不该把主窗口切走：%q", a.pageName)
	}
	if a.userCard == nil || a.userCard.username != "bob" {
		t.Errorf("关注消息没有在应用内打开 bob 的资料卡：%+v", a.userCard)
	}
	if urls := tt2.OpenedURLs(); len(urls) != 0 {
		t.Errorf("通知被交给了系统浏览器：%v", urls)
	}

	// 其它站点目标（RSS、投票、系统等）留在应用的消息中心里，不外跳浏览器。
	st.items = []Item{{Key: "s1", Notif: &notification{
		ID: "s1", Type: "system", IsRead: true,
		Payload: mustObject(t, `{"message":"订阅更新了","path":"/article/42"}`),
	}}}
	tt3 := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt3.Frame()
	if err := tt3.Click("订阅更新了"); err != nil {
		t.Fatalf("点击系统消息失败：%v", err)
	}
	if urls := tt3.OpenedURLs(); len(urls) != 0 {
		t.Errorf("系统消息被交给了系统浏览器：%v", urls)
	}
}

// 从通知跳到具体评论：默认把左侧正文收起，评论区占满整行，进来即见目标楼层。
// 同时确认不影响待已读记账，也不改变"目标没有评论定位"与"非通知跳转"的行为。
func TestNotificationCommentClickCollapsesBody(t *testing.T) {
	srv, _ := readRecorder(t)
	a, _ := notificationApp(t, srv)
	a.autoLoad = false

	// 通知 + 评论定位：收起正文，且待已读照常记账。
	a.openNotificationTarget(siteBaseURL+"/post/social/wEOIDnn?commentId=c1", "n1")
	if a.detail == nil || !a.detail.bodyCollapsed {
		t.Fatalf("从通知跳到评论应默认收起正文：%+v", a.detail)
	}
	if a.detail.notifID != "n1" || a.detail.notifCommentID != "c1" {
		t.Fatalf("收起正文破坏了待已读记账：%+v", a.detail)
	}

	// 通知 + 无评论定位（表态 / 收藏了帖子）：正文保持展开。
	a.openNotificationTarget(siteBaseURL+"/post/social/wEOIDnn", "n1")
	if a.detail == nil || a.detail.bodyCollapsed {
		t.Fatalf("通知跳到帖子正文不该收起：%+v", a.detail)
	}
	if a.detail.notifID != "" || a.detail.notifCommentID != "" {
		t.Fatalf("无评论定位不该记待已读：%+v", a.detail)
	}

	// 非通知路径（从列表按 CommentID 点进来）：正文仍展开。
	a.openDetail(Item{ShortID: "wEOIDnn", CommentID: "c1", URL: siteBaseURL + "/post/social/wEOIDnn"})
	if a.detail == nil || a.detail.bodyCollapsed {
		t.Fatalf("非通知跳转不该收起正文：%+v", a.detail)
	}
}

// 点开一条带评论定位的消息：评论还没出现在屏幕上时不标已读；那层楼真正
// 露出来之后自动提交一次按条已读。
func TestNotificationCommentClickMarksReadWhenVisible(t *testing.T) {
	srv, calls := readRecorder(t)
	a, st := notificationApp(t, srv)
	// autoLoad 关着：打开详情不拉正文与评论（由测试摆好），按条已读仍发往
	// 本地假服务。
	a.autoLoad = false
	a.openNotificationTarget(siteBaseURL+"/post/social/wEOIDnn?commentId=c1", "n1")
	if a.detail == nil || a.detail.notifID != "n1" || a.detail.notifCommentID != "c1" {
		t.Fatalf("点开消息没有记下待已读的评论：%+v", a.detail)
	}
	topic, _ := decodeTopic([]byte(detailTopicJSON), "wEOIDnn")
	a.detail.topic = &topic

	tt := ui.NewTester(a.view, 1100, 760)
	d := a.detail
	// 点开消息时评论还没到：待办先挂起；评论到位前的帧不该标已读。
	d.notifID, d.notifCommentID = "n1", "c1"
	d.replyList = nil
	d.replyTotal = 0
	tt.Frame()
	if d.notifID != "n1" {
		t.Fatalf("评论没到时不该落账：notifID=%q", d.notifID)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("评论没到时不该发已读请求：%+v", got)
	}
	d.replyList = []replyItem{
		{ID: "c1", Author: "u", Floor: 1, Content: "被回复的那条"},
		{ID: "c2", Author: "u", Floor: 2, Content: "另一条评论"},
	}
	d.replyTotal = 2
	// 先把列表排好一帧，下一帧目标评论就在可视区里。
	tt.Frame()
	if f, l := d.list.Visible(); f > 0 || l < 0 || d.notifID != "" {
		t.Fatalf("评论到位后仍在可视区外：visible=%d..%d notifID=%q", f, l, d.notifID)
	}
	tt.Frame()
	waitReadCall(t, calls, `{"id":["n1"]}`)
	// 本地落账在请求结果回来之后：无头测试里它在请求协程里执行，等一拍。
	deadline := time.Now().Add(5 * time.Second)
	for !st.items[0].Notif.IsRead && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !st.items[0].Notif.IsRead {
		t.Error("自动已读没有落到本地消息行")
	}
	if d.notifID != "" || d.notifCommentID != "" {
		t.Error("看完后不该还挂着待已读的消息")
	}
	// 只落一次：后续帧不再重复提交。
	tt.Frame()
	tt.Frame()
	if got := calls(); len(got) != 1 || got[0].body != `{"id":["n1"]}` {
		t.Errorf("自动已读请求 = %+v", got)
	}
}

// waitReadCall 等本地假服务收到第一条已读请求再断言：请求本身在协程里发，
// 帧返回时它可能还没到。
func waitReadCall(t *testing.T, calls func() []readCall, body string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := calls()
	if len(got) == 0 || got[0].body != body {
		t.Fatalf("已读请求 = %+v，期望 %s", got, body)
	}
}

// 目标评论不在首屏时先保持未读：用户没滚到那层楼就不算看过；滚进可视区
// 之后才落账。
func TestNotificationCommentReadWaitsUntilVisible(t *testing.T) {
	srv, calls := readRecorder(t)
	a, st := notificationApp(t, srv)
	a.autoLoad = false
	a.openNotificationTarget(siteBaseURL+"/post/social/wEOIDnn?commentId=c50", "n1")
	topic, _ := decodeTopic([]byte(detailTopicJSON), "wEOIDnn")
	a.detail.topic = &topic
	rows := make([]replyItem, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, replyItem{
			ID: fmt.Sprintf("c%d", i), Author: "u", Floor: i + 1,
			Content: fmt.Sprintf("第%d条评论", i+1),
		})
	}
	a.detail.replyList = rows
	a.detail.replyTotal = len(rows)
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	// 这里 autoLoad 关着、评论由测试摆好：先确认没看到评论时没有落账。
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if a.detail.notifID != "n1" || st.items[0].Notif.IsRead {
		t.Fatal("没看到目标评论就把消息标成了已读")
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("目标评论还没露出来就提交了已读：%+v", got)
	}

	// 滚到第 51 层：从下一帧起它在可视区里，消息这才落账。
	a.detail.list.ScrollTo(50, ui.Start)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if a.detail.notifID != "" {
		t.Error("目标评论进入可视区后仍挂着待已读消息")
	}
	waitReadCall(t, calls, `{"id":["n1"]}`)
	deadline := time.Now().Add(5 * time.Second)
	for !st.items[0].Notif.IsRead && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !st.items[0].Notif.IsRead {
		t.Error("自动已读没有落到本地消息行")
	}
}

// 系统通知 id 里的消息 id 用来按条已读；认不出前缀的 id 不做猜测。
func TestNoticeMessageID(t *testing.T) {
	cases := map[string]string{
		"t2-notification:n1":              "n1",
		"t2-notification:jimmy-1a2b3c:n1": "n1",
		"t2-notification:legacy":          "legacy",
		"other-notification:n1":           "",
		"":                                "",
	}
	for id, want := range cases {
		if got := noticeMessageID(id); got != want {
			t.Errorf("noticeMessageID(%q) = %q，期望 %q", id, got, want)
		}
	}
}

// 窗口拉到最小尺寸时列表仍在窗口内滚动，标题栏按钮也还在。
func TestNotificationWindowFitsMinSize(t *testing.T) {
	a := notificationWindowApp(t)
	tt := ui.NewTester(a.notificationsWindowView, notifWindowMinWidth, notifWindowMinHeight)
	tt.Frame()
	closeR, ok := tt.Find("关闭")
	if !ok {
		t.Fatalf("小窗口里找不到关闭按钮：%q", tt.Texts())
	}
	if closeR.X < 0 || closeR.X+closeR.W > notifWindowMinWidth {
		t.Errorf("标题栏按钮超出窗口：%v", closeR)
	}
	row, ok := tt.Find("这条是提及")
	if !ok {
		t.Fatalf("小窗口里没有消息行：%q", tt.Texts())
	}
	if row.Y < 0 || row.Y+row.H > notifWindowMinHeight {
		t.Errorf("消息行超出窗口：%v", row)
	}
}

// 原生通知的点击回调只带 id：进程内能查到目标时在应用内打开，查不到时由
// 账号册决定落点（见 accounts.go 的 openNotice）。
func TestNotificationNoticeTarget(t *testing.T) {
	a := testApp(t)
	a.rememberNoticeTarget("t2-notification:n1", "https://2libra.com/post/ai-tools/ABC12345")
	if got := a.noticeTarget("t2-notification:n1"); got != "https://2libra.com/post/ai-tools/ABC12345" {
		t.Errorf("点击目标 = %q", got)
	}
	if got := a.noticeTarget("t2-notification:gone"); got != "" {
		t.Errorf("查不到目标时应为空串：%q", got)
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

// 多账号时系统通知写明是哪个账号收到的；单账号不写，免得白占一行。
func TestNativeNoticeNamesAccountOnlyWhenMultiAccount(t *testing.T) {
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n1","type":"reply","payload":{"postShortId":"wEOIDnn","postNodeSlug":"social",
			"postTitle":"社会观察"}
	}`))
	n.FromUser = mustObject(t, `{"username":"alice"}`)

	multi := multiAccountWindow(t, "jimmy")
	notice := multi.notificationNative(n)
	if notice.Subtitle != "账号 @jimmy" {
		t.Errorf("多账号的通知副标题 = %q", notice.Subtitle)
	}
	if notice.AccountID != multi.accountID || notice.ID != notificationNativeID(multi.accountID, "n1") {
		t.Errorf("通知没有带上账号：%+v", notice)
	}

	single := singleAccountWindow(t, "jimmy")
	notice = single.notificationNative(n)
	if notice.Subtitle != "" {
		t.Errorf("单账号的通知不该写账号：%q", notice.Subtitle)
	}
	if notice.Title != "@alice 回复了你" {
		t.Errorf("通知标题 = %q", notice.Title)
	}
}

/* ------------------------------------------------------------------
   通知快捷回复
----------------------------------------------------------------- */

// 快捷回复要按消息类型与 commentLevel 组装父评论与写层级：reply 的
// commentLevel 是被回复对象的层级，mention 的是被提及评论本身的层级。
func TestNotificationQuickReplyTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		ok      bool
		shortID string
		target  replyTarget
	}{
		{
			name:    "回复帖子下的一级回复",
			raw:     `{"id":"n1","type":"reply","payload":{"postShortId":"abc12345","commentId":"c1","commentLevel":0}}`,
			ok:      true,
			shortID: "abc12345",
			target:  replyTarget{ID: "c1", Level: 1},
		},
		{
			name:    "回复你的评论",
			raw:     `{"id":"n1","type":"reply","payload":{"postShortId":"abc12345","commentId":"c1","commentLevel":1}}`,
			ok:      true,
			shortID: "abc12345",
			target:  replyTarget{ID: "c1", Level: 2},
		},
		{
			name: "三级评论缺父评论时不给快捷回复",
			raw:  `{"id":"n1","type":"reply","payload":{"postShortId":"abc12345","commentId":"c1","commentLevel":2}}`,
		},
		{
			name:    "三级评论带父评论",
			raw:     `{"id":"n1","type":"reply_mention","payload":{"postShortId":"abc12345","commentId":"c1","commentLevel":2,"parentCommentId":"p1"}}`,
			ok:      true,
			shortID: "abc12345",
			target:  replyTarget{ID: "c1", Level: 3, ParentID: "p1"},
		},
		{
			name:    "回复附言时发顶层评论",
			raw:     `{"id":"n1","type":"reply","payload":{"postShortId":"abc12345","target":"postscript"}}`,
			ok:      true,
			shortID: "abc12345",
		},
		{
			name:    "帖子里提及",
			raw:     `{"id":"n1","type":"mention","payload":{"postShortId":"abc12345","commentLevel":0}}`,
			ok:      true,
			shortID: "abc12345",
		},
		{
			name:    "评论里提及",
			raw:     `{"id":"n1","type":"mention","payload":{"postShortId":"abc12345","commentId":"c2","commentLevel":2}}`,
			ok:      true,
			shortID: "abc12345",
			target:  replyTarget{ID: "c2", Level: 2},
		},
		{
			name:    "path 里带帖子定位",
			raw:     `{"id":"n1","type":"mention","payload":{"path":"/post/social/ABC12345?commentId=c3","commentId":"c3","commentLevel":1}}`,
			ok:      true,
			shortID: "ABC12345",
			target:  replyTarget{ID: "c3", Level: 1},
		},
		{
			name: "表态不可回复",
			raw:  `{"id":"n1","type":"reaction","payload":{"postShortId":"abc12345"}}`,
		},
		{
			name: "缺帖子定位",
			raw:  `{"id":"n1","type":"reply","payload":{"commentId":"c1","commentLevel":1}}`,
		},
		{
			name: "说是评论回复但缺评论 id",
			raw:  `{"id":"n1","type":"reply","payload":{"postShortId":"abc12345","commentLevel":1}}`,
		},
		{
			name: "说是评论提及但缺评论 id",
			raw:  `{"id":"n1","type":"mention","payload":{"postShortId":"abc12345","commentLevel":2}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := decodeNotificationObject(mustObject(t, tc.raw))
			if n == nil {
				t.Fatal("消息解码失败")
			}
			v := notificationViewOf(n)
			if v.CanReply != tc.ok {
				t.Fatalf("CanReply = %v，期望 %v", v.CanReply, tc.ok)
			}
			if !tc.ok {
				return
			}
			if v.ReplyShortID != tc.shortID || v.ReplyTarget != tc.target {
				t.Errorf("快捷回复目标 = %q / %+v，期望 %q / %+v",
					v.ReplyShortID, v.ReplyTarget, tc.shortID, tc.target)
			}
		})
	}
}

// 快捷回复的完整链路：在消息卡片里展开输入框、打字、发送，请求必须带
// replyCommentId 与显式 level；成功后收起输入框、清空草稿并提示结果。
func TestNotificationQuickReplyPostsComment(t *testing.T) {
	var mu sync.Mutex
	var path, body, key string
	posted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/replies"):
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			path, body, key = r.URL.Path, string(b), r.Header.Get("Idempotency-Key")
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"r1","content":"收到","author":{"username":"jimmy"}}`))
			posted <- struct{}{}
		case strings.HasSuffix(r.URL.Path, "/notifications/read"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"ok":true}`))
		default:
			t.Errorf("快捷回复发出了意外请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}
	a.unreadLoading = true
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.total, st.totalPages, st.page = 1, 1, 1
	st.items = []Item{{Key: "n1", Notif: &notification{
		ID: "n1", Type: "reply", IsRead: false,
		FromUser: mustObject(t, `{"username":"alice"}`),
		Payload: mustObject(t, `{"postShortId":"abc12345","commentId":"c1",
			"commentLevel":1,"commentText":"说得对","postTitle":"一条帖子"}`),
	}}}

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	a.pageName = "list"
	if err := tt.Click("快捷回复"); err != nil {
		t.Fatalf("找不到快捷回复入口：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.pageName != "list" {
		t.Errorf("点快捷回复不该打开帖子：%q", a.pageName)
	}
	if !tt.HasText("快捷回复内容") {
		t.Fatalf("快捷回复输入框没有展开：%q", tt.Texts())
	}
	if err := tt.Click("快捷回复内容"); err != nil {
		t.Fatalf("点输入框失败：%v", err)
	}
	tt.Type("收到")
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatalf("点发送失败：%v（%q）", err, tt.Texts())
	}

	select {
	case <-posted:
	case <-time.After(5 * time.Second):
		t.Fatal("快捷回复请求超时")
	}
	waitFor(t, func() bool { return !st.quickReplyPosting }, "快捷回复没有收尾")

	mu.Lock()
	gotPath, gotBody, gotKey := path, body, key
	mu.Unlock()
	if gotPath != "/api/v1/topics/abc12345/replies" {
		t.Errorf("发评论路径 = %q", gotPath)
	}
	if gotKey == "" {
		t.Error("快捷回复缺少 Idempotency-Key")
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("请求体不是 JSON：%s", gotBody)
	}
	if sent["content"] != "收到" || sent["replyCommentId"] != "c1" ||
		sent["parentId"] != "c1" || sent["level"] != float64(2) {
		t.Errorf("快捷回复请求体不对：%s", gotBody)
	}
	if st.quickReplyOpen != "" || st.quickReplyDraft != "" {
		t.Errorf("发送成功后没有收起输入框：open=%q draft=%q", st.quickReplyOpen, st.quickReplyDraft)
	}
	waitFor(t, func() bool { return st.items[0].Notif.IsRead }, "回复成功后没有把消息标为已读")
	tt.Frame()
	if !tt.HasText("已回复 @alice") {
		t.Errorf("没有提示回复结果：%q", tt.Texts())
	}
}

// 发送失败时保留草稿与输入框：用户不必重新打一遍。
func TestNotificationQuickReplyKeepsDraftOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"c":500,"code":"server_error","m":"服务端错误","d":null}`)
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}
	a.unreadLoading = true
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.total, st.totalPages, st.page = 1, 1, 1
	st.items = []Item{{Key: "n1", Notif: &notification{
		ID: "n1", Type: "reply", IsRead: false,
		FromUser: mustObject(t, `{"username":"alice"}`),
		Payload:  mustObject(t, `{"postShortId":"abc12345","commentId":"c1","commentLevel":1}`),
	}}}

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if err := tt.Click("快捷回复"); err != nil {
		t.Fatalf("找不到快捷回复入口：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("快捷回复内容"); err != nil {
		t.Fatalf("点输入框失败：%v", err)
	}
	tt.Type("没发出去的回复")
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatalf("点发送失败：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return !st.quickReplyPosting }, "快捷回复没有收尾")

	if st.quickReplyOpen != "n1" || st.quickReplyDraft != "没发出去的回复" {
		t.Errorf("失败后草稿/输入框没有保留：open=%q draft=%q",
			st.quickReplyOpen, st.quickReplyDraft)
	}
	tt.Frame()
	if !tt.HasText("回复失败") {
		t.Errorf("没有报告失败原因：%q", tt.Texts())
	}
}

// 只有未读消息才提供快捷回复：已读消息已经看过，卡片头不再出现入口。
func TestNotificationQuickReplyOnlyForUnread(t *testing.T) {
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.unreadLoading = true
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.total, st.totalPages, st.page = 1, 1, 1
	st.items = []Item{{Key: "n1", Notif: &notification{
		ID: "n1", Type: "reply", IsRead: true,
		FromUser: mustObject(t, `{"username":"alice"}`),
		Payload:  mustObject(t, `{"postShortId":"abc12345","commentId":"c1","commentLevel":1}`),
	}}}

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if tt.HasText("快捷回复") {
		t.Fatalf("已读消息不该提供快捷回复：%q", tt.Texts())
	}

	st.items[0].Notif.IsRead = false
	tt.Frame()
	if !tt.HasText("快捷回复") {
		t.Errorf("未读消息应提供快捷回复：%q", tt.Texts())
	}
}
