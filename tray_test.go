package main

import (
	"bytes"
	"image/png"
	"reflect"
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

// 一个账号都没有时托盘顶部是「未登录」，紧接着是登录与注册两个入口；
// 账号管理与退出始终都在。
func TestTrayMenuOffersLoginWhenNoAccounts(t *testing.T) {
	b := testBook(t)
	menu := b.trayMenu()
	want := []string{"未登录", "登录", "注册", "", "账号管理", "", "退出"}
	if got := trayLabels(menu); !reflect.DeepEqual(got, want) {
		t.Fatalf("没有账号时的托盘菜单 %q，期望 %q", got, want)
	}
	// 状态行只显示状态，点它不该有任何动作。
	if account := trayItem(t, menu, trayAccountIDPrefix+"none"); !account.Disabled {
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
	b := testBook(t)
	addTestAccount(t, b, "jimmy")
	menu := b.trayMenu()
	ids := []string{trayAccountIDPrefix + b.accountsView()[0].ID, trayAccountsID, trayQuitID}
	for _, id := range ids {
		if it := trayItem(t, menu, id); it.Icon == nil {
			t.Errorf("托盘菜单项 %q 没有图标", id)
		}
	}
	// 没有账号时那两个入口也要有图标。
	empty := testBook(t).trayMenu()
	for _, id := range []string{trayLoginID, traySignupID} {
		if it := trayItem(t, empty, id); it.Icon == nil {
			t.Errorf("托盘菜单项 %q 没有图标", id)
		}
	}
}

// 挂着几个账号，托盘就列几行：有未读的带上条数，登录失效的写明要重新登录。
func TestTrayMenuListsEveryAccountWithUnread(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	setTestUnread(b, jimmy.ID, 3)
	setTestNeedLogin(b, amy.ID)

	menu := b.trayMenu()
	want := []string{
		"jimmy（3 条未读）",
		"amy（需要重新登录）",
		"", "账号管理", "", "退出",
	}
	if got := trayLabels(menu); !reflect.DeepEqual(got, want) {
		t.Fatalf("托盘菜单 %q，期望 %q", got, want)
	}
}

// 点账号那一行就是打开它的窗口：窗口是懒建的（测试里没有主循环，建不出来），
// 但「最近用过的账号」要立刻记下，托盘左键与启动时的前台窗口都读它。
func TestTrayAccountEntryRemembersTheAccount(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	it := trayItem(t, b.trayMenu(), trayAccountIDPrefix+jimmy.ID)
	it.Click(it, nil)
	if got := b.activeAccountID(); got != jimmy.ID {
		t.Fatalf("点过 %q 之后最近使用的账号是 %q", amy.ID, got)
	}
}

// 托盘里的「退出」走应用自己的退出流程（关窗口、停通知）。
func TestTrayQuitQuitsTheApp(t *testing.T) {
	b := testBook(t)
	quits := 0
	b.quitApp = func() { quits++ }
	it := trayItem(t, b.trayMenu(), trayQuitID)
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
