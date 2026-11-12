package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 「我的」窗口：点击标题栏上登录账号的头像打开。它和通知、设置一样是独立
// 窗口，而不是主窗口里的一层遮罩——四块内容（资产、帖子、回复、权限）都要
// 滚动和分页，塞进主窗口的弹窗里只会互相挤。
//
// 数据、状态与请求在 profile.go；这里只负责窗口的开关与界面。

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showProfile 是「我的」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showProfile() {
	a.publish(a.showProfileWindow)
}

// showProfileWindow 在主线程上真正打开窗口。打开时先按需拉一次资料与余额，
// 首帧里看到的就是当前账号，而不是空壳再闪一下。
func (a *app) showProfileWindow() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看「我的」。", a.showProfile)
		return
	}
	if a.profile == nil {
		a.profile = newProfileState()
	}
	a.ensureProfileLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 profileWindowView。
	if a.win == nil {
		return
	}
	if win := a.profileWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setProfileWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "我的",
		Width:             profileWindowWidth,
		Height:            profileWindowHeight,
		MinWidth:          profileWinMinWidth,
		MinHeight:         profileWinMinHeight,
		StateKey:          "profile",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.profileWindowView),
	})
	a.profileWin = win
	win.OnShow(func() { a.setProfileWindowShown(true) })
	win.OnHide(func() { a.setProfileWindowShown(false) })
	win.OnClosed(func() {
		a.profileWin = nil
		a.setProfileWindowShown(false)
	})
	a.setProfileWindowShown(true)
}

// profileWindowVisible 报告窗口此刻是否在屏幕上：标题栏的头像据此高亮。
func (a *app) profileWindowVisible() bool { return a.profileShown }

// setProfileWindowShown 记下显示状态并让主窗口重画一次。只在主线程调用。
func (a *app) setProfileWindowShown(shown bool) {
	if a.profileShown == shown {
		return
	}
	a.profileShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closeProfileWindow 关掉窗口：退出登录或主窗口关闭时调用，别让上一个账号
// 的资料留在屏幕上。只在主线程调用。
func (a *app) closeProfileWindow() {
	win := a.profileWin
	if win == nil {
		return
	}
	a.profileWin = nil
	a.setProfileWindowShown(false)
	win.Close()
}

// redrawProfileWindow 让窗口按最新状态重建。只在主线程调用。
func (a *app) redrawProfileWindow() {
	if win := a.profileWin; win != nil {
		win.Invalidate()
	}
}

// profileTag 是标题栏右侧的账号标识：头像 + 昵称，整体可点，点开「我的」。
// 只显示昵称，不再写「已登录」：头像和昵称本身就说明身份，那几个字只会挤占
// 标题栏宽度。昵称还没读到时只画头像，仍然是「我的」的入口。
func (a *app) profileTag(c *ui.Context, name string) {
	t := c.Theme()
	tag := ui.Box(c).Padding(2, 5).Radius(6).Background(ui.Transparent)
	if a.profileWindowVisible() || tag.Hovered() {
		tag.Background(t.SurfaceHover)
	}
	tag.Children(func() {
		if name == "" {
			// 昵称还没读到（身份接口慢或失败）：仍然给一个可点的头像位，
			// 「我的」窗口自己会拉资料，不至于没有入口。
			avatarView(c, "", a.avatarBitmap(a.auth.avatarURL()), userAvatarSize(12))
			return
		}
		a.userTag(c, userTagSpec{
			Name:   name,
			Avatar: a.auth.avatarURL(),
			Size:   12,
			Color:  t.TextMuted,
			// 这是自己的账号区，点它打开「我的」；默认的资料卡留给别处的
			// 用户标识。
			Click: a.showProfile,
		})
	})
	tag.Label("我的").Tooltip("打开「我的」窗口").OnClick(a.showProfile)
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// profileWindowView 是「我的」窗口的 view。窗口自带标题栏与窗口按钮，因此
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) profileWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 窗口里的「在站点管理授权」等按钮要把链接交给系统浏览器，服务取本
	// 窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// Esc 收起窗口：随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeProfileWindow)

	ps := a.profile
	if ps == nil {
		return // 没有状态就不画：newApp 与 onAuthChanged 都会建好它
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, profileIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "我的").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if name := a.profileTitleName(ps); name != "" {
					ui.Text(c, name).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.profileWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", a.closeProfileWindow)
		})
		ui.Divider(c)

		a.profileHeader(c, ps)
		ui.Divider(c)

		// 四个标签页：内容各占满剩余高度，自己滚动。
		ui.Row(c).FillWidth().Padding(6, 12).Children(func() {
			ui.Tabs(c, &ps.tab, profileTabLabels...)
		})
		ui.Divider(c)

		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16).Children(func() {
			switch ps.tab {
			case profileTabTopics:
				a.profileTopicsBody(c, ps)
			case profileTabReplies:
				a.profileRepliesBody(c, ps)
			case profileTabAccount:
				a.profileAccountBody(c, ps)
			default:
				a.profileOverviewBody(c, ps)
			}
		})
	})
	a.ensureProfileLoaded()
}

