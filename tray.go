package main

import (
	_ "embed"
	"fmt"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// 系统托盘：Windows 是任务栏通知区域，macOS 是菜单栏。左键点图标把最近用过
// 的账号窗口带回前台；没有账号时回到未登录的主页面（账号面板在右键菜单里）。
// 右键弹出菜单，列出挂着的每个账号（带未读数）、账号管理入口、关闭全部
// （收进托盘，应用继续在后台跑）与退出。登录后每个账号行展开二级菜单：
// 打开窗口、发帖、发动态、退出登录；点开那一行不再直接回到窗口。
//
// 托盘是进程级资源：多账号共用这一个图标，菜单每次右键现建一份，账号列表、
// 未读数因此不会落后于应用内的状态；菜单挂着时左键也会弹出菜单，所以弹出后
// 立刻用 SetMenu(nil) 摘掉。
//
// 只在应用就绪后创建（见 main 的 WhenReady），之后只在主线程读写。

//go:embed resources/tray.png
var trayIconPNG []byte

const (
	// trayID 让 Windows 记住用户把图标放在任务栏上还是折叠区里。
	trayID      = "main"
	trayToolTip = "T2 Desktop"

	trayAccountIDPrefix = "tray-account-"
	// 账号二级菜单各项的 id 前缀：后接账号 id，菜单关闭前就能定位到具体账号。
	trayOpenIDPrefix   = "tray-open-"
	trayPostIDPrefix   = "tray-post-"
	trayMomentIDPrefix = "tray-moment-"
	trayLogoutIDPrefix = "tray-logout-"
	trayAccountsID     = "tray-accounts"
	trayCloseAllID     = "tray-close-all"
	trayLoginID        = "tray-login"
	traySignupID       = "tray-signup"
	trayQuitID         = "tray-quit"
)

// trayMenuIcons 是托盘菜单各项的图标：把现有的 *ui.SVG 渲染成菜单图标。全部
// 按 template 渲染（只取形状，跟随菜单文字颜色，深色菜单里自动变白），和侧边栏
// 入口保持同一套线性图标。mygo 要求图标只生成一次，因此集中在 init 里建好复用。
var (
	trayLoginMI    *mygo.MenuIcon
	traySignupMI   *mygo.MenuIcon
	trayAccountsMI *mygo.MenuIcon
	trayAccountMI  *mygo.MenuIcon
	trayOpenMI     *mygo.MenuIcon
	trayPostMI     *mygo.MenuIcon
	trayMomentMI   *mygo.MenuIcon
	trayLogoutMI   *mygo.MenuIcon
	trayCloseAllMI *mygo.MenuIcon
	trayQuitMI     *mygo.MenuIcon
)

func init() {
	makeMI := func(s *ui.SVG) *mygo.MenuIcon {
		mi, err := mygo.NewMenuIconSVG(s, true)
		if err != nil {
			panic("托盘菜单图标生成失败：" + err.Error())
		}
		return mi
	}
	trayLoginMI = makeMI(loginIcon)
	traySignupMI = makeMI(signupIcon)
	trayAccountsMI = makeMI(accountsIcon)
	trayAccountMI = makeMI(profileIcon)
	trayOpenMI = makeMI(lucide.AppWindow())
	trayPostMI = makeMI(lucide.SquarePen())
	trayMomentMI = makeMI(lucide.PenLine())
	trayLogoutMI = makeMI(lucide.LogOut())
	trayCloseAllMI = makeMI(collapseIcon)
	trayQuitMI = makeMI(closeIcon)
}

// startTray 放上托盘图标。必须在应用就绪后、主线程调用：图标与菜单都是
// 原生资源，早于就绪创建会 panic。创建失败只记日志：托盘只是入口的捷径，
// 窗口、通知照常可用。
func (b *accountBook) startTray() {
	if b.tray != nil {
		return
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      trayID,
		Icon:    trayIcons.forDark(currentIconDark()),
		ToolTip: trayToolTip,
	})
	if err != nil {
		log.Printf("托盘图标不可用：%v", err)
		return
	}
	b.tray = tray
	// 记下托盘真的建出来了：只有这时「关闭窗口收进托盘」才敢藏窗口。
	b.trayReady = true
	// 平时不挂菜单：有菜单时左键会弹菜单，而不是回到窗口。右键才临时挂上
	// 一份，见 showTrayMenu。
	tray.OnClick(b.showMainWindowFromTray)
	tray.OnRightClick(b.showTrayMenu)
	// 退出前摘掉图标，否则通知区域会留下一块点不动的残影，直到鼠标划过。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { b.destroyTray() })
}

// showMainWindowFromTray 是左键单击：把最近用过的账号窗口带回前台；没有账号
// 时回到未登录的主页面，而不是账号面板（后者在右键菜单的「账号管理」里）。
func (b *accountBook) showMainWindowFromTray() {
	b.showMainFromTray(b.raiseAccountWindow, b.raiseGuestWindow)
}

