package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 通知窗口：通知不再是主窗口里的页面或弹窗，而是一个独立的小窗口。标题栏
// 的铃铛（带未读数值）是它的入口，任何窗口、任何 goroutine 都可以调用
// showNotifications 把它打开。点开一条消息，帖子与评论回到主窗口里打开。
// 数据、长连接与已读落账在 notify.go。

// 通知窗口的默认尺寸。用 StateKey 记住用户把它放在哪、拉成多大，所以这里
// 只是首次打开时的默认值。
const (
	notifWindowWidth     = 520
	notifWindowHeight    = 660
	notifWindowMinWidth  = 380
	notifWindowMinHeight = 420
)

// unreadCountText 是铃铛上的数值：三位数以上收成 99+，0 返回空串表示不画
// 数字（标题栏里常驻一个「0」只是噪音）。
func unreadCountText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n > 99:
		return "99+"
	}
	return fmt.Sprintf("%d", n)
}

// notificationBell 是主窗口标题栏上的通知入口：铃铛图标 + 右下角的红色未读
// 角标。图标本身是装饰，可访问名由 Label 提供（「通知（N 条未读）」），读屏
// 与测试都按这个名字找它。
func (a *app) notificationBell(c *ui.Context) {
	t := c.Theme()
	idx := notificationTabIndex()
	open := a.notificationWindowVisible()
	b := ui.Box(c).Padding(4, 6).Radius(6).Center().Background(ui.Transparent)
	if open || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	switch {
	case open:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	case a.unread > 0:
		// 有未读时铃铛用正文色，和右下角的红点一起提示「有事」。
		ink = t.Text
	}
	b.Children(func() {
		// 铃铛与角标叠在一起：图标盒固定 16×16，角标绝对定位在它的右下角，
		// 压出一点图标边缘。旧写法把数值排在铃铛右边，标题栏因此被撑宽，
		// 数字离图标也远；现在数值贴在铃铛上，看起来就是一个角标。
		ui.Box(c).Width(16).Height(16).Shrink(0).Children(func() {
			if idx >= 0 {
				ui.Icon(c, tabIcons[idx]).Size(16, 16).TextColor(ink)
			}
			if count := unreadCountText(a.unread); count != "" {
				// 角标外圈用铃铛底色描一圈，压住铃铛线条处也留得出一条缝，
				// 红点不会和图标糊在一起。悬停、打开时底色换成 SurfaceHover。
				ring := t.Surface
				if open || b.Hovered() {
					ring = t.SurfaceHover
				}
				ui.Box(c).Absolute().Right(-3).Bottom(-3).
					MinWidth(14).Height(14).Padding(0, 3).Radius(999).Center().
					Border(1, ring).Background(t.Danger).Children(func() {
					ui.Text(c, count).FontSize(9).Bold().SingleLine().TextColor(ui.Hex("#ffffff"))
				})
			}
		})
	})
	label := "通知"
	if n := a.unread; n > 0 {
		label = fmt.Sprintf("通知（%d 条未读）", n)
	}
	hint := "打开通知窗口"
	if a.unread > 0 {
		hint = fmt.Sprintf("%d 条未读，点开查看", a.unread)
	}
	b.Label(label).Tooltip(hint).OnClick(a.showNotifications)
}

/* ------------------------------------------------------------------
   通知窗口：全局入口、显示与关闭
----------------------------------------------------------------- */

// showNotifications 是通知窗口的全局入口：任何窗口、任何 goroutine 都能
// 调用。它把工作挪到主线程，窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showNotifications() {
	a.publish(a.showNotificationWindow)
}

