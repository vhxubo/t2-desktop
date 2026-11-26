package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

// autoSignTestApp 造一份已登录的界面状态，签到请求都打到测试服务器。autoLoad
// 保持 false：测试只渲染主窗口看提示，不让列表去请求别的端点。
func autoSignTestApp(t *testing.T, base string) *app {
	t.Helper()
	a := testApp(t)
	a.client.Base = base
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy", Scope: "read:economy write:economy",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// 默认开启；关掉后写盘，重新建一份 store 仍是关的；重置回到开启。
func TestAutoSignPrefsPersist(t *testing.T) {
	dir := t.TempDir()
	s := newAutoSignPrefsStore(dir)
	if !s.on() {
		t.Fatal("自动签到默认应开启")
	}
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if s.on() {
		t.Fatal("关掉后内存里的偏好没变")
	}
	if got := newAutoSignPrefsStore(dir); got.on() {
		t.Fatalf("关掉后没落盘：%s", filepath.Join(dir, "auto_sign.json"))
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if !newAutoSignPrefsStore(dir).on() {
		t.Fatal("重置后应恢复默认开启")
	}
}

// 今日已签到：只发状态查询，不提交签到，也不弹任何提示。
func TestAutoSignAlreadySignedStaysQuiet(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			gets.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":4}`))
		case http.MethodPost:
			posts.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true}`))
		}
	}))
	defer srv.Close()
	a := autoSignTestApp(t, srv.URL+"/api")

	a.maybeAutoSignIn()
	waitCoin(t, "已签到时查询今日状态", func() bool { return gets.Load() == 1 })
	if !a.coins.sign.Signed {
		t.Fatal("已签到的状态没有同步进界面")
	}
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if tt.HasText("签到成功") {
		t.Fatalf("已经签过不该弹成功提示：%q", tt.Texts())
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("已经签过不该提交签到，实际 %d 次", n)
	}
}

// 今日未签到：提交签到成功后在主窗口顶部画一条明显的成功 Alert。
func TestAutoSignSuccessShowsAlert(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			gets.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":false,"streak":5}`))
		case http.MethodPost:
			posts.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":6,"reward_coins":5}`))
		}
	}))
	defer srv.Close()
	a := autoSignTestApp(t, srv.URL+"/api")

	a.maybeAutoSignIn()
	waitCoin(t, "签到成功", func() bool { return a.autoSignNotice != nil })
	if gets.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("签到应查一次状态、提交一次，实际 gets=%d posts=%d", gets.Load(), posts.Load())
	}
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	for _, want := range []string{"签到成功", "获得 5 金币", "连续 6 天"} {
		if !tt.HasText(want) {
			t.Errorf("成功提示缺少 %q：%q", want, tt.Texts())
		}
	}
	// 关闭后（点 Alert 的关闭按钮或定时收起）提示不再占版面。
	a.autoSignNotice.closed = true
	tt.Frame()
	if tt.HasText("签到成功") {
		t.Fatalf("关掉后还显示提示：%q", tt.Texts())
	}
}

// 状态查询说没签过，但服务端以 409 拒绝（期间别处签过了）：按已签处理且不提示。
func TestAutoSignConflictStaysQuiet(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":false,"streak":2}`))
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"c":409,"code":"conflict","m":"今日已签到","d":null}`)
		}
	}))
	defer srv.Close()
	a := autoSignTestApp(t, srv.URL+"/api")

	a.maybeAutoSignIn()
	waitCoin(t, "重复签到按已签处理", func() bool { return a.coins.sign.Signed })
	if posts.Load() != 1 {
		t.Fatalf("应提交 1 次签到，实际 %d", posts.Load())
	}
	if a.coins.sign.Streak != 2 {
		t.Errorf("连续天数应沿用今日状态，实际 %d", a.coins.sign.Streak)
	}
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if a.autoSignNotice != nil || tt.HasText("签到成功") {
		t.Fatalf("重复签到不该弹成功提示：%q", tt.Texts())
	}
}

// 设置里关掉后，打开窗口不再尝试签到。
func TestAutoSignDisabledSkipsRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()
	a := autoSignTestApp(t, srv.URL+"/api")
	if err := a.autoSignPrefs.setEnabled(false); err != nil {
		t.Fatal(err)
	}

	a.maybeAutoSignIn()
	if a.autoSignStarted || calls.Load() != 0 {
		t.Fatalf("关掉自动签到后不该发请求，started=%v calls=%d", a.autoSignStarted, calls.Load())
	}
}

// 设置窗口的开关可点，改动立即落盘；通用页默认显示这一项。
func TestAutoSignSettingsToggle(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1800)
	st.Frame()
	if !st.HasText("打开应用时自动签到") {
		t.Fatalf("通用页没有自动签到开关：%q", st.Texts())
	}
	if err := st.Click("打开应用时自动签到"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.autoSignPrefs.on() {
		t.Fatal("点开关后没有关掉自动签到")
	}
	if got := newAutoSignPrefsStore(a.proxy.dir); got.on() {
		t.Fatal("关掉自动签到没有落盘")
	}
}