// showMainFromTray 是可测试的托盘左键骨架：挑一扇主窗口带到前台。有账号就用
// 最近用过的那个（窗口没开就开一扇），一个都没有时回未登录主页面。
func (b *accountBook) showMainFromTray(raiseAccount func(string), raiseGuest func()) {
	if id := b.activeAccountID(); id != "" {
		if acc := b.account(id); acc != nil && acc.app != nil {
			raiseAccount(id)
			return
		}
	}
	if accounts := b.accountsView(); len(accounts) > 0 {
		// 有账号但窗口都关着：把第一个打开，别让人点了图标什么都没发生。
		raiseAccount(accounts[0].ID)
		return
	}
	raiseGuest()
}

// raiseGuestWindow 把未登录主窗口带回前台；没建过就开一扇。没有账号时它是
// 应用默认的主页面：托盘左键与退出最后一个账号后都回到这里。
func (b *accountBook) raiseGuestWindow() {
	if guest := b.guest; guest != nil {
		guest.raiseMainWindow()
		return
	}
	b.openGuestWindow()
}

// showTrayMenu 是右键菜单的入口：现建一份反映当前账号状态的菜单，弹出，
// 返回后立刻摘掉。PopUpMenu 在菜单关闭前不返回，所以摘掉时菜单早已收起。
func (b *accountBook) showTrayMenu() {
	tray := b.tray
	if tray == nil {
		return
	}
	tray.SetMenu(b.trayMenu())
	tray.PopUpMenu()
	tray.SetMenu(nil)
}

// destroyTray 摘掉托盘图标。退出流程里调用；重复调用无害。
func (b *accountBook) destroyTray() {
	tray := b.tray
	if tray == nil {
		return
	}
	b.tray = nil
	b.trayReady = false
	tray.Destroy()
}

// trayMenu 建右键菜单：每个账号一行（带未读数）并带自己的二级菜单，然后是
// 账号管理与退出。未登录时只有状态、登录与注册，没有二级菜单。
func (b *accountBook) trayMenu() *mygo.Menu {
	accounts := b.accountsView()
	var items []*mygo.MenuItem
	if len(accounts) == 0 {
		items = append(items,
			&mygo.MenuItem{ID: trayAccountIDPrefix + "none", Label: "未登录", Icon: trayAccountMI, Disabled: true},
			&mygo.MenuItem{
				ID:    trayLoginID,
				Label: "登录",
				Icon:  trayLoginMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { b.startAddAccount() },
			},
			&mygo.MenuItem{
				ID:    traySignupID,
				Label: "注册",
				Icon:  traySignupMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { _ = b.openURL(signupURL) },
			},
		)
	} else {
		for _, acc := range accounts {
			id := acc.ID
			label := acc.Name
			switch {
			case acc.NeedsLogin:
				label += "（需要重新登录）"
			case acc.Unread > 0:
				label += fmt.Sprintf("（%d 条未读）", acc.Unread)
			}
			items = append(items, &mygo.MenuItem{
				ID:      trayAccountIDPrefix + id,
				Label:   label,
				Icon:    trayAccountMI,
				Submenu: b.trayAccountSubmenu(id),
			})
		}
	}
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayAccountsID,
			Label: "账号管理",
			Icon:  trayAccountsMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.showPanel() },
		},
		&mygo.MenuItem{
			ID:    trayCloseAllID,
			Label: "关闭全部",
			Icon:  trayCloseAllMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.closeAllWindows() },
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayQuitID,
			Label: "退出",
			Icon:  trayQuitMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.quitApp() },
		},
	)
	return mygo.NewMenu(items)
}

// trayAccountSubmenu 是一个账号的二级菜单：打开窗口、发帖、发动态、分隔线、
// 退出登录。各项都绑住账号 id，多账号时不会串到别的会话上；菜单建在右键弹
// 出时，所以读到的始终是当下挂着的账号。
func (b *accountBook) trayAccountSubmenu(id string) []*mygo.MenuItem {
	return []*mygo.MenuItem{
		{
			ID:    trayOpenIDPrefix + id,
			Label: "打开窗口",
			Icon:  trayOpenMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.raiseAccountWindow(id) },
		},
		{
			ID:    trayPostIDPrefix + id,
			Label: "发帖",
			Icon:  trayPostMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.openAccountCompose(id, false) },
		},
		{
			ID:    trayMomentIDPrefix + id,
			Label: "发动态",
			Icon:  trayMomentMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.openAccountCompose(id, true) },
		},
		mygo.Separator(),
		{
			ID:    trayLogoutIDPrefix + id,
			Label: "退出登录",
			Icon:  trayLogoutMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { b.logoutAccount(id) },
		},
	}
}