// showNotificationWindow 在主线程上真正打开通知窗口。先刷新未读数与列表，
// 再创建窗口：首帧里看到的就是最新的一页，而不是空列表再闪一下。
func (a *app) showNotificationWindow() {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看「通知」。", a.showNotifications)
		return
	}
	a.refreshUnread()
	if a.autoLoad {
		a.load(idx, 1)
	}
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 notificationsWindowView。
	if a.win == nil {
		return
	}
	if win := a.notifWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setNotificationWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "通知",
		Width:             notifWindowWidth,
		Height:            notifWindowHeight,
		MinWidth:          notifWindowMinWidth,
		MinHeight:         notifWindowMinHeight,
		StateKey:          "notifications",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.notificationsWindowView),
	})
	a.notifWin = win
	// 显示状态跟着窗口事件走：用户收起、最小化、关掉窗口时铃铛都要跟着变，
	// 不能只按「窗口对象还在不在」判断。
	win.OnShow(func() { a.setNotificationWindowShown(true) })
	win.OnHide(func() { a.setNotificationWindowShown(false) })
	win.OnClosed(func() {
		a.notifWin = nil
		a.setNotificationWindowShown(false)
	})
	a.setNotificationWindowShown(true)
}

// notificationWindowVisible 报告通知窗口此刻是否在屏幕上。
func (a *app) notificationWindowVisible() bool {
	return a.notifShown
}

// setNotificationWindowShown 记下通知窗口的显示状态，并让主窗口重画一次：
// 铃铛的高亮就跟着这个状态。只在主线程调用。
func (a *app) setNotificationWindowShown(shown bool) {
	if a.notifShown == shown {
		return
	}
	a.notifShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// hideNotificationWindow 收起通知窗口但不销毁：下次从铃铛打开就是同一个
// 窗口、同一个滚动位置。Esc 用它。点开一条消息时不收起，用户回头还能接着看。
func (a *app) hideNotificationWindow() {
	if win := a.notifWin; win != nil {
		a.setNotificationWindowShown(false)
		win.Hide()
	}
}

// closeNotificationWindow 关掉通知窗口：退出登录或主窗口关闭时调用，别让
// 上一个账号的消息、或者一个没有主窗口的孤岛留在屏幕上。只在主线程调用。
func (a *app) closeNotificationWindow() {
	win := a.notifWin
	if win == nil {
		return
	}
	a.notifWin = nil
	a.setNotificationWindowShown(false)
	win.Close()
}

// showMainWindow 把主窗口带到前台，并让它按当前状态重建：在通知窗口里点开
// 帖子后，主窗口要重新构建才会显示新的详情页。
func (a *app) showMainWindow() {
	win := a.win
	if win == nil {
		return
	}
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	win.Invalidate()
}

/* ------------------------------------------------------------------
   通知窗口的界面
----------------------------------------------------------------- */

// notificationsWindowView 是通知窗口的 view。窗口自己带标题栏与窗口按钮，
// 所以这里不需要遮罩，也不占主窗口的位置。
func (a *app) notificationsWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 消息行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 标记已读等操作的结果以 toast 显示在这个窗口里。
	a.drainToasts(c, a.claimsToasts(toastNotifications))
	// Esc 收起窗口而不是退出：消息中心随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.hideNotificationWindow)

	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	unreadText := "无未读"
	if a.unread > 0 {
		unreadText = fmt.Sprintf("%d 条未读", a.unread)
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、双击最大化，右上角是最小化与关闭，按钮与
		// 主窗口同一套画法。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, tabIcons[idx]).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "通知").FontSize(13).Bold().SingleLine()
				ui.Text(c, unreadText).FontSize(11).TextColor(t.TextMuted).SingleLine()
				switch {
				case a.streamLive:
					ui.Text(c, "· 已连接").FontSize(11).TextColor(t.Success).SingleLine()
				default:
					ui.Text(c, "· 未连接").FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.notifWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", func() {
				if win := a.notifWin; win != nil {
					win.Close()
				}
			})
		})
		ui.Divider(c)

		ui.Column(c).Grow(1).MinHeight(0).Padding(10, 12).Gap(8).Children(func() {
			a.notificationActions(c, st)

			// 分段筛选直接映射接口的 type / unread_only，切换后按新参数重拉。
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				for i := range notificationFilters {
					a.notificationFilterChip(c, i, st)
				}
			})
			ui.Divider(c)

			a.notificationsList(c, st)
		})
	})
}

