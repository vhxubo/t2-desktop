package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// fakeStartupHost 按生产接口记录启动链调用。run 里触发 WhenReady 收到的回调，
// 从而复现「app 建好后注册协议、就绪后才装通知、最后起后台任务」的时序。
type fakeStartupHost struct {
	calls        []string
	lockOK       bool
	registerErr  error
	supportNotif bool
	notifier     func(nativeNotice) error
	ready        func()
	onRegister   func()
	onReady      func(*desktop, *app)
	registeredID string
}

func (h *fakeStartupHost) configureHTTP(*desktop) { h.calls = append(h.calls, "configure") }
func (h *fakeStartupHost) onOpenURL(func(string)) { h.calls = append(h.calls, "onOpenURL") }
func (h *fakeStartupHost) requestSingleInstanceLock() bool {
	h.calls = append(h.calls, "lock")
	return h.lockOK
}
func (h *fakeStartupHost) registerURLScheme(id string) error {
	h.calls = append(h.calls, "register")
	h.registeredID = id
	if h.onRegister != nil {
		h.onRegister()
	}
	return h.registerErr
}
func (h *fakeStartupHost) onNotificationClick(func(string)) {
	h.calls = append(h.calls, "onNotificationClick")
}
func (h *fakeStartupHost) onDidBecomeActive(func()) {
	h.calls = append(h.calls, "onDidBecomeActive")
}
func (h *fakeStartupHost) onBeforeQuit(func(*mygo.QuitEvent)) {
	h.calls = append(h.calls, "onBeforeQuit")
}
func (h *fakeStartupHost) notificationsSupported() bool {
	h.calls = append(h.calls, "notificationsSupported")
	return h.supportNotif
}
func (h *fakeStartupHost) nativeNotifier() func(nativeNotice) error {
	h.calls = append(h.calls, "nativeNotifier")
	return h.notifier
}
func (h *fakeStartupHost) whenReady(fn func()) {
	h.calls = append(h.calls, "whenReady")
	h.ready = fn
}
func (h *fakeStartupHost) startBackground(d *desktop, a *app) {
	h.calls = append(h.calls, "startBackground")
	if h.onReady != nil {
		h.onReady(d, a)
	}
}
func (h *fakeStartupHost) run() error {
	h.calls = append(h.calls, "run")
	if h.ready != nil {
		h.ready()
	}
	return nil
}

// 生产启动链的回归：app 先建好，协议注册失败写进进程级状态，WhenReady 里
// 装上平台通知后才起后台任务。任何一步被挪到 app 构造时快照，都会在这里失败。
func TestStartupChainWiresLateProcessState(t *testing.T) {
	b := testBook(t)
	var notices []nativeNotice
	h := &fakeStartupHost{
		lockOK:       true,
		registerErr:  errors.New("scheme registration failed"),
		supportNotif: true,
		notifier: func(n nativeNotice) error {
			notices = append(notices, n)
			return nil
		},
	}
	h.onRegister = func() {
		if b.app == nil {
			t.Error("注册协议时 app 还没建好")
			return
		}
		if got := b.app.schemeWarning(); got != "" {
			t.Errorf("注册失败还没发生，登录页已显示警告：%q", got)
		}
		if b.app.nativeNotifier() != nil {
			t.Error("应用就绪前不该有平台通知函数")
		}
	}
	h.onReady = func(d *desktop, a *app) {
		if d.notify == nil || a.nativeNotifier() == nil {
			t.Error("就绪回调里没有把通知函数装到进程级状态")
		}
		if a.schemeWarning() == "" {
			t.Error("就绪时登录页读不到协议注册失败")
		}
		if !d.mainLoop {
			t.Error("后台任务启动前主循环标记还没立起来")
		}
	}

	if err := runStartup(b, h); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"configure", "onOpenURL", "lock", "register",
		"onNotificationClick", "onDidBecomeActive", "onBeforeQuit",
		"whenReady", "run", "notificationsSupported", "nativeNotifier", "startBackground",
	}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("启动顺序不符：\n got %v\nwant %v", h.calls, want)
	}
	if h.registeredID != appIdentifier {
		t.Errorf("注册的协议 = %q，期望 %q", h.registeredID, appIdentifier)
	}

	a := b.app
	a.pageName = "login"
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText(a.schemeWarning()) {
		t.Errorf("登录页没有显示协议注册失败的说明：%q", tt.Texts())
	}
	a.showNotices([]nativeNotice{{ID: "n1", Title: "新回复", Target: "topic:1"}})
	if len(notices) != 1 || notices[0].ID != "n1" {
		t.Fatalf("生产链装上的通知函数没有送达：%+v", notices)
	}
	if got := a.noticeTarget("n1"); got != "topic:1" {
		t.Errorf("通知点击目标没记住：%q", got)
	}
}

// 第二个实例只把深链交给第一个实例，不能注册协议、装通知或开窗口。
func TestStartupChainSecondInstanceStopsBeforeRegistration(t *testing.T) {
	b := testBook(t)
	h := &fakeStartupHost{lockOK: false, supportNotif: true}
	if err := runStartup(b, h); err != nil {
		t.Fatal(err)
	}
	want := []string{"configure", "onOpenURL", "lock"}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("第二实例多做了一步：\n got %v\nwant %v", h.calls, want)
	}
	if b.schemeWarn != "" || b.notify != nil {
		t.Fatalf("第二实例不该改进程级状态：warn=%q notifyInstalled=%t", b.schemeWarn, b.notify != nil)
	}
}

// 平台不支持原生通知时，就绪回调不能装通知函数；通知窗口要如实说明不可用。
func TestStartupChainSkipsNotifierWhenPlatformCannotNotify(t *testing.T) {
	b := testBook(t)
	h := &fakeStartupHost{lockOK: true, supportNotif: false}
	if err := runStartup(b, h); err != nil {
		t.Fatal(err)
	}
	if b.notify != nil || b.app.nativeNotifier() != nil {
		t.Fatal("平台不支持通知时仍装上了通知函数")
	}
	for _, c := range h.calls {
		if c == "nativeNotifier" {
			t.Fatal("平台不支持通知时不该去取通知函数")
		}
	}
	tt := ui.NewTester(b.app.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	if !tt.HasText("原生通知不可用") {
		t.Errorf("通知窗口没有说明原生通知不可用：%q", tt.Texts())
	}
}