// profileTitleName 是窗口标题栏上的账号名：优先昵称，其次用户名。
func (a *app) profileTitleName(ps *profileState) string {
	if ps.user.DisplayName != "" {
		return ps.user.DisplayName
	}
	if ps.user.Name != "" {
		return ps.user.Name
	}
	return a.auth.username()
}

// profileHeader 是资料头：大头像、昵称、用户 ID 与注册时间，右侧是刷新。
func (a *app) profileHeader(c *ui.Context, ps *profileState) {
	t := c.Theme()
	u := ps.user
	name := a.profileTitleName(ps)
	avatar := u.Avatar
	if avatar == "" {
		avatar = a.auth.avatarURL()
	}
	title := u.DisplayName
	if title == "" {
		title = name
	}
	if title == "" {
		title = "已登录"
	}
	ui.Row(c).FillWidth().Padding(14, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		avatarView(c, name, a.avatarBitmap(avatar), 48)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(16).Bold().SingleLine()
				if u.IsPro {
					ui.Text(c, "PRO").FontSize(10).TextColor(t.Accent)
				}
			})
			line := profileIdentityLine(u)
			if ps.infoErr != "" {
				line = "资料暂时不可用：" + ps.infoErr
			} else if ps.infoLoading && !ps.infoLoaded {
				line = "正在读取账号资料…"
			}
			ui.Text(c, line).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		a.smallAction(c, "刷新", ps.infoLoading || ps.coinLoading, func() { a.reloadProfile() })
	})
}

/* ------------------------------------------------------------------
   Tab 1：概览与资产
---------------------------------------------------------------- */

func (a *app) profileOverviewBody(c *ui.Context, ps *profileState) {
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			a.profileBalanceCard(c, ps)
			a.profileTxSection(c, ps)
			a.profileStatsSection(c, ps)
			a.profileAuthEntry(c, ps)
		})
	})
}

// profileBalanceCard 是余额 + 签到：两列并排，和金币页顶部同一个口径。
func (a *app) profileBalanceCard(c *ui.Context, ps *profileState) {
	t := c.Theme()
	ui.Box(c).FillWidth().Padding(12, 14).Radius(10).
		Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "金币余额").FontSize(11).TextColor(t.TextMuted)
				switch {
				case ps.coinLoading && !ps.balanceOK:
					ui.Spinner(c).Size(20, 20)
				case ps.balanceOK:
					ui.Text(c, coinThousands(ps.balance)+" 金币").FontSize(24).Bold().SingleLine()
				default:
					ui.Text(c, "—").FontSize(24).Bold().TextColor(t.TextMuted)
				}
				if ps.coinErr != "" {
					ui.Text(c, ps.coinErr).FontSize(10).TextColor(t.Danger).MaxLines(2)
				} else {
					ui.Text(c, "需授权 read:economy").FontSize(10).TextColor(t.TextMuted).SingleLine()
				}
			})
			ui.Column(c).Shrink(0).Gap(2).Children(func() {
				ui.Text(c, "今日签到").FontSize(11).TextColor(t.TextMuted)
				text := profileSignText(ps.sign, ps.coinErr)
				line := ui.Text(c, text).FontSize(12).Bold().SingleLine()
				if ps.sign.Known && ps.sign.Signed && ps.coinErr == "" {
					line.TextColor(t.Success)
				} else {
					line.TextColor(t.TextMuted)
				}
				if ps.sign.Known && ps.sign.Coins != 0 {
					ui.Text(c, "今日 +"+coinAmountText(ps.sign.Coins)).FontSize(10).TextColor(t.TextMuted)
				}
			})
		})
	})
}

