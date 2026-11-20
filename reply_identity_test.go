package main

import (
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

// replyIdentityApp 造一个挂了 jimmy + bystander 两个账号的详情页窗口。
func replyIdentityApp(t *testing.T) (*app, *account) {
	t.Helper()
	a := openTestDetail(multiAccountWindow(t, "jimmy"))
	other := a.book.accountByUsername("bystander")
	if other == nil {
		t.Fatal("账号册里找不到 bystander")
	}
	return a, other
}

// 详情页底部的身份标签可以点开换成另一个账号；换完再点一下能换回来。
// 临时身份只写在这份 detailState 上，不落盘、也不改变窗口的登录身份。
func TestReplyIdentitySwitchAndRevert(t *testing.T) {
	a, other := replyIdentityApp(t)
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	if !tt.HasText("当前账号 @jimmy") {
		t.Fatalf("详情页没有写明当前账号：%q", tt.Texts())
	}
	if err := tt.Click("当前账号 jimmy"); err != nil {
		t.Fatalf("点不到身份标签：%v", err)
	}
	tt.Frame()
	if !tt.HasText("用哪个账号回复") {
		t.Fatalf("身份标签没有打开账号列表：%q", tt.Texts())
	}
	if !tt.HasText("@bystander") {
		t.Errorf("账号列表里没有另一个账号：%q", tt.Texts())
	}
	if err := tt.Click("以 bystander 的身份回复"); err != nil {
		t.Fatalf("点不到另一个账号：%v", err)
	}
	if a.detail.replyAsID != other.ID {
		t.Errorf("replyAsID = %q，期望 %q", a.detail.replyAsID, other.ID)
	}
	if a.detail.identityOpen {
		t.Error("选完账号后气泡应当收起")
	}
	tt.Frame()
	if !tt.HasText("回复身份 @bystander") {
		t.Errorf("标签没有换成回复身份：%q", tt.Texts())
	}
	if !tt.HasText("已切换为以 @bystander 的身份回复") {
		t.Errorf("切换后没有提示：%q", tt.Texts())
	}
	// 窗口自己的登录身份不能跟着变。
	if a.auth.username() != "jimmy" {
		t.Errorf("窗口账号被改成了 %q", a.auth.username())
	}
	// 临时身份要能明确地换回自己：再点标签，选当前账号。
	if err := tt.Click("回复身份 bystander"); err != nil {
		t.Fatalf("点不到已切换的身份标签：%v", err)
	}
	tt.Frame()
	if err := tt.Click("切回当前账号 jimmy"); err != nil {
		t.Fatalf("点不到切回当前账号：%v", err)
	}
	if a.detail.replyAsID != "" {
		t.Errorf("切回后 replyAsID = %q，期望空", a.detail.replyAsID)
	}
	tt.Frame()
	if !tt.HasText("当前账号 @jimmy") {
		t.Errorf("切回后标签没有回到当前账号：%q", tt.Texts())
	}
}

// 独立写评论窗口里也能换账号：气泡只在点开它的那扇窗口里画。
func TestReplyIdentitySwitchFromReplyWindow(t *testing.T) {
	a, other := replyIdentityApp(t)
	a.openReplyWindow()
	wt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	wt.Frame()
	if !wt.HasText("当前账号 @jimmy") {
		t.Fatalf("写评论窗口没有写明当前账号：%q", wt.Texts())
	}
	if err := wt.Click("当前账号 jimmy"); err != nil {
		t.Fatalf("点不到写评论窗口里的身份标签：%v", err)
	}
	wt.Frame()
	if !wt.HasText("用哪个账号回复") {
		t.Fatalf("写评论窗口没有打开账号列表：%q", wt.Texts())
	}
	if err := wt.Click("以 bystander 的身份回复"); err != nil {
		t.Fatalf("点不到另一个账号：%v", err)
	}
	if a.detail.replyAsID != other.ID {
		t.Errorf("replyAsID = %q，期望 %q", a.detail.replyAsID, other.ID)
	}
	wt.Frame()
	if !wt.HasText("回复身份 @bystander") {
		t.Errorf("写评论窗口的标签没有换成回复身份：%q", wt.Texts())
	}
	// 气泡已经收起，而且它属于写评论窗口，主窗口不该跟着画一份。
	mt := ui.NewTester(a.view, 1100, 900)
	mt.Frame()
	if mt.HasText("用哪个账号回复") {
		t.Errorf("主窗口不该画写评论窗口的气泡：%q", mt.Texts())
	}
}

// 换了账号后提交，请求必须带那个账号自己的令牌；响应没带作者时也按那个
// 账号补，结果不能记到本窗口名下。
func TestReplyIdentitySendsWithOtherAccount(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("Idempotency-Key")
		gotBody = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"new1","floor":99}`))
	}))
	defer srv.Close()

	a, other := replyIdentityApp(t)
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"
	a.detail.replyAsID = other.ID
	a.detail.draft = "换个身份说一句"
	done := make(chan struct{})
	a.onReplyPosted = func(replyItem, error) { close(done) }

	a.submitReply()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("发送回调超时")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/api/v1/topics/JBYGf2A/replies" {
		t.Errorf("发评论路径不对：%s", gotPath)
	}
	if gotAuth != "Bearer tok-bystander" {
		t.Errorf("没有用切换后的账号令牌：%q", gotAuth)
	}
	if gotKey == "" {
		t.Error("缺少 Idempotency-Key")
	}
	if !strings.Contains(gotBody, `"content":"换个身份说一句"`) {
		t.Errorf("请求体不对：%s", gotBody)
	}
	if a.detail.replyAsID != other.ID {
		t.Errorf("发送后临时身份应保留到离开详情页：%q", a.detail.replyAsID)
	}
	if a.detail.draft != "" {
		t.Errorf("发送后草稿未清空：%q", a.detail.draft)
	}
	last := a.detail.replyList[len(a.detail.replyList)-1]
	if last.ID != "new1" || last.Author != "bystander" {
		t.Errorf("新评论的作者没有按切换后的账号补齐：%+v", last)
	}
}

// 切过去的账号登录失效时拦在本地，草稿保留，并明确告诉用户换回或换一个。
func TestReplyIdentityBlocksWhenSessionExpired(t *testing.T) {
	a, other := replyIdentityApp(t)
	setTestNeedLogin(a.book, other.ID)
	a.detail.replyAsID = other.ID
	a.detail.draft = "还想发"
	a.submitReply()

	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	if !tt.HasText("「bystander」的登录已失效") {
		t.Errorf("没有说明回复账号失效：%q", tt.Texts())
	}
	if _, ok := tt.Find("回复身份已失效"); !ok {
		t.Errorf("身份标签没有标出失效：%q", tt.Texts())
	}
	if a.detail.draft != "还想发" {
		t.Errorf("被拦下时不该动草稿：%q", a.detail.draft)
	}
}

// 临时回复身份不跨页面：跳到别的帖子再关回来，都要回到窗口自己的账号，
// 也不落盘到窗口状态里。
func TestReplyIdentityExpiresWhenLeavingDetail(t *testing.T) {
	a, other := replyIdentityApp(t)
	a.detail.replyAsID = other.ID
	a.openDetail(Item{ShortID: "OTHER2", URL: "https://2libra.com/post/work/OTHER2"})
	if a.detail == nil || a.detail.replyAsID != "" {
		t.Fatalf("跳到新帖子后不该继承临时身份：%+v", a.detail)
	}
	if len(a.detailStack) != 1 || a.detailStack[0].replyAsID != "" {
		t.Errorf("返回栈里的上一篇也应清掉临时身份：%+v", a.detailStack)
	}
	// 这一篇再切一次，关闭后回到上一篇：两边的身份都必须是自己的账号。
	a.detail.replyAsID = other.ID
	a.closeDetail()
	if a.detail == nil {
		t.Fatal("关闭后应退回上一篇帖子")
	}
	if a.detail.shortID != "JBYGf2A" {
		t.Errorf("退回的帖子不对：%q", a.detail.shortID)
	}
	if a.detail.replyAsID != "" {
		t.Errorf("退回上一篇后仍带着临时身份：%q", a.detail.replyAsID)
	}
	a.closeDetail()
	if a.detail != nil {
		t.Errorf("再关闭应回到列表：%+v", a.detail)
	}
}
