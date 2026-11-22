package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// @抽奖助手 的契约与行为按 api-write.md「提及（@）与抽奖」逐条断言：
// 触发是一级评论，数量必填；201 只表示写入，开奖靠轮询 extra.lottery；
// 失败原因走 type=system 通知。

func TestLotteryCommand(t *testing.T) {
	if got, want := lotteryCommand(2), "@抽奖助手 抽 2 个"; got != want {
		t.Errorf("触发指令 = %q，期望 %q", got, want)
	}
	// @ 后面必须有空格，否则服务端把整段当用户名，既不高亮也不触发。
	if !strings.HasPrefix(lotteryCommand(10), "@抽奖助手 ") {
		t.Errorf("触发指令的 @ 后面缺空格：%q", lotteryCommand(10))
	}
}

func TestDecodeTopicLottery(t *testing.T) {
	full := `{"short_id":"N","extra":{"lottery":{"finished_at":"2026-11-02T10:05:00.000Z",` +
		`"comment":{"id":"c9","located_floor":9,"flat_floor":12},` +
		`"winners":[{"username":"jimmy","floor":3,"flat_floor":3,"comment_id":"c3"},` +
		`{"username":" ","floor":4}]}}}`
	topic, err := decodeTopic([]byte(full), "N")
	if err != nil {
		t.Fatal(err)
	}
	lot := topic.Lottery
	if lot == nil {
		t.Fatal("extra.lottery 没有解析出来")
	}
	if lot.FinishedAt != "2026-11-02T10:05:00.000Z" || lot.Comment.ID != "c9" ||
		lot.Comment.LocatedFloor != 9 || lot.Comment.FlatFloor != 12 {
		t.Errorf("开奖元信息解码不对：%+v", lot)
	}
	if len(lot.Winners) != 1 {
		t.Fatalf("空白用户名应被忽略，得到 %+v", lot.Winners)
	}
	if w := lot.Winners[0]; w.Username != "jimmy" || w.Floor != 3 || w.FlatFloor != 3 || w.CommentID != "c3" {
		t.Errorf("中奖者字段解码不对：%+v", w)
	}

	// 顶层也认；空对象 / 缺失都不算「已开奖」，否则作者永远看不到入口。
	top, err := decodeTopic([]byte(`{"short_id":"N","lottery":{"winners":[{"username":"a"}]}}`), "N")
	if err != nil || top.Lottery == nil || len(top.Lottery.Winners) != 1 {
		t.Errorf("顶层 lottery 没认出来：%+v %v", top.Lottery, err)
	}
	for _, raw := range []string{
		`{"short_id":"N"}`,
		`{"short_id":"N","extra":{"lottery":{}}}`,
		`{"short_id":"N","extra":{"lottery":{"finished_at":null}}}`,
	} {
		plain, err := decodeTopic([]byte(raw), "N")
		if err != nil {
			t.Fatal(err)
		}
		if plain.Lottery != nil {
			t.Errorf("空开奖块不该算已开奖：%s → %+v", raw, plain.Lottery)
		}
	}
}

func TestLotteryResultText(t *testing.T) {
	info := &lotteryInfo{Winners: []lotteryWinner{
		{Username: "jimmy", Floor: 3},
		{Username: "bob", FlatFloor: 7},
	}}
	got := lotteryResultText(info)
	for _, want := range []string{"@jimmy（3 楼）", "@bob（7 楼）"} {
		if !strings.Contains(got, want) {
			t.Errorf("开奖提示 %q 缺少 %q", got, want)
		}
	}
	many := make([]lotteryWinner, 8)
	for i := range many {
		many[i] = lotteryWinner{Username: fmt.Sprintf("u%d", i), Floor: i + 1}
	}
	got = lotteryResultText(&lotteryInfo{Winners: many})
	if !strings.Contains(got, "等 8 人") || strings.Contains(got, "u7") {
		t.Errorf("超过 5 人应折叠：%q", got)
	}
}

// lotteryPost 记录一次触发评论的请求。
type lotteryPost struct {
	path, auth, key, body string
}

type lotteryServer struct {
	mu          sync.Mutex
	posts       []lotteryPost
	gets        int
	lotteryFrom int // 第几次 GET 详情开始带开奖结果；0 表示始终不带
	reason      string
}

func (s *lotteryServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/replies"):
		s.posts = append(s.posts, lotteryPost{
			path: r.URL.Path, auth: r.Header.Get("Authorization"),
			key: r.Header.Get("Idempotency-Key"), body: string(raw),
		})
		s.mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"new1","floor":5,"flat_floor":5,"author":{"username":"jimmy"}}`))
		return
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/topics/a1b2"):
		s.gets++
		n := s.gets
		from := s.lotteryFrom
		s.mu.Unlock()
		body := `{"short_id":"a1b2","title":"标题","author":{"username":"jimmy"}`
		if from > 0 && n >= from {
			body += `,"extra":{"lottery":{"finished_at":"2026-11-02T10:05:00.000Z",` +
				`"comment":{"id":"c9"},"winners":[{"username":"jimmy","floor":3}]}}`
		}
		_, _ = fmt.Fprint(w, envelopeJSON(body+`}`))
		return
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/notifications"):
		reason := s.reason
		if reason == "" {
			reason = "帖子不是你的"
		}
		s.mu.Unlock()
		payload, _ := json.Marshal(map[string]any{"error": reason})
		created := time.Now().UTC().Add(-time.Second).Format("2006-01-02T15:04:05.000Z")
		note := fmt.Sprintf(`{"id":"n1","type":"system","created_at":"%s","payload":%s}`, created, payload)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"list":[`+note+`]}`))
		return
	default:
		s.mu.Unlock()
		http.NotFound(w, r)
	}
}