// notificationActions 是窗口里的操作行：全部已读、已读本页、勾选批量、
// 刷新与原生通知开关。勾选批量只在真勾了消息时出现，避免空按钮占位。
func (a *app) notificationActions(c *ui.Context, st *tabState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		a.smallAction(c, "全部已读", st.markingAll || (a.unread == 0 && !hasUnreadItem(st.items)),
			func() { a.markAllNotificationsRead() })
		// 「已读本页」= 把当前已加载的未读消息一次性标为已读；它与站点 Web 的
		// 同名操作一致，底层就是一次带 id 列表的 POST。
		loadedUnread := len(unreadNotificationIDs(st.items))
		loadedLabel := "已读本页"
		if loadedUnread > 0 {
			loadedLabel = fmt.Sprintf("已读本页 %d", loadedUnread)
		}
		a.smallAction(c, loadedLabel, st.loading || loadedUnread == 0,
			func() { a.markLoadedNotificationsRead() })
		if n := notificationSelectionCount(st); n > 0 {
			a.smallAction(c, fmt.Sprintf("已读选中 %d", n), false,
				func() { a.markSelectedNotificationsRead() })
		}
		a.smallAction(c, "刷新", st.loading, func() { a.reloadNotifications() })
		switch {
		case a.notify == nil:
			ui.Text(c, "原生通知不可用").FontSize(11).TextColor(t.TextMuted)
		case a.nativeNotify:
			a.smallAction(c, "原生通知：开", false, func() { a.nativeNotify = false })
		default:
			a.smallAction(c, "原生通知：关", false, func() { a.nativeNotify = true })
		}
	})
}

// notificationsList 画窗口里的消息区：加载中、错误、空状态与列表。
func (a *app) notificationsList(c *ui.Context, st *tabState) {
	t := c.Theme()
	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(28, 28)
			ui.Text(c, "正在加载通知…").TextColor(t.TextMuted)
		})
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "加载失败").FontSize(16).Bold()
			ui.Text(c, st.err).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.reloadNotifications()
			}
			if strings.Contains(st.err, "登录") || strings.Contains(st.err, "授权") {
				if ui.Button(c, "去授权").Clicked() {
					a.requestLogin("登录并授权后才能继续查看「通知」。",
						func() { a.reloadNotifications() })
				}
			}
		})
	case len(st.items) == 0:
		meta := notificationFilterAt(st.filter)
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Padding(24).Children(func() {
			ui.Text(c, meta.EmptyTitle).FontSize(15).Bold()
			ui.Text(c, meta.EmptyHint).TextColor(t.TextMuted).MaxLines(3)
			if st.filter != notificationFilterAll {
				if ui.Button(c, "查看全部").Clicked() {
					a.setNotificationFilter(notificationFilterAll)
				}
			}
		})
	default:
		idx := notificationTabIndex()
		items := st.items
		st.list.Key = func(i int) any {
			if i >= 0 && i < len(items) {
				return items[i].Key
			}
			return i
		}
		ui.List(c, &st.list, len(items), func(i int) {
			a.notificationRow(c, items[i])
		}).Grow(1).MinHeight(0).Padding(4, 0).Gap(8)
		a.loadMoreFooter(c, st.moreErr, st.loading, func() {
			a.load(idx, st.page)
		})
		// 无限滚动：滚到底且还有下一页时自动加载。续拉失败后停在提示上，
		// 等用户点「重试」，避免每次重建都重发同一个失败的请求。
		if st.loaded && !st.loading && st.moreErr == "" && !st.noMore &&
			len(st.items) > 0 && st.list.AtEnd() {
			hasMore := st.totalPages == 0 || st.page < st.totalPages
			if hasMore {
				a.load(idx, st.page+1)
			}
		}
	}
}

// hasUnreadItem reports whether the loaded page has any unread message. It
// decides whether 「全部已读」 is meaningful before the unread badge catches up.
func hasUnreadItem(items []Item) bool {
	for _, it := range items {
		if it.Notif != nil && !it.Notif.IsRead {
			return true
		}
	}
	return false
}

