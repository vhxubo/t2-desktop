package main

import (
	"encoding/json"
	"fmt"
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
	if !topic.FavoriteCountKnown {
		t.Error("响应里有 favorited_count，收藏数应标记为已知")
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

// 未登录点收藏不直接发请求：走全局授权弹窗，由用户自己再点一次收藏。
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

// 详情页的收藏按钮在标题下方作者行的最右侧：点一下发出收藏请求，状态切到
// 已收藏，按钮文案与可访问名一起更新。
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
	favorite, ok := tt.Find("收藏")
	if !ok {
		t.Fatalf("作者行没有收藏按钮：%q", tt.Texts())
	}
	author, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("找不到作者行：%q", tt.Texts())
	}
	if favorite.X <= author.X {
		t.Errorf("收藏按钮应在作者信息右侧：收藏 %+v / 作者 %+v", favorite, author)
	}
	if dy := favorite.Y - author.Y; dy > 20 || dy < -20 {
		t.Errorf("收藏按钮应与作者信息在同一行：收藏 %+v / 作者 %+v", favorite, author)
	}
	// 详情响应里的 favorited_count 跟着收藏按钮展示；数字在「收藏」右侧、
	// 同一行，不能变成按钮外的一行统计。
	count, ok := tt.Find("3")
	if !ok {
		t.Fatalf("收藏按钮没有显示收藏数：%q", tt.Texts())
	}
	if count.X <= favorite.X {
		t.Errorf("收藏数应在收藏按钮文案右侧：收藏数 %+v / 收藏 %+v", count, favorite)
	}
	if dy := count.Y - favorite.Y; dy > 10 || dy < -10 {
		t.Errorf("收藏数应与收藏按钮同一行：收藏数 %+v / 收藏 %+v", count, favorite)
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
	// 状态变了之后按钮的可访问名也变成「取消收藏」，可见文案变成「已收藏」。
	tt.Frame()
	if _, ok := tt.Find("取消收藏"); !ok {
		t.Errorf("收藏后按钮没有变成「取消收藏」：%q", tt.Texts())
	}
	if !tt.HasText("已收藏") {
		t.Errorf("收藏后按钮没有显示「已收藏」：%q", tt.Texts())
	}
	// 乐观更新：收藏成功，收藏数从 3 变成 4；取消收藏后回到 3。
	if _, ok := tt.Find("4"); !ok {
		t.Errorf("收藏后收藏数没有加一：%q", tt.Texts())
	}
	if err := tt.Click("取消收藏"); err != nil {
		t.Fatalf("点击取消收藏失败：%v", err)
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("取消收藏失败：%v", err)
	}
	tt.Frame()
	mu.Lock()
	got = append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 2 || got[1] != "DELETE /v1/topics/AB12/favorite" {
		t.Errorf("请求 = %v", got)
	}
	if _, ok := tt.Find("3"); !ok {
		t.Errorf("取消收藏后收藏数没有减回去：%q", tt.Texts())
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

// 右侧是两个图标入口：表态（笑脸）与打赏（金币）。两个点开的都是贴着入口的
// 气泡；打开本身不发请求。
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
	if !tt.HasText("选择表情 👍") {
		t.Errorf("表态气泡缺少表情按钮：%q", tt.Texts())
	}
	// 表态气泡贴着笑脸入口，关掉它再点底下的打赏入口。
	a.detail.react.reactOpen = false
	tt.Frame()
	tt.Advance(500 * time.Millisecond)
	// 点打赏入口：表情工具栏收起，打赏气泡打开。
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
	for _, want := range []string{"打赏金币", "打赏 100 金币"} {
		if !tt.HasText(want) {
			t.Errorf("打赏气泡缺少 %q：%q", want, tt.Texts())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("打开气泡不该发请求，实际 %d 次", n)
	}
}

// 打赏与表态都收成贴着入口的 Lobe Popover：面板横向靠近图标、宽度不超过
// 一个小面板，不再像整页 Modal 那样把视线拉到窗口中央。
func TestInteractPopoversCompactAndAnchored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	for _, tc := range []struct{ name, entry, content string }{
		{"表态", "表情", "选择表情 👍"},
		{"打赏", "打赏", "打赏金币"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := loginApp(t, srv)
			a.pageName = "detail"
			topic, err := decodeTopic([]byte(interactJSON), "AB12")
			if err != nil {
				t.Fatal(err)
			}
			topic.URL = "https://2libra.com/post/work/AB12"
			a.detail = &detailState{shortID: "AB12", topic: &topic}

			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			entry, ok := tt.Find(tc.entry)
			if !ok {
				t.Fatalf("找不到 %s 入口：%q", tc.name, tt.Texts())
			}
			if err := tt.Click(tc.entry); err != nil {
				t.Fatalf("点开%s气泡失败：%v", tc.name, err)
			}
			tt.Frame()
			panel, ok := tt.Find(tc.content)
			if !ok {
				t.Fatalf("没有弹出%s面板：%q", tc.name, tt.Texts())
			}
			width := panel.W
			if tc.name == "表态" {
				// 表情按钮只是面板里的一格，面板实际宽度按表情数算。
				width = reactionPanelWidth(len(reactionEmojisOf(a.detail, reactionTopic)))
			}
			if width > 260 {
				t.Errorf("%s面板应小巧（<=260），实际宽 %.0f", tc.name, width)
			}
			if dx := panel.X - entry.X; dx < -180 || dx > 180 {
				t.Errorf("%s面板应贴着入口：入口 X %.0f，面板 X %.0f", tc.name, entry.X, panel.X)
			}
		})
	}
}

// 正文与评论的表态气泡宽度跟着展示的表情走：面板右边缘要贴住最右边那个
// 表情按钮，不能在右侧留下一条空白。表情多到换行时也按每行 4 个算准宽度。
func TestReactionPanelFitsEmojiGrid(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		target  string
		emojis  []string
		comment bool
	}{
		{"正文", reactionTopic, []string{"❤️", "👍", "🎉", "🤔"}, false},
		{"评论", "c1", []string{"❤️", "👍", "🎉", "🤔"}, true},
		{"满行", reactionTopic, []string{"😀", "😂", "🤔", "🎉", "❤️", "👍", "🙏", "💯"}, false},
		{"少量", reactionTopic, []string{"👍", "❤️"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := loginApp(t, srv)
			a.pageName = "detail"
			topic, err := decodeTopic([]byte(interactJSON), "AB12")
			if err != nil {
				t.Fatal(err)
			}
			topic.URL = "https://2libra.com/post/work/AB12"
			topic.NodeEmojis = tc.emojis
			a.detail = &detailState{shortID: "AB12", topic: &topic}
			if tc.comment {
				pg := decodeReplies([]byte(detailRepliesJSON))
				a.detail.replyList = flattenReplies(pg)
				a.detail.replyLoaded = true
			}
			a.openReactionPopup(a.detail, tc.target)

			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			emojis := reactionEmojisOf(a.detail, tc.target)
			if len(emojis) == 0 {
				t.Fatal("测试节点没有推荐表情")
			}
			// 第一个表情按钮贴着面板左边缘，据此推回面板右边缘。
			first, ok := tt.Find("选择表情 " + emojis[0])
			if !ok {
				t.Fatalf("没有弹出表态面板：%q", tt.Texts())
			}
			panelRight := first.X + reactionPanelWidth(len(emojis))
			rightmost := float32(0)
			for _, emoji := range emojis {
				btn, ok := tt.Find("选择表情 " + emoji)
				if !ok {
					t.Fatalf("面板里缺少表情 %s：%q", emoji, tt.Texts())
				}
				if edge := btn.X + btn.W; edge > rightmost {
					rightmost = edge
				}
			}
			if gap := panelRight - rightmost; gap > 1 || gap < 0 {
				t.Errorf("面板右侧留白 %.1f DIP：面板右边缘 %.1f，最右表情右边缘 %.1f",
					gap, panelRight, rightmost)
			}
		})
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

// 打赏金额始终是 100～500 的整数，低于下限的输入按 100 提交。
func TestRewardAmountRange(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int
	}{
		{-10, 100}, {1, 100}, {99, 100}, {99.6, 100},
		{100, 100}, {150.4, 150}, {500, 500}, {600, 500},
	} {
		if got := rewardAmount(tc.in); got != tc.want {
			t.Errorf("rewardAmount(%v) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

// 打赏金额框是 Lobe 的数字输入：手输回车后生效，低于下限按 100 记。
func TestRewardAmountInputCommitsAndClamps(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	a.openRewardPopup(a.detail, reactionTopic)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Access("打赏金额"); !ok {
		t.Fatalf("打赏气泡缺少金额输入框：%q", tt.Texts())
	}
	for _, tc := range []struct {
		typed string
		want  int
	}{
		{"250", 250},
		{"50", 100},
	} {
		if err := tt.Click("打赏金额"); err != nil {
			t.Fatalf("点金额输入框失败：%v", err)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(tc.typed)
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		if got := rewardAmount(a.detail.react.amount); got != tc.want {
			t.Errorf("输入 %s 后金额 = %d，期望 %d", tc.typed, got, tc.want)
		}
		if !tt.HasText(fmt.Sprintf("打赏 %d 金币", tc.want)) {
			t.Errorf("确认按钮没跟着输入变：%q", tt.Texts())
		}
	}
	// 上下键按 50 步进。
	if err := tt.Click("打赏金额"); err != nil {
		t.Fatalf("点金额输入框失败：%v", err)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if got := rewardAmount(a.detail.react.amount); got != 150 {
		t.Errorf("按上键后金额 = %d，期望 150", got)
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
	if err := tt.Click("打赏 100 金币"); err != nil {
		t.Fatalf("点打赏失败：%v（%q）", err, tt.Texts())
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("打赏失败：%v", err)
	}
	body := <-got
	if body["type"] != "post" || body["post_id"] != "AB12" {
		t.Errorf("打赏请求体不对：%v", body)
	}
	if body["amount"] != float64(100) {
		t.Errorf("打赏金额 = %v，期望 100", body["amount"])
	}
	if !keyPattern.MatchString(gotKey) {
		t.Errorf("打赏必须带幂等键，实际 %q", gotKey)
	}
	if !hasToast(a, "已打赏 100 金币") {
		// toast 可能已经被点击后的那一帧交给窗口领走了：再看界面。
		tt.Frame()
		if !tt.HasText("已打赏 100 金币") {
			t.Errorf("打赏成功没有 toast：%+v %q", a.toasts, tt.Texts())
		}
	}
	if a.detail.topic.Rewards.Total != 100 || a.detail.topic.Rewards.Count != 1 {
		t.Errorf("本地没有记账：%+v", a.detail.topic.Rewards)
	}
	if a.detail.react.rewardOpen {
		t.Error("打赏成功后气泡应关闭")
	}
}

// 打赏气泡里给一排常用金额：点芯片只改金额、只改确认按钮上的数字，绝不能因此
// 直接打赏——扣币必须由用户再点一次确认按钮，期间气泡也不能自己关掉。
func TestRewardPresetAmounts(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
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
	a.openRewardPopup(a.detail, reactionTopic)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("常用金额") {
		t.Errorf("打赏气泡没有常用金额：%q", tt.Texts())
	}
	for _, amount := range []int{100, 150, 200, 300, 350, 400, 500} {
		name := fmt.Sprintf("常用金额 %d 金币", amount)
		info, ok := tt.Access(name)
		if !ok {
			t.Fatalf("缺少常用金额 %d 金币的按钮：%q", amount, tt.Texts())
		}
		if info.Description == "" {
			t.Errorf("常用金额 %d 金币 没有 tooltip", amount)
		}
	}

	// 连点两个常用金额：金额跟着变，但一次请求都不发，气泡也不关。
	for _, amount := range []int{200, 300} {
		if err := tt.Click(fmt.Sprintf("常用金额 %d 金币", amount)); err != nil {
			t.Fatalf("点常用金额 %d 失败：%v", amount, err)
		}
		tt.Frame()
		if got := rewardAmount(a.detail.react.amount); got != amount {
			t.Errorf("点常用金额后金额 = %d，期望 %d", got, amount)
		}
		if !tt.HasText(fmt.Sprintf("打赏 %d 金币", amount)) {
			t.Errorf("确认按钮没跟着金额变：%q", tt.Texts())
		}
		if !a.detail.react.rewardOpen {
			t.Fatalf("点常用金额 %d 后气泡不该关掉", amount)
		}
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n != 0 {
			t.Fatalf("点常用金额 %d 不该发请求，实际 %d 次", amount, n)
		}
	}
	if a.detail.topic.Rewards.Total != 0 {
		t.Errorf("点常用金额不该记账：%+v", a.detail.topic.Rewards)
	}

	// 只有再点一次确认按钮才真的打赏，金额就是刚选的那个。
	if err := tt.Click("打赏 300 金币"); err != nil {
		t.Fatalf("点确认打赏失败：%v", err)
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("打赏失败：%v", err)
	}
	mu.Lock()
	got := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("打赏请求次数 = %d，期望 1：%v", len(got), got)
	}
	if got[0]["amount"] != float64(300) {
		t.Errorf("打赏金额 = %v，期望 300", got[0]["amount"])
	}
	if a.detail.topic.Rewards.Total != 300 {
		t.Errorf("打赏没有记账：%+v", a.detail.topic.Rewards)
	}
}

// 打赏气泡每次打开都从默认金额 100 开始，不沿用上次输入或选过的数额。
func TestRewardPopupResetsAmountOnOpen(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := loginApp(t, srv)
	topic, err := decodeTopic([]byte(interactJSON), "AB12")
	if err != nil {
		t.Fatal(err)
	}
	topic.URL = "https://2libra.com/post/work/AB12"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}
	a.openRewardPopup(a.detail, reactionTopic)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("常用金额 300 金币"); err != nil {
		t.Fatalf("点常用金额失败：%v", err)
	}
	tt.Frame()
	if got := rewardAmount(a.detail.react.amount); got != 300 {
		t.Fatalf("点常用金额后金额 = %d，期望 300", got)
	}

	// 关掉再打开：金额与确认按钮都要回到 100。
	a.detail.react.rewardOpen = false
	tt.Frame()
	a.openRewardPopup(a.detail, reactionTopic)
	tt.Frame()
	if got := rewardAmount(a.detail.react.amount); got != 100 {
		t.Errorf("重新打开打赏气泡金额 = %d，期望 100", got)
	}
	if !tt.HasText("打赏 100 金币") {
		t.Errorf("确认按钮没有回到默认金额：%q", tt.Texts())
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
	if err := tt.Click("打赏 100 金币"); err != nil {
		t.Fatalf("点打赏失败：%v", err)
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("打赏失败：%v", err)
	}
	body := <-got
	if body["type"] != "comment" || body["comment_id"] != "r1" {
		t.Errorf("评论打赏请求体不对：%v", body)
	}
	if a.detail.replyList[0].Rewards.Total != 100 {
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
	a.detail.react.amount = 150
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
}

// 自己写的评论不显示表态与打赏入口：这两个动作都会通知评论作者，发给
// 自己既没有意义也会打扰自己。回复入口仍保留。
func TestOwnCommentHidesReactionAndReward(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := loginApp(t, srv) // 登录用户名是 reader

	// 帖子本身没有作者 uuid，互动条不出现；这里只验证评论底行。
	topic := topicDetail{
		ShortID: "AB12", Title: "标题", NodeID: "node-1",
		URL: "https://2libra.com/post/work/AB12", NodeEmojis: []string{"👍"},
	}
	a.pageName = "detail"
	a.detail = &detailState{
		shortID: "AB12",
		topic:   &topic,
		replyList: []replyItem{{
			ID: "r1", AuthorID: "u2", Author: "danny", Content: "别人的评论", Floor: 1,
		}},
	}

	tt := ui.NewTester(a.view, 1100, 760)
	for _, entry := range []string{"表情", "打赏"} {
		if _, ok := tt.Find(entry); !ok {
			t.Fatalf("别人的评论应有「%s」入口：%q", entry, tt.Texts())
		}
	}

	// 同一条评论换成当前登录用户写的：两个入口都消失，回复还在。
	a.detail.replyList[0].Author = "reader"
	tt.Frame()
	for _, entry := range []string{"表情", "打赏"} {
		if _, ok := tt.Find(entry); ok {
			t.Errorf("自己的评论不该有「%s」入口", entry)
		}
	}
	if _, ok := tt.Find("回复"); !ok {
		t.Errorf("自己的评论仍应保留回复入口：%q", tt.Texts())
	}
	if a.canActOn(a.detail, "r1") || a.canRewardOn(a.detail, "r1") {
		t.Error("自己的评论不应允许表态或打赏")
	}
}

// 自己的帖子同样不显示表态与打赏入口：已有的汇总照常展示，但不能给自己
// 发通知，也不该把金币打赏给自己。
func TestOwnTopicHidesReactionAndReward(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := loginApp(t, srv) // 登录用户名是 reader

	topic := topicDetail{
		ShortID: "AB12", Title: "标题", NodeID: "node-1",
		Author: "reader", AuthorID: "user-1",
		URL: "https://2libra.com/post/work/AB12", NodeEmojis: []string{"👍"},
		Reactions: []reactionStat{{Emoji: "👍", Count: 2}},
		Rewards:   rewardStat{Total: 10, Count: 1},
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "AB12", topic: &topic}

	tt := ui.NewTester(a.view, 1100, 760)
	for _, entry := range []string{"表情", "打赏"} {
		if _, ok := tt.Find(entry); ok {
			t.Errorf("自己的帖子不该有「%s」入口：%q", entry, tt.Texts())
		}
	}
	if _, ok := tt.Find("表情，共 2 次"); !ok {
		t.Errorf("已有的表态汇总仍应展示：%q", tt.Texts())
	}
	if a.canActOn(a.detail, reactionTopic) || a.canRewardOn(a.detail, reactionTopic) {
		t.Error("自己的帖子不应允许表态或打赏")
	}
}

// 即使有人绕过入口直接调用动作函数，自己的帖子与评论也不会发出请求。
func TestOwnContentRejectsReactionAndRewardRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv) // 登录用户名是 reader

	topic := topicDetail{
		ShortID: "AB12", Title: "标题", NodeID: "node-1",
		Author: "reader", AuthorID: "user-1",
		URL: "https://2libra.com/post/work/AB12", NodeEmojis: []string{"👍"},
	}
	a.pageName = "detail"
	a.detail = &detailState{
		shortID: "AB12",
		topic:   &topic,
		replyList: []replyItem{{
			ID: "r1", AuthorID: "u2", Author: "reader", Content: "自己的评论", Floor: 1,
		}},
	}
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }

	a.toggleReaction(a.detail, reactionTopic, "👍")
	a.toggleReaction(a.detail, "r1", "👍")
	a.submitReward(a.detail, reactionTopic)
	a.submitReward(a.detail, "r1")

	select {
	case err := <-done:
		t.Fatalf("自己的内容不该发出互动请求，却收到回调：%v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("自己的内容发出了 %d 个表态 / 打赏请求", n)
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
