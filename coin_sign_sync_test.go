package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// signSyncTestApp 造一份已登录、允许自动加载的状态；签到请求都打到测试服务器。
func signSyncTestApp(t *testing.T, base string) *app {
	t.Helper()
	a := testApp(t)
	a.autoLoad = true
	a.client.Base = base
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy", Scope: "read:economy write:economy",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// 打开标题栏账号菜单会静默同步今日签到状态；同一自然日再开合只命中缓存，
// 不再发请求。
func TestAccountMenuSignSyncsOncePerDay(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("签到同步应发 GET，实际是 %s", r.Method)
		}
		gets.Add(1)
		_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":4}`))
	}))
	defer srv.Close()
	a := signSyncTestApp(t, srv.URL+"/api")

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)
	waitCoin(t, "打开账号菜单同步签到状态", func() bool {
		return gets.Load() == 1 && a.coins.signSyncedDate == coinTodayDate()
	})
	tt.Frame()
	if !tt.HasText("今日已签到") {
		t.Fatalf("同步后账号菜单没显示最新签到状态：%q", tt.Texts())
	}

	// 收起再展开：当天状态已在手，不该重复请求。
	openAccountMenu(t, tt)
	tt.Frame()
	openAccountMenu(t, tt)
	tt.Frame()
	if n := gets.Load(); n != 1 {
		t.Fatalf("同一天重复打开菜单多发了签到请求：gets=%d", n)
	}
}

// 隔天（或换账号后同步日期过期）时，昨天缓存的「已签到」不能先冒充今天的，
// 打开菜单要用新响应覆盖。
func TestAccountMenuSignSyncsAfterStaleDay(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		gets.Add(1)
		_, _ = io.WriteString(w, envelopeJSON(`{"signed":false}`))
	}))
	defer srv.Close()
	a := signSyncTestApp(t, srv.URL+"/api")
	a.coins.sign = coinSignStatus{Known: true, Signed: true, Streak: 7}
	a.coins.signSyncedDate = "2000-01-01"

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)
	waitCoin(t, "隔天重新同步签到状态", func() bool {
		return gets.Load() == 1 && a.coins.sign.Known && !a.coins.sign.Signed &&
			a.coins.signSyncedDate == coinTodayDate()
	})
	tt.Frame()
	if !tt.HasText("今日未签到") {
		t.Fatalf("隔天后仍显示旧签到状态：%q", tt.Texts())
	}
}

// 同步失败要退避：菜单每帧都可能重建，不能一直重发注定失败的请求。
func TestAccountMenuSignSyncBacksOffOnFailure(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/sign" {
			http.NotFound(w, r)
			return
		}
		gets.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := signSyncTestApp(t, srv.URL+"/api")

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)
	waitCoin(t, "首次签到同步失败", func() bool {
		return gets.Load() == 1 && !a.coins.signSyncing
	})
	openAccountMenu(t, tt)
	tt.Frame()
	openAccountMenu(t, tt)
	tt.Frame()
	if n := gets.Load(); n != 1 {
		t.Fatalf("失败后没有退避，重复发出了签到请求：gets=%d", n)
	}
}

// 签到状态写进按账号隔离的本地缓存，但只对同一自然日有效；隔天不能把昨天
// 的「已签到」带到新的一天。
func TestCoinSignCacheRestoredOnlyForToday(t *testing.T) {
	dir := t.TempDir()
	token := tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy", Scope: "read:economy"}
	reopen := func() *app {
		t.Helper()
		b := newApp(dir)
		b.autoLoad = false
		if err := b.auth.storeToken(token); err != nil {
			t.Fatal(err)
		}
		return b
	}

	a := reopen()
	a.coins.sign = coinSignStatus{Known: true, Signed: true, Streak: 3}
	a.coins.signSyncedDate = coinTodayDate()
	a.saveCoinCache()

	b := reopen()
	b.loadCoinCache()
	if !b.coins.sign.Known || !b.coins.sign.Signed || b.coins.sign.Streak != 3 {
		t.Errorf("同一天的签到状态没从缓存恢复：%+v", b.coins.sign)
	}
	if b.coins.signSyncedDate != coinTodayDate() {
		t.Errorf("缓存没有带回同步日期：%q", b.coins.signSyncedDate)
	}

	old := coinNow
	coinNow = func() time.Time { return old().AddDate(0, 0, 1) }
	defer func() { coinNow = old }()
	c := reopen()
	c.loadCoinCache()
	if c.coins.sign.Known || c.coins.signSyncedDate != "" {
		t.Errorf("隔天不该恢复昨天的签到状态：%+v / %q", c.coins.sign, c.coins.signSyncedDate)
	}
}
