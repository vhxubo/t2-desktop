package main

import (
	"bytes"
	"image/png"
	"path/filepath"
	"reflect"
	"strings"
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

	want := []string{"tray-state", trayLoginID, traySignupID, "-", trayNativeNotifyID, "-", trayCloseAllID, "-", trayQuitID}
	if got := trayItemIDs(menu.Items()); !reflect.DeepEqual(got, want) {
		t.Errorf("未登录的托盘菜单 = %v，期望 %v", got, want)
	}
	if label := menu.Items()[0].Label; label != "未登录" {
		t.Errorf("未登录的状态行 = %q，期望 %q", label, "未登录")
	}
}

// 登录后顶部换成通知入口（带未读角标），动作变成发帖 / 发动态 / 设置；
// 不再写「已登录」，退出登录也不放在托盘里。
func TestTrayMenuLoggedIn(t *testing.T) {
	d := testBook(t)
	addTestAccount(t, d, "jimmy")
	d.app = &app{auth: d.auth}
	d.app.unread = 3
	menu := d.trayMenu()

	want := []string{trayNotificationsID, trayPostID, trayMomentID, traySettingsID, "-", trayNativeNotifyID, "-", trayCloseAllID, "-", trayQuitID}
	if got := trayItemIDs(menu.Items()); !reflect.DeepEqual(got, want) {
		t.Errorf("已登录的托盘菜单 = %v，期望 %v", got, want)
	}
	first := menu.Items()[0]
	if first.Label != "通知" {
		t.Errorf("通知入口文案 = %q，期望 %q", first.Label, "通知")
	}
	if first.Badge != "3" || first.BadgeShape != mygo.BadgeShapePill {
		t.Errorf("未读角标 = %q / %q，期望 %q / pill", first.Badge, first.BadgeShape, "3")
	}
	if first.Icon == nil || first.Click == nil {
		t.Error("通知入口应带铃铛图标且可点击")
	}
	for _, it := range menu.Items() {
		if strings.Contains(it.Label, "已登录") || it.Label == "退出登录" {
			t.Errorf("托盘菜单不该出现 %q", it.Label)
		}
	}
}

// 未读数为 0 时不画角标；三位数以上收成 99+，和标题栏铃铛一致。
func TestTrayNotificationBadge(t *testing.T) {
	cases := []struct {
		unread int
		badge  string
	}{
		{0, ""},
		{7, "7"},
		{100, "99+"},
	}
	for _, tc := range cases {
		d := testBook(t)
		addTestAccount(t, d, "jimmy")
		d.app = &app{auth: d.auth, unread: tc.unread}
		first := d.trayMenu().Items()[0]
		if first.Badge != tc.badge {
			t.Errorf("未读 %d 的角标 = %q，期望 %q", tc.unread, first.Badge, tc.badge)
		}
	}
}

// 托盘的原生通知开关读写同一份 notify.json：勾选状态跟着偏好走，点一下关、
// 再点一下开，下次右键也能看到新状态。
func TestTrayNativeNotificationToggle(t *testing.T) {
	d := testBook(t)
	addTestAccount(t, d, "jimmy")
	d.notify = func(nativeNotice) error { return nil }
	d.app = &app{auth: d.auth, notificationPrefs: d.notificationPrefs}

	item := d.trayMenu().ItemByID(trayNativeNotifyID)
	if item == nil {
		t.Fatal("托盘菜单缺少原生通知开关")
	}
	if item.Type != mygo.MenuItemCheckbox || item.Label != "原生通知" || !item.IsChecked() {
		t.Fatalf("原生通知开关 = %q / %q / checked=%v，期望 checkbox / 原生通知 / true",
			item.Type, item.Label, item.IsChecked())
	}
	if item.Click == nil || item.Icon == nil {
		t.Fatal("原生通知开关应可点击且带图标")
	}

	item.Click(item, nil)
	if d.notificationPrefs.notificationEnabled() {
		t.Error("点击后原生通知应为关")
	}
	var cfg notificationPrefsConfig
	if err := readJSONFile(filepath.Join(d.dir, "notify.json"), &cfg); err != nil {
		t.Fatalf("读 notify.json 失败：%v", err)
	}
	if !cfg.Disabled {
		t.Error("关闭后 notify.json 应写入 disabled=true")
	}
	if again := d.trayMenu().ItemByID(trayNativeNotifyID); again.IsChecked() {
		t.Error("重新打开的托盘菜单应显示原生通知已关闭")
	}

	again := d.trayMenu().ItemByID(trayNativeNotifyID)
	again.Click(again, nil)
	if !d.notificationPrefs.notificationEnabled() {
		t.Error("再次点击后原生通知应为开")
	}
	if err := readJSONFile(filepath.Join(d.dir, "notify.json"), &cfg); err != nil {
		t.Fatalf("读 notify.json 失败：%v", err)
	}
	if cfg.Disabled {
		t.Error("开启后 notify.json 应写入 disabled=false")
	}
}

// 没有原生通知的平台不给一个点了没反应的开关，和通知窗口的「不可用」一致。
func TestTrayNativeNotificationUnavailable(t *testing.T) {
	d := testBook(t)
	d.app = &app{auth: d.auth}

	item := d.trayMenu().ItemByID(trayNativeNotifyID)
	if item == nil {
		t.Fatal("托盘菜单缺少原生通知入口")
	}
	if !item.Disabled || item.Label != "原生通知不可用" || item.Click != nil {
		t.Fatalf("不可用的原生通知入口 = disabled=%v / %q / click=%v，期望禁用说明且不可点",
			item.Disabled, item.Label, item.Click != nil)
	}
}
