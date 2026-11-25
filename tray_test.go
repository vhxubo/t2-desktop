package main

import (
	"bytes"
	"image/png"
	"reflect"
	"testing"

	"github.com/egoist/mygo"
)

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

// trayItemIDs 把菜单压成一串 ID，分隔符记成 "-"，断言顺序与分组都直观。
func trayItemIDs(items []*mygo.MenuItem) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if it.Type == mygo.MenuItemSeparator {
			ids = append(ids, "-")
			continue
		}
		ids = append(ids, it.ID)
	}
	return ids
}

// 未登录时只给登录与注册，不露出发帖这类需要账号的入口。
func TestTrayMenuLoggedOut(t *testing.T) {
	d := testBook(t)
	d.app = &app{auth: d.auth}
	menu := d.trayMenu()

	want := []string{"tray-state", trayLoginID, traySignupID, "-", trayCloseAllID, "-", trayQuitID}
	if got := trayItemIDs(menu.Items()); !reflect.DeepEqual(got, want) {
		t.Errorf("未登录的托盘菜单 = %v，期望 %v", got, want)
	}
	if label := menu.Items()[0].Label; label != "未登录" {
		t.Errorf("未登录的状态行 = %q，期望 %q", label, "未登录")
	}
}

// 登录后顶部换成当前账号，动作变成发帖 / 发动态 / 设置；退出登录不放在托盘里。
func TestTrayMenuLoggedIn(t *testing.T) {
	d := testBook(t)
	addTestAccount(t, d, "jimmy")
	d.app = &app{auth: d.auth}
	menu := d.trayMenu()

	want := []string{"tray-state", trayPostID, trayMomentID, traySettingsID, "-", trayCloseAllID, "-", trayQuitID}
	if got := trayItemIDs(menu.Items()); !reflect.DeepEqual(got, want) {
		t.Errorf("已登录的托盘菜单 = %v，期望 %v", got, want)
	}
	if label := menu.Items()[0].Label; label != "已登录：jimmy" {
		t.Errorf("账号状态行 = %q，期望 %q", label, "已登录：jimmy")
	}
	for _, it := range menu.Items() {
		if it.Label == "退出登录" {
			t.Error("托盘菜单不该有退出登录入口")
		}
	}
}

// 令牌里没有昵称时状态行不能空着。
func TestTrayStateLabelWithoutUsername(t *testing.T) {
	m := newAuthManagerAt(t.TempDir(), newProxyManager(t.TempDir()))
	m.store = &memoryTokenStore{}
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if got, want := trayStateLabel(m), "已登录"; got != want {
		t.Errorf("无昵称的状态行 = %q，期望 %q", got, want)
	}
}
