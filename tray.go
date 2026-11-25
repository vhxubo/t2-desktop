package main

import (
	_ "embed"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// 系统托盘：Windows 是任务栏通知区域，macOS 是菜单栏。左键点图标把主窗口
// 带回前台；右键弹出菜单。未登录时提供登录、注册；登录后顶部显示当前账号，
// 下面接发帖、发动态、设置。退出登录不放在托盘里，去「我的」或命令中心。
// 两种状态都有「关闭全部」（收进托盘，应用继续在后台跑）与「退出」。
//
// 托盘是进程级资源：菜单每次右键现建一份，弹出的菜单挂着时左键也会弹出
// 菜单，所以弹出后立刻用 SetMenu(nil) 摘掉。
//
// 只在应用就绪后创建（见 main 的 WhenReady），之后只在主线程读写。

//go:embed resources/tray.png
var trayIconPNG []byte

const (
	// trayID 让 Windows 记住用户把图标放在任务栏上还是折叠区里。
	trayID      = "main"
	trayToolTip = "T2 Desktop"

	trayLoginID    = "tray-login"
	traySignupID   = "tray-signup"
	trayPostID     = "tray-post"
	trayMomentID   = "tray-moment"
	traySettingsID = "tray-settings"
	trayCloseAllID = "tray-close-all"
	trayQuitID     = "tray-quit"
)

// trayMenuIcons 是托盘菜单各项的图标：把现有的 *ui.SVG 渲染成菜单图标。全部
// 按 template 渲染（只取形状，跟随菜单文字颜色，深色菜单里自动变白），和侧边栏
// 入口保持同一套线性图标。mygo 要求图标只生成一次，因此集中在 init 里建好复用。
var (
	trayLoginMI    *mygo.MenuIcon
	traySignupMI   *mygo.MenuIcon
	trayPostMI     *mygo.MenuIcon
	trayMomentMI   *mygo.MenuIcon
	traySettingsMI *mygo.MenuIcon
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
	// 与命令中心里的发帖 / 发动态 / 设置用同一组图标。
	trayPostMI = makeMI(lucide.SquarePen())
	trayMomentMI = makeMI(lucide.PenLine())
	traySettingsMI = makeMI(lucide.Settings())
	trayCloseAllMI = makeMI(collapseIcon)
	trayQuitMI = makeMI(closeIcon)
}

// startTray 放上托盘图标。必须在应用就绪后、主线程调用：图标与菜单都是
// 原生资源，早于就绪创建会 panic。创建失败只记日志：托盘只是入口的捷径，
// 窗口、通知照常可用。
func (d *desktop) startTray() {
	if d.tray != nil {
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
	d.tray = tray
	// 记下托盘真的建出来了：只有这时「关闭窗口收进托盘」才敢藏窗口。
	d.trayReady = true
	// 平时不挂菜单：有菜单时左键会弹菜单，而不是回到窗口。右键才临时挂上
	// 一份，见 showTrayMenu。
	tray.OnClick(d.showMainWindowFromTray)
	tray.OnRightClick(d.showTrayMenu)
	// 退出前摘掉图标，否则通知区域会留下一块点不动的残影，直到鼠标划过。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { d.destroyTray() })
}

// showMainWindowFromTray 是左键单击：把主窗口带回前台；窗口没开就开一扇。
func (d *desktop) showMainWindowFromTray() {
	a := d.app
	if a == nil {
		return
	}
	if !a.mainClosed && a.win != nil {
		a.raiseMainWindow()
		return
	}
	a.openMainWindow()
}

// showTrayMenu 是右键菜单的入口：现建一份反映当前登录状态的菜单，弹出，
// 返回后立刻摘掉。PopUpMenu 在菜单关闭前不返回，所以摘掉时菜单早已收起。
func (d *desktop) showTrayMenu() {
	tray := d.tray
	if tray == nil {
		return
	}
	tray.SetMenu(d.trayMenu())
	tray.PopUpMenu()
	tray.SetMenu(nil)
}

// destroyTray 摘掉托盘图标。退出流程里调用；重复调用无害。
func (d *desktop) destroyTray() {
	tray := d.tray
	if tray == nil {
		return
	}
	d.tray = nil
	d.trayReady = false
	tray.Destroy()
}

// trayMenu 建右键菜单。顶部一行只读的账号状态：未登录提示「未登录」，登录后
// 露出当前账号；再按状态给登录 / 注册或发帖 / 发动态 / 设置。关闭全部与退出
// 是窗口管理动作，两种状态都在。
func (d *desktop) trayMenu() *mygo.Menu {
	a := d.app
	var auth *AuthManager
	if a != nil {
		auth = a.auth
	}
	items := []*mygo.MenuItem{
		{ID: "tray-state", Label: trayStateLabel(auth), Disabled: true},
	}
	if auth != nil && auth.loggedIn() {
		items = append(items,
			&mygo.MenuItem{
				ID:    trayPostID,
				Label: "发帖",
				Icon:  trayPostMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { d.trayOpenApp(a.showCompose) },
			},
			&mygo.MenuItem{
				ID:    trayMomentID,
				Label: "发动态",
				Icon:  trayMomentMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { d.trayOpenApp(a.showMomentCompose) },
			},
			&mygo.MenuItem{
				ID:    traySettingsID,
				Label: "设置",
				Icon:  traySettingsMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { d.trayOpenApp(a.showSettings) },
			},
		)
	} else {
		items = append(items,
			&mygo.MenuItem{
				ID:    trayLoginID,
				Label: "登录",
				Icon:  trayLoginMI,
				Click: func(*mygo.MenuItem, *mygo.Window) {
					if d.app != nil {
						d.app.startLogin()
					}
				},
			},
			&mygo.MenuItem{
				ID:    traySignupID,
				Label: "注册",
				Icon:  traySignupMI,
				Click: func(*mygo.MenuItem, *mygo.Window) { _ = d.openURL(signupURL) },
			},
		)
	}
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayCloseAllID,
			Label: "关闭全部",
			Icon:  trayCloseAllMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { d.closeAllWindows() },
		},
		mygo.Separator(),
		&mygo.MenuItem{
			ID:    trayQuitID,
			Label: "退出",
			Icon:  trayQuitMI,
			Click: func(*mygo.MenuItem, *mygo.Window) { d.quitApp() },
		},
	)
	return mygo.NewMenu(items)
}

// trayOpenApp 从托盘打开功能窗口。发帖与设置窗口都以主窗口作 UI 队列出口；
// 窗口收进托盘或关掉后 a.win 可能为空，直接调用会被跳过，先把主窗口带回来。
func (d *desktop) trayOpenApp(open func()) {
	a := d.app
	if a == nil {
		return
	}
	if a.mainClosed || a.win == nil {
		a.openMainWindow()
	} else {
		a.raiseMainWindow()
	}
	open()
}

func trayStateLabel(auth *AuthManager) string {
	if auth == nil || !auth.loggedIn() {
		return "未登录"
	}
	if name := auth.username(); name != "" {
		return "已登录：" + name
	}
	return "已登录"
}
