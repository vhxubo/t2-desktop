package main

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 账号面板（「账号」窗口）
//
// 多账号的总入口：列出本机登录过的每个账号、它有几条未读、窗口开没开，
// 以及添加 / 打开 / 重新登录 / 退出这几个动作。每个账号有自己的主窗口，
// 关掉窗口只是把它挂回后台（见 accounts.go），账号面板是回到它的地方。

const (
	accountsWindowWidth  = 460
	accountsWindowHeight = 520
	accountsWindowMinW   = 380
	accountsWindowMinH   = 320
)

/* ------------------------------------------------------------------
   窗口：入口、显示与关闭
------------------------------------------------------------------ */

// showPanel 是账号面板的全局入口：任何窗口、任何 goroutine 都能调用。窗口
// 已经开着就只把它带到前台，不会开出第二个。建窗口排到下一轮主线程处理
// （见 accountBook.postMain）。
func (b *accountBook) showPanel() {
	b.postMain(b.openPanel)
}

// showPanelWindow 在主线程上真正打开账号面板。窗口是一份原生资源，只有应用
// 就绪后、在主线程才能建；无头测试直接渲染 panelView。
func (b *accountBook) showPanelWindow() {
	if !b.afterReady {
		return
	}
	if win := b.panelWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "账号",
		Width:             accountsWindowWidth,
		Height:            accountsWindowHeight,
		MinWidth:          accountsWindowMinW,
		MinHeight:         accountsWindowMinH,
		StateKey:          "accounts",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(b.panelView),
	})
	b.panelWin = win
	win.OnClosed(func() {
		b.panelWin = nil
	})
}

// closePanelWindow 关掉账号面板。只在主线程调用。
func (b *accountBook) closePanelWindow() {
	win := b.panelWin
	if win == nil {
		return
	}
	b.panelWin = nil
	win.Close()
}

// redrawPanel 让账号面板按最新状态重建。可以从任意线程调用（mygo 的
// Invalidate 是安全的），窗口没开时什么都不做。
func (b *accountBook) redrawPanel() {
	if win := b.panelWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   面板界面
------------------------------------------------------------------ */

// panelView 是账号面板的 view。它只读账号册拍下来的快照，不碰任何窗口对象。
func (b *accountBook) panelView(c *ui.Context) {
	// 主题与主窗口同一份：面板也要跟着「跟随系统 / 浅色 / 深色」变。
	c.SetTheme(b.theme.theme(c.Theme().Dark))
	t := c.Theme()
	c.Root().Background(t.Background)
	// Esc 收起窗口：随时可以再从侧边栏的「账号」打开。
	c.OnShortcut(0, ui.KeyEscape, b.closePanelWindow)

	accounts := b.accountsView()
	pending := b.pendingLogin()

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, accountsIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "账号").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				ui.Text(c, b.summary()).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := b.panelWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", b.closePanelWindow)
		})
		ui.Divider(c)

		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(12, 14).Gap(8).Children(func() {
				if len(accounts) == 0 {
					ui.Text(c, "还没有登录账号。点下面的「添加账号」在浏览器里授权，"+
						"回来后这里会出现它的窗口；可以挂多个账号，各自登录、各自收通知。").
						FontSize(12).TextColor(t.TextMuted).MaxLines(4)
				}
				for _, acc := range accounts {
					// 头像在本机缓存里就直接画；没缓存过的这里顺带取一次。
					if b.afterReady {
						b.prefetchAvatar(acc.Avatar)
					}
					b.panelAccountRow(c, acc)
				}
			})
		})

		ui.Divider(c)
		b.panelFooter(c, pending)
	})

	// 退出账号前问一句：它会删掉本机保存的令牌，需要重新授权才能回来。
	b.panelLogoutDialog(c)
}