// profileTxSection 是金币明细的快捷入口：默认收起，展开后列出最近几条变动
// （签到收益、发帖消耗、打赏等），再给一个到站点看全部流水的出口。
func (a *app) profileTxSection(c *ui.Context, ps *profileState) {
	t := c.Theme()
	settingsSection(c, "金币明细", func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			label := "展开最近记录"
			if ps.txOpen {
				label = "收起"
			}
			a.smallAction(c, label, ps.txLoading && !ps.txLoaded, func() {
				ps.txOpen = !ps.txOpen
				if ps.txOpen && !ps.txLoaded && !ps.txLoading {
					a.loadProfileTransactions()
				}
			})
			ui.Spacer(c).Grow(1)
			a.smallAction(c, "全部流水", false, func() {
				a.gotoTab(coinTabIndex())
				a.closeProfileWindow()
			})
		})
		if !ps.txOpen {
			return
		}
		switch {
		case ps.txLoading && len(ps.txItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, "正在加载金币记录…").FontSize(11).TextColor(t.TextMuted)
			})
		case ps.txErr != "":
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, ps.txErr).FontSize(11).TextColor(t.Danger).MaxLines(2).Grow(1)
				a.smallAction(c, "重试", false, func() { a.loadProfileTransactions() })
			})
		case len(ps.txItems) == 0:
			ui.Text(c, "最近没有金币变动。").FontSize(11).TextColor(t.TextMuted)
		default:
			for _, tx := range ps.txItems {
				a.profileTxRow(c, tx)
			}
		}
	})
}

// profileTxRow 是一行金币变动：原因、时间与带符号的金额。点有相关帖子的
// 记录会回到应用内的正文，其余只读。
func (a *app) profileTxRow(c *ui.Context, tx coinTransaction) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(8, 10).Radius(8).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, coinReasonLabel(tx.Reason)).FontSize(12).Bold().SingleLine()
				if detail := coinTransactionDetail(tx); detail != "" {
					ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MaxLines(1)
				}
			})
			ui.Column(c).Shrink(0).Gap(1).Children(func() {
				ui.Text(c, coinSignedText(tx.Amount)).FontSize(13).Bold().
					TextColor(coinAmountColor(t, tx.Amount)).TextAlign(ui.End)
				ui.Text(c, coinWhen(tx.CreatedAt, false)).FontSize(10).
					TextColor(t.TextMuted).TextAlign(ui.End)
			})
		})
	})
	if tx.RelatePath != "" {
		row.Tooltip("打开相关帖子或页面")
		row.OnClick(func() { a.handleLink(tx.RelatePath) })
	}
}

// profileStatsSection 是「最近统计」：粉丝、关注、主页访问与等级来自公开
// 资料；帖子数与回复数只有对应的标签页拉到过数据时才显示，不为了一个数字
// 在打开窗口时多发两个请求。
func (a *app) profileStatsSection(c *ui.Context, ps *profileState) {
	u := ps.user
	settingsSection(c, "最近统计", func() {
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			profileStatChip(c, "粉丝", profileCountText(ps.infoLoaded, u.Followers))
			profileStatChip(c, "关注", profileCountText(ps.infoLoaded, u.Following))
			profileStatChip(c, "主页访问", profileCountText(ps.infoLoaded, u.HomeView))
			profileStatChip(c, "等级", profileCountText(ps.infoLoaded && u.Level > 0, u.Level))
			if ps.topicLoaded {
				profileStatChip(c, "帖子", fmt.Sprintf("%d", len(ps.topics)))
			}
			if ps.replyLoaded {
				profileStatChip(c, "回复", fmt.Sprintf("%d", ps.replyTotal))
			}
		})
	})
}

// profileCountText 在数据还没到或接口没给值时显示占位，而不是一个假的 0。
func profileCountText(known bool, n int) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func profileStatChip(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Box(c).Padding(6, 10).Radius(8).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, value).FontSize(14).Bold().SingleLine()
			ui.Text(c, label).FontSize(10).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// profileAuthEntry 是概览页底部的授权管理快速入口：一条说明 + 两个出口。