func (s *lotteryServer) snapshot() ([]lotteryPost, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]lotteryPost(nil), s.posts...), s.gets
}

func lotteryTestApp(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
		Scope: "read profile write:topic write:comment",
	}); err != nil {
		t.Fatal(err)
	}
	a.client.Base = srv.URL
	a.autoLoad = true
	a.pageName = "detail"
	a.detail = &detailState{shortID: "a1b2", topic: &topicDetail{
		ShortID: "a1b2", Title: "标题", Author: "jimmy", AuthorID: "u1",
		URL: "https://2libra.com/post/work/a1b2",
	}}
	return a
}

func fastLotteryPoll(t *testing.T) {
	t.Helper()
	oldI, oldT := lotteryPollInterval, lotteryPollTimeout
	lotteryPollInterval, lotteryPollTimeout = 5*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { lotteryPollInterval, lotteryPollTimeout = oldI, oldT })
}

// 完整链路：发触发评论 → 轮询详情 → 开奖结果用 toast 报出，工具条回到可点。
// 顺带断言触发评论的路径、鉴权、幂等键与正文（level=0，不带 parentId）。
func TestLotteryFlowPostsTriggerAndShowsWinners(t *testing.T) {
	fastLotteryPoll(t)
	srv := &lotteryServer{lotteryFrom: 2}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)

	a.detail.lottery.count = 2
	a.startLottery(a.detail)
	waitFor(t, func() bool { return !a.detail.lottery.busy }, "抽奖流程没有结束")

	posts, gets := srv.snapshot()
	if len(posts) != 1 {
		t.Fatalf("触发评论次数 = %d，期望 1：%+v", len(posts), posts)
	}
	p := posts[0]
	if p.path != "/v1/topics/a1b2/replies" {
		t.Errorf("触发评论路径 = %q", p.path)
	}
	if p.auth != "Bearer tok" || p.key == "" {
		t.Errorf("鉴权 / 幂等键 = %q / %q", p.auth, p.key)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(p.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "@抽奖助手 抽 2 个" || body["level"] != float64(0) {
		t.Errorf("触发评论正文 = %v，期望一级评论的抽奖指令", body)
	}
	if _, ok := body["parentId"]; ok {
		t.Errorf("触发评论不能带 parentId：%v", body)
	}
	if gets < 2 {
		t.Errorf("没有轮询详情：GET %d 次", gets)
	}
	if a.detail.topic.Lottery == nil || len(a.detail.topic.Lottery.Winners) != 1 {
		t.Errorf("开奖结果没有落到页面：%+v", a.detail.topic.Lottery)
	}
	if !hasToastContaining(a, "@jimmy（3 楼）") {
		t.Errorf("没有开奖 toast：%+v", a.toasts)
	}
	// 触发评论本身也立刻进了评论列表。
	if len(a.detail.replyList) != 1 || a.detail.replyList[0].Content != "@抽奖助手 抽 2 个" {
		t.Errorf("触发评论没有就地插入：%+v", a.detail.replyList)
	}
}

// 抽奖只给帖主：canLottery 判断与作者一致，非作者不能展开确认框或发请求。
func TestLotteryOnlyForAuthor(t *testing.T) {
	srv := &lotteryServer{}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)
	if !a.canLottery(a.detail) {
		t.Fatal("作者的帖子应该允许抽奖")
	}

	a.detail.topic.Author = "alice"
	if a.canLottery(a.detail) {
		t.Error("别人的帖子不该允许抽奖")
	}
	a.askLottery(a.detail)
	posts, _ := srv.snapshot()
	if len(posts) != 0 {
		t.Errorf("非作者不该发出任何请求：%v", posts)
	}
}

// 人数由调用方传入：startLottery 用选中的数量发触发评论。
func TestLotteryStartUsesSelectedCount(t *testing.T) {
	srv := &lotteryServer{lotteryFrom: 1}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)
	fastLotteryPoll(t)

	a.detail.lottery.count = 3
	a.startLottery(a.detail)
	waitFor(t, func() bool { return !a.detail.lottery.busy }, "抽奖流程没有结束")
	posts, _ := srv.snapshot()
	if len(posts) != 1 || !strings.Contains(posts[0].body, "@抽奖助手 抽 3 个") {
		t.Errorf("触发评论没有用选中的人数：%+v", posts)
	}
}