// panelAccountRow 是面板里的一行：头像、昵称、未读与「打开 / 退出」两个动作。
// 点这一行（头像与昵称那一块）就是打开它的窗口。
func (b *accountBook) panelAccountRow(c *ui.Context, acc accountView) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(8, 10).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if acc.Open {
		row.Border(1, t.Accent)
	}
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		avatarView(c, acc.Name, b.avatarBitmap(acc.Avatar), 34)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, acc.Name).FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if acc.Open {
					// 窗口开着：这个角标和任务栏里的那一个窗口对应得上。
					ui.Text(c, "已打开").FontSize(10).TextColor(t.Accent).Shrink(0)
				}
				if acc.Unread > 0 {
					badge := ui.Box(c).Padding(1, 6).Radius(9).Shrink(0).Background(t.Accent)
					badge.Children(func() {
						ui.Textf(c, "%d", acc.Unread).FontSize(10).Bold().
							TextColor(ui.RGB(255, 255, 255)).SingleLine()
					})
					badge.Label(fmt.Sprintf("%d 条未读", acc.Unread))
				}
			})
			ui.Text(c, accountRowHint(acc)).FontSize(11).
				TextColor(accountRowHintColor(t, acc)).SingleLine()
		})
		// 右侧动作：登录失效时先给「重新登录」，否则是「打开」。
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			if acc.NeedsLogin {
				if ui.PrimaryButton(c, "重新登录").Clicked() {
					b.reloginAccount(acc.ID)
				}
			} else if ui.Button(c, "打开").Clicked() {
				b.openWindow(acc.ID)
			}
			if ui.Button(c, "退出").Clicked() {
				b.confirmOut = acc.ID
			}
		})
	})
	row.Label(acc.Name).Tooltip("打开 " + acc.Name + " 的窗口")
	if !acc.NeedsLogin {
		row.OnClick(func() { b.openWindow(acc.ID) })
	}
}

// accountRowHint 是账号行下面那行小字：登录是否还有效、窗口开没开。未读数
// 已经作为角标画在昵称旁边，这里不再重复一遍数字。
func accountRowHint(acc accountView) string {
	switch {
	case acc.NeedsLogin:
		return "登录已失效，需要重新授权"
	case acc.Open:
		return "窗口已打开"
	default:
		return "已挂在后台，点开即可使用"
	}
}

func accountRowHintColor(t *ui.Theme, acc accountView) ui.Color {
	if acc.NeedsLogin {
		return t.Danger
	}
	return t.TextMuted
}

// panelFooter 是面板底部：添加账号、正在进行的授权与说明文字。
func (b *accountBook) panelFooter(c *ui.Context, pending *accountLogin) {
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(12, 14).Gap(8).Children(func() {
		if pending != nil {
			b.panelPendingLogin(c, pending)
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "添加账号").Clicked() {
				b.startAddAccount()
			}
			if b.guestApp() == nil && b.accountCount() > 0 {
				if ui.Button(c, "未登录窗口").Clicked() {
					b.openGuestWindow()
				}
			}
			ui.Spacer(c).Grow(1)
		})
		if msg := b.loginStatus(); msg != "" {
			ui.Text(c, msg).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		}
		ui.Text(c, "每个账号一个窗口，关掉窗口它就挂在后台：未读角标与登录状态照样更新。"+
			"退出账号会删掉本机保存的令牌，下次要重新授权。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	})
}

// panelPendingLogin 是「正在等授权」那一段。内置浏览器授权时给出「打开授权
// 窗口 / 改用系统浏览器 / 取消」；系统浏览器授权时给出粘贴回调链接的兜底入口
// （浏览器没自动跳回时用）。两处都留着取消。
func (b *accountBook) panelPendingLogin(c *ui.Context, pending *accountLogin) {
	t := c.Theme()
	if pending.embedded {
		ui.Text(c, "在内置浏览器里完成授权").FontSize(12).Bold().TextColor(t.Text)
		if pending.accountID != "" {
			name := "这个账号"
			if acc := b.account(pending.accountID); acc != nil {
				name = "「" + acc.name() + "」"
			}
			ui.Text(c, "授权页开在独立的浏览器窗口里，它不会用到系统浏览器里的登录状态，"+
				"请在里面登录要重新授权的"+name+"账号。同意授权后那个窗口会自动收起。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		} else {
			ui.Text(c, "授权页开在独立的浏览器窗口里，它不会用到系统浏览器里的登录状态，"+
				"所以可以直接登录要挂的另一个账号。同意授权后那个窗口会自动收起。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "打开授权窗口").Clicked() {
				b.showLoginWindow()
			}
			if ui.Button(c, "改用系统浏览器").Clicked() {
				b.useSystemBrowserForLogin()
			}
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "取消授权").Clicked() {
				b.cancelLogin()
			}
		})
		if msg := b.loginStatus(); msg != "" {
			ui.Text(c, msg).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		}
		return
	}
	ui.Text(c, "等待浏览器授权…").FontSize(12).Bold().TextColor(t.Text)
	if warn := b.schemeWarning(); warn != "" {
		ui.Text(c, warn).FontSize(11).TextColor(t.Danger).MaxLines(3)
	}
	ui.Text(c, "同意后浏览器会自动跳回本应用。如果浏览器没有跳回，"+
		"把地址栏里的完整链接粘贴到下面，一样能完成这次授权。").
		FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	ui.TextInput(c, &pending.paste).Label("回调链接").
		Placeholder(defaultRedirectURI + "?code=...&state=...").FillWidth()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c, "用回调链接完成").Clicked() {
			if !b.completeCallback(pending.paste) {
				b.setLoginMsg("回调链接无法完成这次授权")
			}
		}
		if ui.Button(c, "取消授权").Clicked() {
			b.cancelLogin()
		}
		ui.Spacer(c).Grow(1)
	})
	if msg := b.loginStatus(); msg != "" {
		ui.Text(c, msg).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	}
}