func (a *app) profileAuthEntry(c *ui.Context, ps *profileState) {
	t := c.Theme()
	settingsSection(c, "授权管理", func() {
		ui.Text(c, "本应用通过 2Libra OAuth 读取你的账号，只能用你授予的权限范围；"+
			"随时可以在站点后台撤销，撤销后本应用的接口调用会立即失效。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			a.smallAction(c, "查看当前权限", false, func() { ps.tab = profileTabAccount })
			a.smallAction(c, "在站点管理授权", false, func() {
				a.openExternal(siteBaseURL + oauthManagePath)
			})
		})
	})
}

/* ------------------------------------------------------------------
   Tab 2：我的帖子
---------------------------------------------------------------- */

func (a *app) profileTopicsBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "我的帖子").FontSize(13).Bold()
		if ps.topicLoaded {
			ui.Textf(c, "共 %d 篇", len(ps.topics)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", ps.topicLoading, func() { a.loadProfileTopics() })
		a.smallAction(c, "在站点查看全部", false, func() {
			a.openExternal(siteBaseURL + "/user/" + url.PathEscape(a.auth.username()) + "/topics")
		})
	})

	switch {
	case ps.topicLoading && len(ps.topics) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载我的帖子…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case ps.topicErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, ps.topicErr).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadProfileTopics()
			}
		})
		return
	case len(ps.topics) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还没有发布过帖子。").FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, "需要发帖请到站点完成。").FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	items := ps.topics
	ps.topicList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &ps.topicList, len(items), func(i int) {
		a.profileTopicRow(c, items[i])
	}).Grow(1).MinHeight(0).Gap(8)
}

func (a *app) profileTopicRow(c *ui.Context, it Item) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, it.Title).FontSize(13).Bold().MaxLines(2)
			if meta := profileTopicMeta(it); meta != "" {
				ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
	if it.ShortID != "" {
		row.Tooltip("打开正文")
		row.OnClick(func() { a.openProfileDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.openExternal(it.URL) })
	}
}

// profileTopicMeta 是帖子行的次行：发布时间 + 回复 / 浏览等元信息。时间在
// Item 里是 SubtitleSuffix（形如「 · 2 天前」），这里去掉前导分隔符。
func profileTopicMeta(it Item) string {
	parts := make([]string, 0, 2)
	if when := strings.TrimPrefix(it.SubtitleSuffix, " · "); when != "" {
		parts = append(parts, when)
	}
	if it.Meta != "" {
		parts = append(parts, it.Meta)
	}
	return strings.Join(parts, " · ")
}

/* ------------------------------------------------------------------
   Tab 3：我的回复
---------------------------------------------------------------- */

func (a *app) profileRepliesBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	name := a.auth.username()
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "我的回复").FontSize(13).Bold()
		if ps.replyLoaded {
			ui.Textf(c, "共 %d 条", ps.replyTotal).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", ps.replyLoading, func() {
			if name != "" {
				a.loadProfileReplies(name, 1)
			}
		})
		a.smallAction(c, "在站点查看全部", false, func() {
			a.openExternal(siteBaseURL + "/user/" + url.PathEscape(name) + "/replies")
		})
	})

	if name == "" {
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还不知道当前账号的用户名").FontSize(13).Bold()
			ui.Text(c, "点右上角「刷新」重新读取账号资料后再试。").FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	switch {
	case ps.replyLoading && len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载我的回复…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case ps.replyErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, ps.replyErr).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadProfileReplies(name, 1)
			}
		})
		return
	case len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还没有发表过回复。").FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, "在帖子详情页底部的评论框里发表评论。").FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	items := ps.replies
	ps.replyList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &ps.replyList, len(items), func(i int) {
		a.profileReplyRow(c, items[i])
	}).Grow(1).MinHeight(0).Gap(8)
	a.profileMoreReplies(c, ps, name)
}

