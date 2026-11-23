package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 账号面板（「账号」窗口）
//
// 多账号的总入口：列出本机登录过的每个账号、它有几条未读、窗口开没开，
// 以及添加 / 打开 / 重新登录 / 退出这几个动作。每个账号有自己的主窗口，
// 关掉窗口只是把它挂回后台（见 accounts.go），账号面板是回到它的地方。
//
// 整套界面走 Lobe UI：颜色取 TokensOf(c)，行 / 标签 / 按钮 / 提示分别由
// layout.Block、display.Tag、action.Button 与 feedback.Alert 提供，和主窗口
// 及各附属窗口用的是同一套设计语言。文案与动作保持不变，只换观感。

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
		Content:           ui.View(b.windowView(b.panelView)),
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

// windowView 是账号面板与登录窗口 view 的入口：记下这一帧属于哪扇窗口，
// 面板头像加载完成时只重绘这一扇（见 accountBook.imageFetcher）。
func (b *accountBook) windowView(fn func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		b.images.beginFrame(c.Services())
		defer b.images.endFrame()
		fn(c)
	}
}

/* ------------------------------------------------------------------
   面板界面
------------------------------------------------------------------ */

// panelView 是账号面板的 view。它只读账号册拍下来的快照，不碰任何窗口对象。
func (b *accountBook) panelView(c *ui.Context) {
	// 主题与主窗口同一份：面板也要跟着「跟随系统 / 浅色 / 深色」变。
	// theme.theme 返回的就是 lobeCfg 派生的主题，后面的 TokensOf(c) 因而
	// 命中的是应用自己的品牌 token，不是 Lobe 默认值。
	c.SetTheme(b.theme.theme(c.Theme().Dark))
	tk := lobetheme.TokensOf(c)
	c.Root().Background(tk.BgLayout)
	// Esc 收起窗口：随时可以再从侧边栏的「账号」打开。
	c.OnShortcut(0, ui.KeyEscape, b.closePanelWindow)

	accounts := b.accountsView()
	pending := b.pendingLogin()

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		// 图标换成 Lucide 的「用户组」，文字走 display.Text，配色走 token。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.Users()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "账号", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				display.Text(c, b.summary(), display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := b.panelWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", b.closePanelWindow)
		})
		layout.Divider(c, layout.DividerProps{})

		// 账号列表可滚动：账号多了不会被底部说明挤掉。ScrollArea 用 Lobe
		// 的细滚动条与边缘渐隐，不再用 mygo 原生 Scroll。
		list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			ui.Column(c).FillWidth().Padding(12, 14).Gap(8).Children(func() {
				if len(accounts) == 0 {
					feedback.Alert(c, "还没有登录账号", feedback.AlertProps{
						Type: feedback.Info, Variant: feedback.Soft,
						Description: "点下面的「添加账号」在浏览器里授权，回来后这里会出现它的窗口；" +
							"可以挂多个账号，各自登录、各自收通知。",
					})
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
		list.Grow(1).MinHeight(0)

		layout.Divider(c, layout.DividerProps{})
		b.panelFooter(c, pending)
	})

	// 退出账号前问一句：它会删掉本机保存的令牌，需要重新授权才能回来。
	b.panelLogoutDialog(c)
}

