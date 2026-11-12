package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// interactJSON 是一份带互动前置字段的帖子详情：收藏状态、节点 uuid 与推荐
// 表情、投票选项 uuid 都要能从它解析出来，否则收藏与表态只能报「缺少信息」。
const interactJSON = `{
  "short_id":"AB12",
  "title":"标题",
  "content":"正文",
  "node":{"id":"node-1","name":"职场","slug":"work",
          "main_emojis":["❤️","👍"],
          "sub_emojis":["👍","🎉","🤔"]},
  "author":{"id":"user-1","username":"jimmy","avatar_url":"https://r2.2libra.com/a.png"},
  "favorited":true,
  "favorited_count":3,
  "created_at":"2026-11-01T09:00:00Z",
  "vote":{"id":"vote-1","title":"投票","options":[
    {"id":"opt-1","text":"甲","vote_count":2},
    {"id":"opt-2","text":"乙"}
  ]}
}`

func TestDecodeInteractionFields(t *testing.T) {
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatalf("解析详情失败：%v", err)
	}
	if !topic.FavoriteKnown || !topic.Favorited {
		t.Errorf("收藏状态没有解析出来：known=%v favorited=%v", topic.FavoriteKnown, topic.Favorited)
	}
	if topic.FavoritedCount != 3 {
		t.Errorf("收藏数 = %d，期望 3", topic.FavoritedCount)
	}
	if topic.NodeID != "node-1" {
		t.Errorf("节点 uuid = %q，期望 node-1", topic.NodeID)
	}
	// main + sub 合并去重，顺序保持 main 优先。
	wantEmojis := []string{"❤️", "👍", "🎉", "🤔"}
	if len(topic.NodeEmojis) != len(wantEmojis) {
		t.Fatalf("推荐表情 = %v，期望 %v", topic.NodeEmojis, wantEmojis)
	}
	for i, want := range wantEmojis {
		if topic.NodeEmojis[i] != want {
			t.Errorf("推荐表情[%d] = %q，期望 %q（%v）", i, topic.NodeEmojis[i], want, topic.NodeEmojis)
		}
	}
	if topic.Vote == nil || topic.Vote.ID != "vote-1" {
		t.Fatalf("投票块 = %+v，期望带 uuid", topic.Vote)
	}
	if len(topic.Vote.Options) != 2 || topic.Vote.Options[0].ID != "opt-1" || topic.Vote.Options[1].ID != "opt-2" {
		t.Errorf("投票选项 uuid 没有解析：%+v", topic.Vote.Options)
	}
}

// 评论表态要评论作者的 uuid，它只能从 author.id 取，不能用昵称代替。
func TestDecodeReplyAuthorID(t *testing.T) {
	r := decodeReply([]byte(`{"id":"r1","content":"写得很好","floor":2,"flat_floor":7,
		"author":{"id":"u2","username":"danny"}}`), 0)
	if r.AuthorID != "u2" {
		t.Errorf("评论作者 uuid = %q，期望 u2", r.AuthorID)
	}
	if r.Floor != 2 || r.FlatFloor != 7 {
		t.Errorf("楼层解析不对：floor=%d flat=%d", r.Floor, r.FlatFloor)
	}
}