// profileMoreReplies 是回复列表底部的续拉行：还有下一页时给按钮，续拉失败
// 时给出原因并可重试，已有内容保持不动。
func (a *app) profileMoreReplies(c *ui.Context, ps *profileState, name string) {
	t := c.Theme()
	switch {
	case ps.replyMoreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+ps.replyMoreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadProfileReplies(name, ps.replyPage) })
		})
	case ps.replyLoading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	case !ps.replyNoMore:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "加载更多").Clicked() {
				a.loadProfileReplies(name, ps.replyPage+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

func (a *app) profileReplyRow(c *ui.Context, it Item) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, it.Title).FontSize(12).MaxLines(2)
			parts := make([]string, 0, 3)
			if x := it.Extra; x != nil {
				if x.PostTitle != "" {
					parts = append(parts, "《"+x.PostTitle+"》")
				}
				if x.Floor > 0 {
					parts = append(parts, fmt.Sprintf("%d 楼", x.Floor))
				}
				if x.When != "" {
					parts = append(parts, x.When)
				}
			}
			if len(parts) > 0 {
				ui.Text(c, strings.Join(parts, " · ")).FontSize(11).
					TextColor(t.TextMuted).SingleLine()
			}
		})
	})
	if it.ShortID != "" {
		row.Tooltip("打开原帖并定位这条回复")
		row.OnClick(func() { a.openProfileDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.openExternal(it.URL) })
	}
}

/* ------------------------------------------------------------------
   Tab 4：权限与账号
---------------------------------------------------------------- */

func (a *app) profileAccountBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	u := ps.user
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			settingsSection(c, "账号", func() {
				a.profileAccountRow(c, "用户名", profileOrDash(u.Name, a.auth.username()))
				a.profileAccountRow(c, "用户 ID", profileUserNumber(u.UserNumber))
				a.profileAccountRow(c, "注册时间", profileRegisteredOrDash(u.CreatedAt))
				a.profileAccountRow(c, "等级", profilePositiveOrDash(ps.infoLoaded, u.Level))
				a.profileAccountRow(c, "粉丝 / 关注",
					profileFollowText(ps.infoLoaded, u.Followers, u.Following))
			})
			ui.Divider(c)

			settingsSection(c, "授权范围", func() {
				scopes := a.auth.scopes()
				if len(scopes) == 0 {
					ui.Text(c, "令牌里没有 scope 信息：可能是旧版本授权的会话，"+
						"重新登录一次即可拿到完整范围。").
						FontSize(11).TextColor(t.TextMuted).MaxLines(3)
				} else {
					ui.Text(c, "当前令牌携带 "+fmt.Sprintf("%d", len(scopes))+" 项权限：").
						FontSize(11).TextColor(t.TextMuted)
					for _, s := range scopes {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, s).FontSize(11).Width(110).SingleLine()
							ui.Text(c, profileScopeLabel(s)).FontSize(11).
								TextColor(t.TextMuted).SingleLine().Grow(1)
						})
					}
				}
			})
			ui.Divider(c)

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.PrimaryButton(c, "在 2Libra 管理授权").Clicked() {
					a.openExternal(siteBaseURL + oauthManagePath)
				}
				if ui.Button(c, "退出登录").Clicked() {
					a.auth.Logout()
				}
				ui.Spacer(c).Grow(1)
			})
			ui.Text(c, "撤销授权页在 2Libra 站点的「授权应用」里；撤销后需要重新登录本应用。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
	})
}

func (a *app) profileAccountRow(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		ui.Text(c, label).FontSize(12).Width(90).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(12).SingleLine()
	})
}

func profileOrDash(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "—"
}

func profileUserNumber(n int) string {
	if n <= 0 {
		return "—"
	}
	return "#" + fmt.Sprintf("%d", n)
}

func profileRegisteredOrDash(raw string) string {
	if text := profileRegisteredText(raw); text != "" {
		return text
	}
	return "—"
}

func profilePositiveOrDash(known bool, n int) string {
	if !known || n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func profileFollowText(known bool, followers, following int) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("%d / %d", followers, following)
}

/* ------------------------------------------------------------------
   打开正文与切换主窗口
---------------------------------------------------------------- */

// openProfileDetail 从「我的」窗口打开一篇帖子或一条回复：关掉本窗口，把
// 主窗口带到前台并换到详情页。评论行会带上 CommentID，正文里据此定位楼层。
func (a *app) openProfileDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.closeProfileWindow()
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(it)
	a.showMainWindow()
}

// gotoTab 把主窗口切到侧边栏的某一页（金币页、我的帖子等），并清掉详情页。
func (a *app) gotoTab(i int) {
	if i < 0 || i >= len(tabSpecs) {
		return
	}
	a.tab = i
	a.lastTab = i
	a.navSel = tabID(i)
	a.pageName = "list"
	a.detail = nil
	a.detailStack = nil
	a.status = ""
}