// panelAccountRow 是面板里的一行：头像、昵称、未读与「打开 / 退出」两个动作。
// 点这一行（头像与昵称那一块）就是打开它的窗口。行本体是一张可点的 Lobe
// Block：描边卡片、圆角与悬停底色都来自主题，窗口开着时用主色描边标出。
func (b *accountBook) panelAccountRow(c *ui.Context, acc accountView) {
	tk := lobetheme.TokensOf(c)
	row := layout.Block(c, layout.BlockProps{Clickable: true, Variant: layout.BlockOutlined}).
		FillWidth().Row().AlignItems(ui.Center).Padding(9, 10)
	if acc.Open {
		row.Border(1, tk.Primary)
	}
	if row.Hovered() {
		row.Background(tk.FillTertiary)
	}
	row.Children(func() {
		// 头像：有缓存位图就用 Lobe Avatar 显示，没缓存过时它按昵称画
		// 一个带纹理的占位，和 Lobe 里的用户头像一致。
		var image ui.ImageSource
		if bmp := b.avatarBitmap(acc.Avatar); bmp != nil {
			image = bmp
		}
		display.Avatar(c, acc.Name, display.AvatarProps{
			Size: 34, Shape: display.AvatarCircle, Image: image,
		})

		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if acc.Unread > 0 {
					// 未读数贴在昵称右上角。把昵称放在 Badge 的 content 里，
					// 右留一点空隙，角标就不会压住旁边的「已打开」标签。
					feedback.Badge(c, feedback.BadgeProps{
						Count: acc.Unread, Color: tk.Primary, Small: true,
					}, func() {
						ui.Row(c).PaddingEnd(6).Children(func() {
							display.Text(c, acc.Name, display.TextProps{
								Strong: true, FontSize: 13, Rows: 1,
							})
						})
					})
				} else {
					display.Text(c, acc.Name, display.TextProps{
						Strong: true, FontSize: 13, Rows: 1,
					})
				}
				if acc.Open {
					// 窗口开着：这个标签和任务栏里的那一个窗口对应得上。
					display.Tag(c, "已打开", display.TagProps{
						Size: lobetheme.Small, Color: display.TagBlue,
						Variant: display.TagFilled, Round: true,
					})
				}
			})
			hint := display.TextSecondary
			if acc.NeedsLogin {
				hint = display.TextDanger
			}
			display.Text(c, accountRowHint(acc), display.TextProps{
				Type: hint, FontSize: 11, Rows: 1,
			})
		})

		// 右侧动作：登录失效时先给「重新登录」，否则是「打开」。
		// 这一处并排几个动作含义要靠文字区分，所以保留文字按钮。
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			if acc.NeedsLogin {
				if action.Button(c, "重新登录", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Primary,
				}).Clicked() {
					b.reloginAccount(acc.ID)
				}
			} else if action.Button(c, "打开", action.ButtonProps{
				Size: lobetheme.Small,
			}).Clicked() {
				b.openWindow(acc.ID)
			}
			if action.Button(c, "退出", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Text,
			}).Clicked() {
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

// panelFooter 是面板底部：添加账号与正在进行的授权。
func (b *accountBook) panelFooter(c *ui.Context, pending *accountLogin) {
	ui.Column(c).FillWidth().Padding(12, 14).Gap(8).Children(func() {
		if pending != nil {
			b.panelPendingLogin(c, pending)
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "添加账号", action.ButtonProps{
				Variant: action.Primary,
			}).Clicked() {
				b.startAddAccount()
			}
			// 注册是网页流程，和侧边栏 / 托盘共用同一个带邀请码的地址。
			if action.Button(c, "注册", action.ButtonProps{}).Clicked() {
				b.openSignup()
			}
			if b.guestApp() == nil && b.accountCount() > 0 {
				if action.Button(c, "未登录窗口", action.ButtonProps{}).Clicked() {
					b.openGuestWindow()
				}
			}
		})
		if msg := b.loginStatus(); msg != "" {
			display.Text(c, msg, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 3,
			})
		}
	})
}

// openSignup 打开带邀请码的 2Libra 注册页：signupURL 已经把 aff 写在地址里，
// 账号面板和侧边栏、托盘走的是同一个入口，注册仍由系统浏览器完成。
func (b *accountBook) openSignup() {
	if err := b.openURL(signupURL); err != nil {
		b.setLoginMsg("打开注册页失败：" + err.Error())
	}
}