// 同一帖只能开一次：已经开过奖时不再发触发评论，直接提示。
func TestLotteryAlreadyDrawnSkipsTrigger(t *testing.T) {
	srv := &lotteryServer{}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)
	a.detail.topic.Lottery = &lotteryInfo{FinishedAt: "x", Winners: []lotteryWinner{{Username: "a"}}}
	a.detail.lottery.count = 3
	a.startLottery(a.detail)
	posts, gets := srv.snapshot()
	if len(posts) != 0 || gets != 0 {
		t.Errorf("已开奖不该再发请求：posts=%v gets=%d", posts, gets)
	}
	if !hasToastContaining(a, "已经开过奖") {
		t.Errorf("没有说明不能重复抽奖：%+v", a.toasts)
	}
}

func TestLotteryFailureReasonFromSystemNotification(t *testing.T) {
	srv := &lotteryServer{}
	s := httptest.NewServer(srv)
	defer s.Close()
	c := &Client{HTTP: s.Client(), Base: s.URL + "/api", bearer: func() string { return "tok" }}
	got := c.lotteryFailureReason(context.Background(), "2026-01-01T00:00:00")
	if got != "帖子不是你的" {
		t.Errorf("失败原因 = %q，期望系统通知里的 error", got)
	}
	// 早于本次抽奖的旧通知不能冒充原因。
	if got := c.lotteryFailureReason(context.Background(), "2099-01-01T00:00:00"); got != "" {
		t.Errorf("旧通知应被过滤掉，得到 %q", got)
	}
}

// 超时后把系统通知里的失败原因展示出来，而不是静默结束。
func TestLotteryTimeoutShowsReason(t *testing.T) {
	fastLotteryPoll(t)
	srv := &lotteryServer{reason: "帖子不是你的"}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)
	a.startLottery(a.detail)
	waitFor(t, func() bool { return !a.detail.lottery.busy }, "抽奖超时没有收尾")
	tt := ui.NewTester(a.view, 900, 700)
	tt.Frame()
	if !tt.HasText("帖子不是你的") {
		t.Errorf("没有展示开奖失败原因：%q", tt.Texts())
	}
}

// 抽奖确认框接在详情工具条上：入口只在帖主的「更多」菜单里；点开先选人数
// 并确认，确认之后才发触发评论。
func TestLotteryDialogConfirmsAndStarts(t *testing.T) {
	srv := &lotteryServer{lotteryFrom: 1}
	s := httptest.NewServer(srv)
	defer s.Close()
	a := lotteryTestApp(t, s)
	fastLotteryPoll(t)

	found := false
	for _, act := range a.detailToolActions(a.detail) {
		if act.key == "lottery" {
			found = true
			if act.label != "发起抽奖" {
				t.Errorf("抽奖入口文案 = %q，期望「发起抽奖」", act.label)
			}
		}
	}
	if !found {
		t.Fatal("帖主的详情工具条里没有抽奖入口")
	}

	a.detail.lottery.count = 2
	a.askLottery(a.detail)
	if !a.detail.lottery.awaiting {
		t.Fatal("点抽奖入口应展开确认框")
	}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"发起抽奖？", "中奖人数", "确认发起抽奖"} {
		if !tt.HasText(want) {
			t.Errorf("抽奖确认框缺少 %q：%q", want, tt.Texts())
		}
	}
	if posts, _ := srv.snapshot(); len(posts) != 0 {
		t.Fatalf("只展开确认框不该发请求：%+v", posts)
	}

	if err := tt.Click("确认发起抽奖"); err != nil {
		t.Fatalf("点不到确认按钮：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool {
		return !a.detail.lottery.busy && !a.detail.lottery.awaiting
	}, "确认后抽奖没有发起")

	posts, _ := srv.snapshot()
	if len(posts) != 1 || !strings.Contains(posts[0].body, "抽 2 个") {
		t.Errorf("确认后没有按选中人数发触发评论：%+v", posts)
	}
}

// 抽奖的三个网络出口（触发评论、轮询详情、系统通知）都必须走配置的代理。
func TestLotteryEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/replies"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"r1","floor":2}`))
		case strings.Contains(r.URL.Path, "/notifications"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"list":[]}`))
		default:
			_, _ = fmt.Fprint(w, envelopeJSON(`{"short_id":"a1b2","extra":{"lottery":{"finished_at":"x"}}}`))
		}
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy: p, Base: "http://upstream.invalid/api", bearer: func() string { return "tok" },
	}
	if _, err := c.createReply(context.Background(), "a1b2", lotteryCommand(1), replyTarget{}); err != nil {
		t.Fatalf("经代理发触发评论失败：%v", err)
	}
	if topic, err := c.fetchTopic(context.Background(), "a1b2"); err != nil || topic.Lottery == nil {
		t.Fatalf("经代理轮询开奖结果失败：%v %+v", err, topic.Lottery)
	}
	if reason := c.lotteryFailureReason(context.Background(), ""); reason != "" {
		t.Errorf("空通知不该有失败原因：%q", reason)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /api/v1/topics/a1b2/replies",
		"GET /api/v1/topics/a1b2",
		"GET /api/v1/me/notifications",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到 %d 次请求，期望 %d 次：%v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 个请求 = %q，期望 %q", i+1, paths[i], want[i])
		}
	}
}