// 收藏是按路径切换的方法：收藏发 POST，取消发 DELETE，都要带上令牌。
func TestFavoriteToggleRequests(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	// 从「未收藏」开始，先发 POST 再发 DELETE。
	topic.FavoriteKnown = true
	topic.Favorited = false
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	syncFavorite(a.detail, topic, a.auth.username())
	done := make(chan error, 4)
	a.onInteractDone = func(err error) { done <- err }

	a.toggleFavorite(a.detail)
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("收藏失败：%v", err)
	}
	if !a.detail.fav.favorited || !a.detail.fav.known {
		t.Errorf("收藏后状态不对：%+v", a.detail.fav)
	}
	if !hasToast(a, "已收藏") {
		t.Errorf("收藏成功没有 toast：%+v", a.toasts)
	}

	a.toggleFavorite(a.detail)
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("取消收藏失败：%v", err)
	}
	if a.detail.fav.favorited {
		t.Error("取消收藏后仍显示已收藏")
	}
	if !hasToast(a, "已取消收藏") {
		t.Errorf("取消收藏成功没有 toast：%+v", a.toasts)
	}

	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	want := []string{"POST /v1/topics/AB12/favorite", "DELETE /v1/topics/AB12/favorite"}
	if len(got) != len(want) {
		t.Fatalf("请求 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("请求[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// 未登录点收藏不直接发请求：走全局授权弹窗，登录成功后重跑同一动作。
func TestFavoriteRequiresLogin(t *testing.T) {
	a := testApp(t)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	a.toggleFavorite(a.detail)
	if !a.authGate.open {
		t.Fatal("未登录点收藏应打开授权弹窗")
	}
	if len(a.authGate.replays) != 1 {
		t.Fatalf("待重跑的调用 = %d，期望 1", len(a.authGate.replays))
	}
	if a.detail.fav.busy || a.detail.fav.favorited {
		t.Errorf("未登录不应改动收藏状态：%+v", a.detail.fav)
	}
}

// 帖子表态的请求体字段名与取值必须和文档一致：少一个字段服务端就 400。
func TestTopicReactionRequestBody(t *testing.T) {
	body := runTopicReaction(t)
	want := map[string]string{
		"emoji":     "👍",
		"recUserId": "user-1",
		"path":      "/post/work/AB12",
		"postTitle": "标题",
		"nodeId":    "node-1",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("请求体 %s = %v，期望 %q（%v）", k, body[k], v, body)
		}
	}
	if len(body) != len(want) {
		t.Errorf("请求体多出字段：%v", body)
	}
}

func runTopicReaction(t *testing.T) map[string]any {
	t.Helper()
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/topics/AB12/reaction" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	t.Cleanup(srv.Close)

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.toggleReaction(a.detail, reactionTopic, "👍")
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("表态失败：%v", err)
	}
	select {
	case body := <-got:
		return body
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到表态请求")
		return nil
	}
}

// 评论表态的正文与楼层：floor 缺失时退回 flat_floor；正文按 2000 字截断。
func TestReplyReactionRequestBody(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/replies/r1/reaction" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	content := strings.Repeat("字", 2100)
	a.pageName = "detail"
	a.detail = &detailState{
		shortID: "AB12",
		topic:   &topic,
		replyList: []replyItem{{
			ID: "r1", AuthorID: "u2", Author: "danny",
			Content: content, FlatFloor: 7,
		}},
	}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.toggleReaction(a.detail, "r1", "🎉")
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("评论表态失败：%v", err)
	}

	body := <-got
	if body["recUserId"] != "u2" || body["nodeId"] != "node-1" {
		t.Errorf("评论表态的接收方 / 节点不对：%v", body)
	}
	if body["path"] != "/post/work/AB12" {
		t.Errorf("评论表态路径 = %v", body["path"])
	}
	comment, _ := body["comment"].(string)
	if n := len([]rune(comment)); n != 2000 {
		t.Errorf("评论正文截断后 %d 字，期望 2000", n)
	}
	if body["locatedFloor"] != float64(7) {
		t.Errorf("locatedFloor = %v，期望 7（floor 缺失时退回 flat_floor）", body["locatedFloor"])
	}
	if body["flatFloor"] != float64(7) {
		t.Errorf("flatFloor = %v，期望 7", body["flatFloor"])
	}
	// 表态是 toggle：本地先乐观加上，服务端成功后就保留。
	if !reactionMine(a.detail.replyList[0].Reactions, "🎉") {
		t.Errorf("评论表态没有落到本地状态：%+v", a.detail.replyList[0].Reactions)
	}
}

