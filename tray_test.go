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

// trayItem 按 id 取托盘菜单里的一项（含二级菜单），取不到就报出整份菜单，
// 方便定位。
func trayItem(t *testing.T, menu *mygo.Menu, id string) *mygo.MenuItem {
	t.Helper()
	it := menu.ItemByID(id)
	if it == nil {
		t.Fatalf("托盘菜单里没有 %q：%q", id, trayLabels(menu))
	}
	return it
}

// traySubLabels 是一个账号二级菜单的文案，分隔线记为空串。
func traySubLabels(item *mygo.MenuItem) []string {
	if item == nil || item.Submenu == nil {
		return nil
	}
	var out []string
	for _, child := range item.Submenu {
		out = append(out, child.Label)
	}
	return out
}

// 一个账号都没有时托盘顶部是「未登录」，紧接着是登录与注册两个入口；
// 账号管理与退出始终都在。
func TestTrayMenuOffersLoginWhenNoAccounts(t *testing.T) {
	b := testBook(t)
	menu := b.trayMenu()
	want := []string{"未登录", "登录", "注册", "", "账号管理", "关闭全部", "", "退出"}
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

// 托盘左键是「回到主页面」，不是打开账号管理：没有账号时回未登录主窗口。
// 账号面板仍可以从右键菜单的「账号管理」进入。
func TestTrayClickReturnsToMainPageWithoutAccounts(t *testing.T) {
	b := testBook(t)
	var accounts []string
	guestCalls := 0
	b.showMainFromTray(
		func(id string) { accounts = append(accounts, id) },
		func() { guestCalls++ },
	)
	if guestCalls != 1 {
		t.Fatalf("没有账号时托盘左键调用了 %d 次未登录主页面，期望 1 次", guestCalls)
	}
	if len(accounts) != 0 {
		t.Errorf("没有账号时托盘左键不该打开账号窗口：%v", accounts)
	}
}

// 有账号时托盘左键照旧回到账号主窗口（这里只断言挑账号，不建原生窗口）。
func TestTrayClickStillRaisesAccountWindow(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	var accounts []string
	guestCalls := 0
	b.showMainFromTray(
		func(id string) { accounts = append(accounts, id) },
		func() { guestCalls++ },
	)
	if len(accounts) != 1 || accounts[0] != acc.ID {
		t.Fatalf("托盘左键选中的账号 = %v，期望 %q", accounts, acc.ID)
	}
	if guestCalls != 0 {
		t.Errorf("有账号时托盘左键不该去未登录主页面，却去了 %d 次", guestCalls)
	}
}

// 托盘菜单的可操作项都要带图标：和侧边栏入口同一套线性图标，系统菜单才会
// 在图标列画出它们（Windows 任务栏、Linux 托盘的菜单都显示图标列）。
func TestTrayMenuItemsCarryIcons(t *testing.T) {
	b := testBook(t)
	addTestAccount(t, b, "jimmy")
	menu := b.trayMenu()
	id := b.accountsView()[0].ID
	ids := []string{
		trayAccountIDPrefix + id, trayOpenIDPrefix + id, trayPostIDPrefix + id,
		trayMomentIDPrefix + id, trayLogoutIDPrefix + id,
		trayAccountsID, trayCloseAllID, trayQuitID,
	}
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
		"", "账号管理", "关闭全部", "", "退出",
	}
	if got := trayLabels(menu); !reflect.DeepEqual(got, want) {
		t.Fatalf("托盘菜单 %q，期望 %q", got, want)
	}
}

// 二级菜单里的「打开窗口」就是打开这个账号的窗口：窗口是懒建的（测试里没有
// 主循环，建不出来），但「最近用过的账号」要立刻记下，托盘左键与启动时的前台
// 窗口都读它。多账号时点另一个账号的子项也不会串到前一个账号上。
func TestTrayAccountEntryRemembersTheAccount(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	menu := b.trayMenu()
	if got := traySubLabels(trayItem(t, menu, trayAccountIDPrefix+jimmy.ID)); !reflect.DeepEqual(got,
		[]string{"打开窗口", "发帖", "发动态", "", "退出登录"}) {
		t.Fatalf("账号二级菜单是 %q", got)
	}
	it := trayItem(t, menu, trayOpenIDPrefix+jimmy.ID)
	it.Click(it, nil)
	if got := b.activeAccountID(); got != jimmy.ID {
		t.Fatalf("点过 %q 之后最近使用的账号是 %q", amy.ID, got)
	}
}

// 每个账号各带一份二级菜单，发帖 / 发动态打开发帖窗口的对应模式，退出登录
// 只退出这一行所属的账号；未登录时没有二级菜单。
func TestTrayAccountSubmenuActionsTargetTheirAccount(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	menu := b.trayMenu()

	// 给两个账号各挂一份界面状态：headless 下发帖窗口不会真建出来，但模式会
	// 写进各自的 compose，足以看出动作落到了哪个账号。
	jimmyApp := &app{auth: jimmy.auth, book: b, accountID: jimmy.ID}
	amyApp := &app{auth: amy.auth, book: b, accountID: amy.ID}
	jimmy.app, amy.app = jimmyApp, amyApp

	post := trayItem(t, menu, trayPostIDPrefix+amy.ID)
	post.Click(post, nil)
	if jimmyApp.compose != nil {
		t.Error("点 amy 的「发帖」不该动 jimmy 的发帖窗口")
	}
	if amyApp.compose == nil || amyApp.compose.mode != composeModePost {
		t.Fatalf("amy 的发帖模式 = %+v，期望帖子模式", amyApp.compose)
	}

	moment := trayItem(t, menu, trayMomentIDPrefix+jimmy.ID)
	moment.Click(moment, nil)
	if amyApp.compose == nil || amyApp.compose.mode != composeModePost {
		t.Fatalf("点 jimmy 的「发动态」改了 amy 的模式：%+v", amyApp.compose)
	}
	if jimmyApp.compose == nil || jimmyApp.compose.mode != composeModeMoment {
		t.Fatalf("jimmy 的发动态模式 = %+v，期望动态模式", jimmyApp.compose)
	}

	logout := trayItem(t, menu, trayLogoutIDPrefix+amy.ID)
	logout.Click(logout, nil)
	if got := b.accountsView(); len(got) != 1 || got[0].ID != jimmy.ID {
		t.Fatalf("退出 amy 之后账号列表 = %v，期望只剩 jimmy", got)
	}
	if jimmyApp.compose == nil {
		t.Error("退出别的账号不该丢掉 jimmy 的发帖窗口")
	}

	empty := testBook(t).trayMenu()
	if it := empty.ItemByID(trayAccountIDPrefix + "none"); it == nil || it.Submenu != nil {
		t.Error("未登录的状态行不该带二级菜单")
	}
}

// 托盘里的「关闭全部」把窗口收进托盘，但应用继续在后台跑；它和「退出」
// 是两条不同的路子，点一下只调一次收窗动作。
func TestTrayCloseAllWindowsHidesWindowsWithoutQuitting(t *testing.T) {
	b := testBook(t)
	addTestAccount(t, b, "jimmy")
	closed := 0
	quits := 0
	b.closeAllWindows = func() { closed++ }
	b.quitApp = func() { quits++ }
	it := trayItem(t, b.trayMenu(), trayCloseAllID)
	it.Click(it, nil)
	if closed != 1 {
		t.Fatalf("点「关闭全部」调用了 %d 次收窗，期望 1 次", closed)
	}
	if quits != 0 {
		t.Fatalf("点「关闭全部」不该退出应用，却退出了 %d 次", quits)
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