func (a *app) notificationFilterChip(c *ui.Context, index int, st *tabState) {
	t := c.Theme()
	option := notificationFilters[index]
	active := st.filter == index
	chip := ui.Button(c, option.Label).FontSize(12).Padding(3, 10).Radius(999)
	if active {
		chip.Background(t.Accent).TextColor(t.AccentText)
	} else {
		chip.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
	}
	chip.Tooltip(option.Hint)
	if chip.Clicked() && !active {
		a.setNotificationFilter(index)
	}
}

func (a *app) smallAction(c *ui.Context, label string, disabled bool, onClick func()) {
	t := c.Theme()
	button := ui.Button(c, label).
		Background(ui.Transparent).
		TextColor(t.Text).
		FontSize(12).
		Padding(2, 6).
		Radius(6)
	if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Disabled(disabled)
	if button.Clicked() {
		onClick()
	}
}

func (a *app) setNotificationFilter(filter int) {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if st.filter == filter {
		return
	}
	st.filter = filter
	st.items = nil
	st.selected = nil
	st.loaded = false
	st.loading = false
	st.noMore = false
	st.err = ""
	st.moreErr = ""
	st.page = 1
	st.list.ScrollTo(0, ui.Start)
	a.load(idx, 1)
}

func (a *app) reloadNotifications() {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if st.loading {
		return
	}
	st.loaded = false
	st.items = nil
	st.selected = nil
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(idx, 1)
}

// notificationRow 一条消息就是一张和列表同宽的卡片：勾选框、未读点与「标为
// 已读」都在卡片里，表态的表情与人名写在同一行。整张卡片可点，点开的是这条
// 消息的目标。
func (a *app) notificationRow(c *ui.Context, it Item) {
	n := it.Notif
	if n == nil {
		return
	}
	t := c.Theme()
	v := notificationViewOf(n)
	unread := !n.IsRead
	st := a.tabs[notificationTabIndex()]
	reading := st != nil && st.reading[n.ID]

	card := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if card.Hovered() {
		card.Background(t.SurfaceHover)
	}
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			// 卡片头：勾选框（仅未读）、未读点、类型、时间，未读时右上角是
			// 「标为已读」。这些都在卡片内，每行不再为它们各留一条竖带——旧
			// 写法把勾选框和「已读」摆在卡片外面，正文被挤成窄窄一条。
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				if unread && st != nil {
					checked := st.selected[n.ID]
					ui.Checkbox(c, &checked, "").Shrink(0).
						Label("勾选这条消息").Tooltip("勾选后可批量标记已读")
					if checked != st.selected[n.ID] {
						setNotificationSelected(st, n.ID, checked)
					}
				}
				if unread {
					ui.Text(c, "●").FontSize(9).TextColor(t.Accent)
				}
				ui.Text(c, v.TypeLabel).FontSize(11).Bold().TextColor(notificationToneColor(t, v.Tone))
				if v.ActorIsAlias {
					ui.Text(c, "匿名").FontSize(11).TextColor(t.TextMuted)
				}
				if when := relativeTime(n.CreatedAt); when != "" {
					ui.Text(c, when).FontSize(11).TextColor(t.TextMuted)
				}
				ui.Spacer(c).Grow(1)
				if unread {
					a.notificationReadAction(c, reading, func() { a.markNotificationRead(n.ID) })
				}
			})
			switch {
			case len(v.Reaction.Groups) > 0:
				// 表态消息：表情合到一行，人名只写一次（见 notificationReactionLine）。
				a.notificationReactionLine(c, v.Reaction, v.Action)
			case v.ActorName != "":
				// 操作者昵称在句首：统一用户标识把头像画在 @昵称前，头像跟着
				// 13px 的标题字号走；聚合表态等没有操作者的消息保持纯文字。
				a.userTag(c, userTagSpec{
					Name:     v.ActorName,
					Avatar:   v.ActorAvatar,
					Label:    "@" + v.ActorName + " " + v.Action,
					Size:     13,
					Bold:     true,
					MaxLines: 2,
					Badges:   v.ActorBadges,
				})
			default:
				action := v.Action
				if v.Kind == "reaction" {
					// 表态消息但服务端没给表情和人名：补一个主语，句子不能只剩动词。
					action = "有人" + action
				}
				ui.Text(c, action).FontSize(13).Bold().MaxLines(2)
			}
			if v.TargetTitle != "" {
				ui.Text(c, "《"+v.TargetTitle+"》").FontSize(12).TextColor(t.Text).SingleLine()
			}
			if v.Excerpt != "" {
				ui.Text(c, v.Excerpt).FontSize(12).TextColor(t.Text).MaxLines(3)
			}
			if v.Detail != "" {
				ui.Text(c, v.Detail).FontSize(12).TextColor(notificationToneColor(t, v.DetailTone)).MaxLines(2)
			}
			if len(v.Meta) > 0 {
				ui.Text(c, strings.Join(v.Meta, " · ")).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}
		})
	})
	// 可访问名说明这一下点开的是什么：读屏读得到，测试也按它取卡片的位置。
	card.Label("打开消息：" + v.Action)
	card.OnClick(func() { a.openNotificationTarget(v.TargetURL) })
}