// panelPendingLogin 是「正在等授权」那一段。内置浏览器授权时给出「打开授权
// 窗口 / 改用系统浏览器 / 取消」；系统浏览器授权时给出粘贴回调链接的兜底入口
// （浏览器没自动跳回时用）。两处都留着取消。
func (b *accountBook) panelPendingLogin(c *ui.Context, pending *accountLogin) {
	if pending.embedded {
		desc := "授权页开在独立的浏览器窗口里，它不会用到系统浏览器里的登录状态，" +
			"所以可以直接登录要挂的另一个账号。同意授权后那个窗口会自动收起。"
		if pending.accountID != "" {
			name := "这个账号"
			if acc := b.account(pending.accountID); acc != nil {
				name = "「" + acc.name() + "」"
			}
			desc = "授权页开在独立的浏览器窗口里，它不会用到系统浏览器里的登录状态，" +
				"请在里面登录要重新授权的" + name + "账号。同意授权后那个窗口会自动收起。"
		}
		feedback.Alert(c, "在内置浏览器里完成授权", feedback.AlertProps{
			Type: feedback.Info, Variant: feedback.Soft, Description: desc,
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "打开授权窗口", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Primary,
			}).Clicked() {
				b.showLoginWindow()
			}
			if action.Button(c, "改用系统浏览器", action.ButtonProps{
				Size: lobetheme.Small,
			}).Clicked() {
				b.useSystemBrowserForLogin()
			}
			ui.Spacer(c).Grow(1)
			if action.Button(c, "取消授权", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Text,
			}).Clicked() {
				b.cancelLogin()
			}
		})
		if msg := b.loginStatus(); msg != "" {
			display.Text(c, msg, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 3,
			})
		}
		return
	}
	feedback.Alert(c, "等待浏览器授权…", feedback.AlertProps{
		Type: feedback.Info, Variant: feedback.Soft,
	})
	if warn := b.schemeWarning(); warn != "" {
		feedback.Alert(c, warn, feedback.AlertProps{
			Type: feedback.Error, Variant: feedback.Soft,
		})
	}
	display.Text(c, "同意后浏览器会自动跳回本应用。如果浏览器没有跳回，"+
		"把地址栏里的完整链接粘贴到下面，一样能完成这次授权。",
		display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
	display.Text(c, "回调链接", display.TextProps{
		Strong: true, FontSize: 11, Rows: 1,
	})
	field.Input(c, &pending.paste, field.InputProps{
		Placeholder: defaultRedirectURI + "?code=...&state=...",
		Size:        lobetheme.Small,
	})
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if action.Button(c, "用回调链接完成", action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Primary,
		}).Clicked() {
			if !b.completeCallback(pending.paste) {
				b.setLoginMsg("回调链接无法完成这次授权")
			}
		}
		if action.Button(c, "取消授权", action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Text,
		}).Clicked() {
			b.cancelLogin()
		}
	})
	if msg := b.loginStatus(); msg != "" {
		display.Text(c, msg, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 3,
		})
	}
}

// panelLogoutDialog 是「退出这个账号」的确认框：退出会删掉本机保存的令牌，
// 下次要重新授权，所以先问一句。Modal 本身就是 Lobe 组件，文案照旧。
func (b *accountBook) panelLogoutDialog(c *ui.Context) {
	if b.confirmOut == "" {
		return
	}
	id := b.confirmOut
	name := "这个账号"
	if acc := b.account(id); acc != nil {
		name = acc.name()
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "退出账号？",
		Width:      420,
		OkText:     "确认退出",
		CancelText: "取消",
	}, func() {
		display.Text(c, "退出「"+name+"」会关掉它的窗口，并删掉本机保存的令牌；"+
			"下次要用它需要重新在浏览器里授权。别的账号不受影响。",
			display.TextProps{Type: display.TextSecondary, Rows: 3})
	})
	// 取消、✕、Esc 与点窗外都只是放弃退出。
	if !open {
		b.confirmOut = ""
		return
	}
	if ok {
		b.confirmOut = ""
		b.logoutAccount(id)
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