// 表态失败要回滚乐观更新：不能让界面停在一个服务端并没有接受的状态上。
func TestReactionRollsBackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"c":500,"code":"internal_error","m":"服务端错误","d":null,"t":1}`)
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	topic.Reactions = []reactionStat{{Emoji: "👍", Count: 2}}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.toggleReaction(a.detail, reactionTopic, "👍")
	if err := waitInteract(t, done); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
	for _, s := range a.detail.topic.Reactions {
		if s.Emoji == "👍" {
			if s.Mine || s.Count != 2 {
				t.Errorf("失败后没有回滚：%+v", s)
			}
			return
		}
	}
	t.Errorf("失败后 👍 从表态里消失了：%+v", a.detail.topic.Reactions)
}

// 详情页页头的收藏按钮要能点：点一下发出收藏请求，状态切到已收藏。
func TestDetailFavoriteButton(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.FavoriteKnown = false
	topic.Favorited = false
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }

	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("收藏"); !ok {
		t.Fatalf("页头没有收藏按钮：%q", tt.Texts())
	}
	if err := tt.Click("收藏"); err != nil {
		t.Fatalf("点击收藏失败：%v", err)
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("收藏失败：%v", err)
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "POST /v1/topics/AB12/favorite" {
		t.Errorf("请求 = %v", got)
	}
	// 状态变了之后按钮的可访问名也变成「取消收藏」。
	tt.Frame()
	if _, ok := tt.Find("取消收藏"); !ok {
		t.Errorf("收藏后按钮没有变成「取消收藏」：%q", tt.Texts())
	}
}

// 表情摘要默认是「叠放的表情 + 总次数」；悬停说明把每种表情的次数依次拼接，
// 点开明细还能逐项看。
func TestReactionSummaryMergesCounts(t *testing.T) {
	a := testApp(t)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	topic.Reactions = []reactionStat{
		{Emoji: "👍", Count: 1},
		{Emoji: "❤️", Count: 1},
		{Emoji: "🎉", Count: 1},
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}

	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("表情，共 3 次"); !ok {
		t.Fatalf("摘要没有显示「表情 + 总数」：%q", tt.Texts())
	}
	first, ok := tt.Find("👍")
	if !ok {
		t.Fatal("摘要里没有表情")
	}
	second, ok := tt.Find("❤️")
	if !ok {
		t.Fatal("摘要里缺少第二个表情")
	}
	// 叠放：相邻表情的间距小于一个圆片的宽度，说明后一个压住了前一个。
	pitch := second.X - first.X
	if pitch <= 0 || pitch >= reactionStackSize {
		t.Errorf("表情没有叠放：间距 %v（圆片宽 %d）", pitch, reactionStackSize)
	}
	if _, ok := tt.Find("🎉"); !ok {
		t.Error("叠放的表情应包含第三个")
	}
	// 悬停说明拼接每种表情的次数。
	got := reactionSummaryTip([]reactionStat{
		{Emoji: "👍", Count: 1}, {Emoji: "❤️", Count: 1}, {Emoji: "🎉", Count: 1},
	})
	if want := "👍 1 ❤️ 1 🎉 1"; got != want {
		t.Errorf("悬停说明 = %q，期望 %q", got, want)
	}
	if err := tt.Click("表情，共 3 次"); err != nil {
		t.Fatalf("点开表情明细失败：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"表情明细", "👍 1", "❤️ 1", "🎉 1"} {
		if !tt.HasText(want) {
			t.Errorf("表情明细缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 右侧是两个图标入口：表态（笑脸）与打赏（金币）。点开的气泡贴着入口，
// 不是居中的弹窗；打开本身不发请求。
func TestReactionPopover(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.pageName = "detail"
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.detail = &detailState{shortID: "AB12", topic: &topic}

	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("表情"); !ok {
		t.Fatalf("右侧没有表情图标入口：%q", tt.Texts())
	}
	if _, ok := tt.Find("打赏"); !ok {
		t.Fatalf("右侧没有打赏图标入口：%q", tt.Texts())
	}
	if err := tt.Click("表情"); err != nil {
		t.Fatalf("点表情入口失败：%v", err)
	}
	tt.Frame()
	if !a.detail.react.reactOpen || a.detail.react.reactFor != reactionTopic {
		t.Errorf("表态气泡状态不对：%+v", a.detail.react)
	}
	for _, want := range []string{"选择一个表情", "选择表情 👍"} {
		if !tt.HasText(want) {
			t.Errorf("表态气泡缺少 %q：%q", want, tt.Texts())
		}
	}
	// 点打赏入口：表态气泡收起，打赏气泡打开。
	if err := tt.Click("打赏"); err != nil {
		t.Fatalf("点打赏入口失败：%v", err)
	}
	tt.Frame()
	if a.detail.react.reactOpen {
		t.Error("打开打赏气泡时表态气泡应收起")
	}
	if !a.detail.react.rewardOpen || a.detail.react.rewardFor != reactionTopic {
		t.Errorf("打赏气泡状态不对：%+v", a.detail.react)
	}
	for _, want := range []string{"打赏金币", "打赏 5 金币"} {
		if !tt.HasText(want) {
			t.Errorf("打赏气泡缺少 %q：%q", want, tt.Texts())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("打开气泡不该发请求，实际 %d 次", n)
	}
}

// 弹窗里点表情就是一次表态。
func TestReactionActionTogglesEmoji(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.openReactionPopup(a.detail, reactionTopic)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("选择表情 👍"); err != nil {
		t.Fatalf("气泡里点表情失败：%v（%q）", err, tt.Texts())
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("表态失败：%v", err)
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "POST /v1/topics/AB12/reaction" {
		t.Errorf("请求 = %v", got)
	}
	if !reactionMine(a.detail.topic.Reactions, "👍") {
		t.Errorf("表态没有落到帖子状态：%+v", a.detail.topic.Reactions)
	}
	if a.detail.react.reactOpen {
		t.Error("表态后气泡应关闭")
	}
}

// 打赏：请求体、必填幂等键、成功后的 toast 与本地记账。
func TestRewardSubmitsFromDialog(t *testing.T) {
	got := make(chan map[string]any, 1)
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rewards" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		gotKey = r.Header.Get("Idempotency-Key")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.openRewardPopup(a.detail, reactionTopic)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("打赏 5 金币"); err != nil {
		t.Fatalf("点打赏失败：%v（%q）", err, tt.Texts())
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("打赏失败：%v", err)
	}
	body := <-got
	if body["type"] != "post" || body["post_id"] != "AB12" {
		t.Errorf("打赏请求体不对：%v", body)
	}
	if body["amount"] != float64(5) {
		t.Errorf("打赏金额 = %v，期望 5", body["amount"])
	}
	if !keyPattern.MatchString(gotKey) {
		t.Errorf("打赏必须带幂等键，实际 %q", gotKey)
	}
	if !hasToast(a, "已打赏 5 金币") {
		// toast 可能已经被点击后的那一帧交给窗口领走了：再看界面。
		tt.Frame()
		if !tt.HasText("已打赏 5 金币") {
			t.Errorf("打赏成功没有 toast：%+v %q", a.toasts, tt.Texts())
		}
	}
	if a.detail.topic.Rewards.Total != 5 || a.detail.topic.Rewards.Count != 1 {
		t.Errorf("本地没有记账：%+v", a.detail.topic.Rewards)
	}
	if a.detail.react.rewardOpen {
		t.Error("打赏成功后气泡应关闭")
	}
}

// 评论打赏用 type=comment 与 comment_id。
func TestRewardCommentTarget(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{
		shortID: "AB12",
		topic:   &topic,
		replyList: []replyItem{
			{ID: "r1", AuthorID: "u2", Author: "danny", Content: "评论"},
		},
	}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }
	a.openRewardPopup(a.detail, "r1")

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("打赏 5 金币"); err != nil {
		t.Fatalf("点打赏失败：%v", err)
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("打赏失败：%v", err)
	}
	body := <-got
	if body["type"] != "comment" || body["comment_id"] != "r1" {
		t.Errorf("评论打赏请求体不对：%v", body)
	}
	if a.detail.replyList[0].Rewards.Total != 5 {
		t.Errorf("评论打赏没有记账：%+v", a.detail.replyList[0].Rewards)
	}
}

// 同一笔打赏失败重试复用同一个幂等键；金额变了才换新键。
func TestRewardIdempotencyKeyReuse(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"c":500,"code":"internal_error","m":"服务端错误","d":null,"t":1}`)
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	done := make(chan error, 3)
	a.onInteractDone = func(err error) { done <- err }

	a.openRewardPopup(a.detail, reactionTopic)
	a.submitReward(a.detail, reactionTopic)
	if err := waitInteract(t, done); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
	// 同金额重试：还是这一笔，幂等键不能换。
	a.submitReward(a.detail, reactionTopic)
	if err := waitInteract(t, done); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
	// 改金额就是另一笔打赏：换新键。
	a.detail.react.amount = 8
	a.submitReward(a.detail, reactionTopic)
	if err := waitInteract(t, done); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
	mu.Lock()
	got := append([]string(nil), keys...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("打赏请求次数 = %d，期望 3：%v", len(got), got)
	}
	if got[0] == "" || got[0] != got[1] {
		t.Errorf("同一笔打赏重试应复用幂等键：%v", got)
	}
	if got[2] == got[0] || got[2] == "" {
		t.Errorf("金额变化后应换新幂等键：%v", got)
	}
}