// notificationReadAction 是未读消息卡片右上角的「标为已读」。它只是卡片里的
// 一个小动作，不占一整列：字小、悬停换强调色，点它不会连带打开消息。
func (a *app) notificationReadAction(c *ui.Context, busy bool, onClick func()) {
	t := c.Theme()
	label := "标为已读"
	if busy {
		label = "正在标记…"
	}
	b := ui.Button(c, label).Background(ui.Transparent).FontSize(11).
		Padding(1, 6).Radius(6).TextColor(t.TextMuted)
	if b.Hovered() {
		b.TextColor(t.Accent)
	}
	b.Disabled(busy).Tooltip("只把这一条标为已读")
	if b.Clicked() {
		onClick()
	}
}

// notificationReactionLine 画表态消息的「用了什么表情、都有谁」：表情合并成
// 一行胶囊，不同表情不再各占一组括号；人名去重后只出现一次——同一个人用了两
// 种表情也只写一个名字——名字之间用顿号相连，超出上限的人用「等 N 人」收尾。
// 这一行后面接着的就是这条消息要说什么（「表态了你的帖子」）。
func (a *app) notificationReactionLine(c *ui.Context, o notificationReactionOverview, action string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
		for _, g := range o.Groups {
			a.notificationReactionChip(c, g)
		}
		names := o.People
		if len(names) > notificationReactionNamesMax {
			names = names[:notificationReactionNamesMax]
		}
		for i, u := range names {
			if i > 0 {
				ui.Text(c, "、").FontSize(11).TextColor(t.TextMuted)
			}
			a.userTag(c, userTagSpec{
				Name:   u.Name,
				Avatar: u.Avatar,
				Label:  "@" + u.Name,
				Size:   11,
				Color:  t.TextMuted,
				Badges: u.Badges,
			})
		}
		if others := notificationReactionOthers(o, len(names)); others > 0 {
			ui.Textf(c, "等 %d 人", others).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, action).FontSize(12).TextColor(t.Text)
	})
}

// notificationReactionChip 是一枚表情胶囊：与详情页的表态摘要是同一套圆角
// 胶囊。只有一次表态时不写数字，「👍 1」比「👍」更吵。
func (a *app) notificationReactionChip(c *ui.Context, g notificationReactionGroup) {
	t := c.Theme()
	label := g.Emoji
	if g.Count > 1 {
		label = fmt.Sprintf("%s %d", g.Emoji, g.Count)
	}
	chip := ui.Box(c).Padding(1, 7).Radius(999).Shrink(0).Center().
		Background(t.Background).Border(1, t.Border)
	chip.Label(fmt.Sprintf("%s，%d 次表态", g.Emoji, g.Count))
	chip.Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
}

func notificationToneColor(t *ui.Theme, tone string) ui.Color {
	switch tone {
	case "success":
		return t.Success
	case "warning":
		return t.Warning
	case "danger":
		return t.Danger
	case "primary", "info":
		return t.Accent
	}
	return t.TextMuted
}