// panelLogoutDialog 是「退出这个账号」的确认框：退出会删掉本机保存的令牌，
// 下次要重新授权，所以先问一句。
func (b *accountBook) panelLogoutDialog(c *ui.Context) {
	if b.confirmOut == "" {
		return
	}
	t := c.Theme()
	id := b.confirmOut
	name := "这个账号"
	if acc := b.account(id); acc != nil {
		name = acc.name()
	}
	open := true
	dismissed := ui.Modal(c, &open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "退出账号？").FontSize(16).Bold()
			ui.Text(c, "退出「"+name+"」会关掉它的窗口，并删掉本机保存的令牌；"+
				"下次要用它需要重新在浏览器里授权。别的账号不受影响。").
				TextColor(t.TextMuted).MaxLines(3)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					b.confirmOut = ""
				}
				if ui.PrimaryButton(c, "确认退出").Clicked() {
					b.confirmOut = ""
					b.logoutAccount(id)
				}
			})
		})
	})
	if dismissed.Dismissed() {
		b.confirmOut = ""
	}
}

/* ------------------------------------------------------------------
   账号头像：面板不是 app，但它和窗口共用同一份图片缓存
------------------------------------------------------------------ */

// avatarBitmap 取一个账号头像的位图。没加载过时返回 nil，调用方画昵称
// 首字母（见 avatarView）。
func (b *accountBook) avatarBitmap(raw string) *ui.Bitmap {
	raw = normalizeAvatarURL(raw)
	if raw == "" {
		return nil
	}
	return b.images.bitmap(raw)
}

// prefetchAvatar 让账号头像进本机缓存：面板没有自己的 app，借用同一份图片
// 缓存与同一个取图出口（头像属于长期缓存，下载一次就留着）。
func (b *accountBook) prefetchAvatar(raw string) {
	raw = normalizeAvatarURL(raw)
	if raw == "" {
		return
	}
	b.images.prefetch(raw, imageCacheStable, b.imageFetcher())
}

// imageFetcher 是账号面板取图与重绘的那一份实现。
func (b *accountBook) imageFetcher() imageFetcher {
	return imageFetcher{fetch: b.fetchImage, redraw: b.redrawPanel}
}

// fetchImage 取一张图片的原始字节：和窗口里的图片一样走配置的代理。
func (b *accountBook) fetchImage(raw string) ([]byte, error) {
	return fetchImageBytes(b.imgHTTP, raw)
}
