package main

import (
	"bytes"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo"
)

// trayLabels 是菜单里各行的文案，分隔线记为空串：用来断言整份菜单的形状，
// 而不只是「某个 id 在不在」。
func trayLabels(menu *mygo.Menu) []string {
	var out []string
	for _, it := range menu.Items() {
		out = append(out, it.Label)
	}
	return out
}

// trayItem 按 id 取托盘菜单里的一项，取不到就报出整份菜单，方便定位。
func trayItem(t *testing.T, menu *mygo.Menu, id string) *mygo.MenuItem {
	t.Helper()
	it := menu.ItemByID(id)
	if it == nil {
		t.Fatalf("托盘菜单里没有 %q：%q", id, trayLabels(menu))
	}
	return it
}

// 未登录时托盘菜单顶部是「未登录」，紧接着是登录与注册两个入口；通知、
// 发帖与退出三个入口始终都在。
func TestTrayMenuOffersLoginWhenSignedOut(t *testing.T) {
	a := testApp(t)
	menu := a.trayMenu()
	want := []string{"未登录", "登录", "注册", "", "通知", "发帖", "", "退出"}
	if got := trayLabels(menu); !reflect.DeepEqual(got, want) {
		t.Fatalf("未登录时的托盘菜单 %q，期望 %q", got, want)
	}
	// 账号状态行只显示状态，点它不该有任何动作。
	if account := trayItem(t, menu, trayAccountID); !account.Disabled {
		t.Error("账号状态行不该可点")
	}
	if trayItem(t, menu, trayLoginID).Click == nil {
		t.Error("「登录」没有接上动作")
	}
	if trayItem(t, menu, traySignupID).Click == nil {
		t.Error("「注册」没有接上动作")
	}
}

// 托盘菜单的可操作项都要带图标：和侧边栏入口同一套线性图标，系统菜单才会
// 在图标列画出它们（Windows 任务栏、Linux 托盘的菜单都显示图标列）。
func TestTrayMenuItemsCarryIcons(t *testing.T) {
	a := testApp(t)
	for _, id := range []string{trayLoginID, traySignupID, trayNotificationsID, trayComposeID, trayQuitID} {
		if it := trayItem(t, a.trayMenu(), id); it.Icon == nil {
			t.Errorf("托盘菜单项 %q 没有图标", id)
		}
	}
}

// 登录后账号状态变成「已登录：用户名」，登录按钮换成「退出登录」。
func TestTrayMenuShowsAccountAndLogoutWhenSignedIn(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	menu := a.trayMenu()
	want := []string{"已登录：jimmy", "退出登录", "", "通知", "发帖", "", "退出"}
	if got := trayLabels(menu); !reflect.DeepEqual(got, want) {
		t.Fatalf("登录后的托盘菜单 %q，期望 %q", got, want)
	}
	if menu.ItemByID(trayLoginID) != nil {
		t.Error("登录后不该再出现「登录」")
	}
}

// 托盘里的「退出登录」就是应用内的那次退出：令牌被清掉，账号状态行随之下
// 次弹出菜单时回到「未登录」。
func TestTrayLogoutSignsOut(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	it := trayItem(t, a.trayMenu(), trayLogoutID)
	it.Click(it, nil)
	if a.auth.loggedIn() {
		t.Fatal("点托盘里的「退出登录」后仍是登录状态")
	}
	if got := trayItem(t, a.trayMenu(), trayAccountID).Label; got != "未登录" {
		t.Errorf("退出后账号状态是 %q，期望「未登录」", got)
	}
}

// 未登录时从托盘点「通知」「发帖」和从窗口里点一样：先弹授权，登录成功后
// 再把窗口打开，用户不用回主窗口再点一次。
func TestTrayEntriesAskForLoginFirst(t *testing.T) {
	for _, tc := range []struct {
		id    string
		entry string
	}{
		{trayComposeID, "发帖"},
		{trayNotificationsID, "通知"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			a := testApp(t)
			it := trayItem(t, a.trayMenu(), tc.id)
			it.Click(it, nil)
			if !a.authGate.open {
				t.Fatalf("未登录点托盘里的「%s」没有弹出授权", tc.entry)
			}
			if !strings.Contains(a.authGate.need.Message, tc.entry) {
				t.Errorf("授权提示 %q 没有说明「%s」需要登录", a.authGate.need.Message, tc.entry)
			}
			if len(a.authGate.replays) == 0 {
				t.Errorf("点「%s」后没有登记登录成功后要重开的窗口", tc.entry)
			}
		})
	}
}

// 托盘里的「退出」走应用自己的退出流程（关窗口、停通知）。
func TestTrayQuitQuitsTheApp(t *testing.T) {
	a := testApp(t)
	quits := 0
	a.quitApp = func() { quits++ }
	it := trayItem(t, a.trayMenu(), trayQuitID)
	it.Click(it, nil)
	if quits != 1 {
		t.Fatalf("点「退出」调用了 %d 次退出，期望 1 次", quits)
	}
}

// 托盘图标是 32×32 的 PNG：16 点在高分屏上的尺寸。图标不对时 Windows 会
// 画出模糊或空白的一格，而这里不会跑到真窗口上才发现。
func TestTrayIconIsA32PixelPNG(t *testing.T) {
	cfg, err := png.DecodeConfig(bytes.NewReader(trayIconPNG))
	if err != nil {
		t.Fatalf("托盘图标不是 PNG：%v", err)
	}
	if cfg.Width != 32 || cfg.Height != 32 {
		t.Errorf("托盘图标 %dx%d，期望 32x32", cfg.Width, cfg.Height)
	}
}
