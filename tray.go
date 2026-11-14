package main

import (
	_ "embed"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 系统托盘：Windows 是任务栏通知区域，macOS 是菜单栏。左键点图标把主窗口
// 带回前台；右键弹出菜单，最上面是账号状态与登录 / 退出登录，下面是通知、
// 发帖两个窗口入口，最后是退出应用。
//
// 托盘只在应用就绪后创建（见 main 里的 WhenReady），之后只在主线程读写。
// 菜单不常驻：每次右键现建一份，登录状态、用户名因此不会落后于应用内的
// 状态；菜单挂着时左键也会弹出菜单，所以弹出后立刻用 SetMenu(nil) 摘掉。

//go:embed resources/tray.png
var trayIconPNG []byte

const (
	// trayID 让 Windows 记住用户把图标放在任务栏上还是折叠区里。
	trayID      = "main"
	trayToolTip = "T2 Desktop"

	trayAccountID       = "tray-account"
	trayLoginID         = "tray-login"
	traySignupID        = "tray-signup"
	trayLogoutID        = "tray-logout"
	trayNotificationsID = "tray-notifications"
	trayComposeID       = "tray-compose"
	trayQuitID          = "tray-quit"
)

// trayMenuIcons 是托盘菜单各项的图标：把现有的 *ui.SVG 渲染成菜单图标。全部
// 按 template 渲染（只取形状，跟随菜单文字颜色，深色菜单里自动变白），和侧边栏
// 入口保持同一套线性图标。mygo 要求图标只生成一次，因此集中在 init 里建好复用。
var (
	trayLoginMI   *mygo.MenuIcon
	traySignupMI  *mygo.MenuIcon
	trayBellMI    *mygo.MenuIcon
	trayComposeMI *mygo.MenuIcon
	trayQuitMI    *mygo.MenuIcon
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
	trayBellMI = makeMI(bellIcon)
	trayComposeMI = makeMI(composeIcon)
	trayQuitMI = makeMI(closeIcon)
}

// startTray 放上托盘图标。必须在应用就绪后、主线程调用：图标与菜单都是
// 原生资源，早于就绪创建会 panic。创建失败只记日志：托盘只是入口的捷径，
// 窗口、通知与发帖照常可用。
func (a *app) startTray() {
	if a.tray != nil {
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
	a.tray = tray
	// 记下托盘真的建出来了：只有这时「关闭主窗口收进托盘」才敢藏窗口。
	a.trayReady = true
	// 平时不挂菜单：有菜单时左键会弹菜单，而不是回到窗口。右键才临时挂上
	// 一份，见 showTrayMenu。
	tray.OnClick(a.showMainWindow)
	tray.OnRightClick(a.showTrayMenu)
	// 退出前摘掉图标，否则通知区域会留下一块点不动的残影，直到鼠标划过。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.destroyTray() })
}

// showTrayMenu 是右键菜单的入口：现建一份反映当前账号状态的菜单，弹出，
// 返回后立刻摘掉。PopUpMenu 在菜单关闭前不返回，所以摘掉时菜单早已收起。
func (a *app) showTrayMenu() {
	tray := a.tray
	if tray == nil {
		return
	}
	tray.SetMenu(a.trayMenu())
	tray.PopUpMenu()
	tray.SetMenu(nil)
}

// destroyTray 摘掉托盘图标。退出流程里调用；重复调用无害。
func (a *app) destroyTray() {
	tray := a.tray
	if tray == nil {
		return
	}
	a.tray = nil
	a.trayReady = false
	tray.Destroy()
}

// trayMenu 建右键菜单：账号状态与登录 / 退出登录，通知，发帖，退出应用。
func (a *app) trayMenu() *mygo.Menu {
	items := []*mygo.MenuItem{a.trayAccountItem()}
	if a.auth.loggedIn() {
		items = append(items, &mygo.MenuItem{
			ID:    trayLogoutID,
			Label: "退出登录",
			Icon:  trayLoginMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.auth.Logout() },
		})
	} else {
		items = append(items, &mygo.MenuItem{
			ID:    trayLoginID,
			Label: "登录",
			Icon:  trayLoginMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.startLogin() },
		})
		items = append(items, &mygo.MenuItem{
			ID:    traySignupID,
			Label: "注册",
			Icon:  traySignupMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.openSignup() },
		})
	}
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayNotificationsID,
			Label: "通知",
			Icon:  trayBellMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.showNotifications() },
		},
		&mygo.MenuItem{
			ID:    trayComposeID,
			Label: "发帖",
			Icon:  trayComposeMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.showCompose() },
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayQuitID,
			Label: "退出",
			Icon:  trayQuitMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { a.quitApp() },
		},
	)
	return mygo.NewMenu(items)
}

// trayAccountItem 是菜单顶部那行账号状态：登录后是「已登录：用户名」，未
// 登录是「未登录」。它只显示状态，不响应点击。
func (a *app) trayAccountItem() *mygo.MenuItem {
	label := "未登录"
	if a.auth.loggedIn() {
		label = "已登录"
		if name := a.auth.username(); name != "" {
			label += "：" + name
		}
	}
	return &mygo.MenuItem{ID: trayAccountID, Label: label, Disabled: true}
}