// 未登录点表态图标：直接走全局授权弹窗，不打开气泡。
func TestReactionEntryRequiresLogin(t *testing.T) {
	a := testApp(t)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("表情"); err != nil {
		t.Fatalf("点表情入口失败：%v", err)
	}
	if !a.authGate.open {
		t.Fatal("未登录点表态应打开全局授权弹窗")
	}
	if a.detail.react.reactOpen {
		t.Error("未登录不该打开表态气泡")
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("授权后应重跑这次表态入口，待重跑 %d 个", len(a.authGate.replays))
	}
}

// 匿名帖没有作者 uuid：表态通知与打赏都没有接收方，所以只展示已有表态，
// 不提供任何操作入口。
func TestAnonymousTopicHasNoReactionEntry(t *testing.T) {
	a := testApp(t)
	topic := topicDetail{
		ShortID: "ANON1", Title: "匿名帖", NodeID: "node-1",
		URL:        "https://2libra.com/post/work/ANON1",
		NodeEmojis: []string{"👍"},
		Reactions:  []reactionStat{{Emoji: "👍", Count: 1}},
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "ANON1", topic: &topic}
	tt := ui.NewTester(a.view, 1100, 760)
	for _, entry := range []string{"表情", "打赏"} {
		if _, ok := tt.Find(entry); ok {
			t.Errorf("匿名帖不该有「%s」入口", entry)
		}
	}
	if _, ok := tt.Find("表情，共 1 次"); !ok {
		t.Errorf("已有的表情仍应展示：%q", tt.Texts())
	}
	if canReactTopic(&topic) {
		t.Error("没有作者 uuid 的帖子不应允许表态")
	}
}

/* ------------------------------------------------------------------
   小工具
-------------------------------------------------------------- */

func waitInteract(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("等待互动请求完成超时")
		return nil
	}
}

func hasToast(a *app, text string) bool {
	for _, n := range a.toasts {
		if n.text == text {
			return true
		}
	}
	return false
}
