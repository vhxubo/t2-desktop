package main

import (
	_ "embed"
	"fmt"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 系统托盘：Windows 是任务栏通知区域，macOS 是菜单栏。左键点图标把最近用过
// 的账号窗口带回前台；右键弹出菜单，列出挂着的每个账号（带未读数）、账号
// 管理入口与退出。
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
	trayAccountsID      = "tray-accounts"
	trayLoginID         = "tray-login"
	traySignupID        = "tray-signup"
	trayQuitID          = "tray-quit"
)

// trayMenuIcons 是托盘菜单各项的图标：把现有的 *ui.SVG 渲染成菜单图标。全部
// 按 template 渲染（只取形状，跟随菜单文字颜色，深色菜单里自动变白），和侧边栏
// 入口保持同一套线性图标。mygo 要求图标只生成一次，因此集中在 init 里建好复用。
var (
	trayLoginMI    *mygo.MenuIcon
	traySignupMI   *mygo.MenuIcon
	trayAccountsMI *mygo.MenuIcon
	trayAccountMI  *mygo.MenuIcon
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
		Icon:    trayIconPNG,
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

// showMainWindowFromTray 是左键单击：把最近用过的账号窗口带回前台；一个
// 账号都没有时打开账号面板，那里有登录入口。
func (b *accountBook) showMainWindowFromTray() {
	if id := b.activeAccountID(); id != "" {
		if acc := b.account(id); acc != nil && acc.app != nil {
			acc.app.raiseMainWindow()
			return
		}
	}
	if len(b.accountsView()) > 0 {
		// 有账号但窗口都关着：把第一个打开，别让人点了图标什么都没发生。
		b.raiseAccountWindow(b.accountsView()[0].ID)
		return
	}
	b.showPanel()
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

// trayMenu 建右键菜单：每个账号一行（带未读数），然后是账号管理与退出。
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
				ID:    trayAccountIDPrefix + id,
				Label: label,
				Icon:  trayAccountMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { b.raiseAccountWindow(id) },
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
