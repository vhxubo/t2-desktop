package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 通知的界面：标题栏上的铃铛（带未读数值）打开一个独立弹窗，弹窗里是
// 分段筛选、消息列表与已读操作。数据、长连接与已读落账在 notify.go。

// notifDialogWidth / notifDialogHeight 是通知弹窗的首选尺寸；窗口更小
// 时按窗口收缩，列表在面板里滚动，不把弹窗顶出屏幕。
const (
	notifDialogWidth  = 640
	notifDialogHeight = 560
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

// notificationBell 是标题栏上的通知入口：铃铛图标 + 未读数。图标本身是
// 装饰，可访问名由 Label 提供（「通知（N 条未读）」），读屏与测试都按这个
// 名字找它。
func (a *app) notificationBell(c *ui.Context) {
	t := c.Theme()
	idx := notificationTabIndex()
	b := ui.Box(c).Padding(4, 6).Radius(6).Center().Background(ui.Transparent)
	if a.notifOpen || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	switch {
	case a.notifOpen:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	case a.unread > 0:
		// 有未读时铃铛用正文色，和右边的数值一起提示「有事」。
		ink = t.Text
	}
	b.Children(func() {
		// 图标与数值并排：Box 默认是纵向的，这里显式排一行，否则数值会掉到
		// 铃铛下面，把 32px 的标题栏撑高。
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if idx >= 0 {
				ui.Icon(c, tabIcons[idx]).Size(16, 16).TextColor(ink)
			}
			if count := unreadCountText(a.unread); count != "" {
				ui.Box(c).Padding(0, 5).Radius(999).Center().Background(t.Accent).Children(func() {
					ui.Text(c, count).FontSize(10).Bold().SingleLine().TextColor(t.AccentText)
				})
			}
		})
	})
	label := "通知"
	if n := a.unread; n > 0 {
		label = fmt.Sprintf("通知（%d 条未读）", n)
	}
	hint := "打开通知弹窗"
	if a.unread > 0 {
		hint = fmt.Sprintf("%d 条未读，点开查看", a.unread)
	}
	b.Label(label).Tooltip(hint).OnClick(func() { a.openNotifications() })
}

// openNotifications 打开通知弹窗。未登录时先走全局授权弹窗，授权成功后
// 重跑这一步，用户不用回到标题栏再点一次。
func (a *app) openNotifications() {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看「通知」。", func() { a.openNotifications() })
		return
	}
	a.notifOpen = true
	a.refreshUnread()
	// 已经加载过的列表也刷新一次：弹窗打开时看到最新一页，加载过程中旧
	// 内容仍然可见，不会被换成一片空白。headless 测试（autoLoad 为假）
	// 只渲染已注入的列表，不发真实请求。
	if a.autoLoad {
		a.load(idx, 1)
	}
}

// notificationsDialog 是通知弹窗本体。用 Modal 而不是页面：消息中心是
// 随时要看一眼的东西，不该把正在浏览的列表或正文挤走。
func (a *app) notificationsDialog(c *ui.Context) {
	if !a.notifOpen {
		return
	}
	idx := notificationTabIndex()
	if idx < 0 {
		a.notifOpen = false
		return
	}
	t := c.Theme()
	st := a.tabs[idx]
	unreadText := "无未读"
	if a.unread > 0 {
		unreadText = fmt.Sprintf("%d 条未读", a.unread)
	}
	panelW, panelH := notificationsDialogSize(c)

	ui.Modal(c, &a.notifOpen, func() {
		ui.Column(c).Width(panelW).Height(panelH).Gap(8).Children(func() {
			// 头部：铃铛、标题、未读数与实时连接状态，右侧是关闭。
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, tabIcons[idx]).Size(18, 18).TextColor(t.Accent)
				ui.Text(c, "通知").FontSize(16).Bold().SingleLine()
				ui.Text(c, unreadText).FontSize(12).TextColor(t.TextMuted).SingleLine()
				switch {
				case !a.auth.loggedIn():
				case a.streamLive:
					ui.Text(c, "· 实时通知已连接").FontSize(11).TextColor(t.Success).SingleLine()
				default:
					ui.Text(c, "· 实时通知未连接").FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
				ui.Spacer(c).Grow(1)
				closeBtn := ui.Button(c, "").Background(ui.Transparent).
					Border(0, t.Border).TextColor(t.Text).FontSize(14).
					Padding(4, 4).Radius(6)
				closeBtn.Children(func() {
					ui.Icon(c, closeIcon).Size(16, 16)
				})
				closeBtn.Label("关闭通知")
				if closeBtn.Clicked() {
					a.notifOpen = false
				}
			})

			a.notificationActions(c, st)

			// 分段筛选直接映射接口的 type / unread_only，切换后按新参数重拉。
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				for i := range notificationFilters {
					a.notificationFilterChip(c, i, st)
				}
			})
			ui.Divider(c)

			if st.actionErr != "" {
				ui.Text(c, st.actionErr).FontSize(11).TextColor(t.Danger).MaxLines(2)
			}
			a.notificationsList(c, st)
		})
	})
}

// notificationActions 是弹窗里的操作行：全部已读、已读本页、勾选批量、
// 刷新、在站点查看与原生通知开关。勾选批量只在真勾了消息时出现，避免
// 空按钮占位。
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
		a.smallAction(c, "在站点查看", false, func() {
			a.openExternal(siteBaseURL + "/notifications")
		})
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

// notificationsList 画弹窗里的消息区：加载中、错误、空状态与列表。
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
		}).Grow(1).MinHeight(0).Padding(4, 2).Gap(8)
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

// notificationsDialogSize 是弹窗面板的尺寸：首选 640×560，窗口不够大时
// 留出四周的空隙后收缩（窗口最小 760×520，仍能完整放下）。
func notificationsDialogSize(c *ui.Context) (float32, float32) {
	w, h := float32(notifDialogWidth), float32(notifDialogHeight)
	if r := c.Root().Bounds(); r.W > 0 && r.H > 0 {
		w = min(w, r.W-100)
		h = min(h, r.H-100)
	}
	if w < 360 {
		w = 360
	}
	if h < 240 {
		h = 240
	}
	return w, h
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
	st.actionErr = ""
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
	st.actionErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(idx, 1)
}

// notificationRow renders one message: type + time on top, the action
// sentence, the target title, the comment body and additional details. The
// whole card opens the target; unread messages get a separate 「已读」 button.
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

	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		// 未读行左侧的勾选框：勾中的消息由「已读选中」一次提交，和站点 Web
		// 的勾选批量是同一个接口、同一种语义。
		if unread && st != nil {
			checked := st.selected[n.ID]
			ui.Checkbox(c, &checked, "").Shrink(0).Tooltip("勾选后可批量标记已读")
			if checked != st.selected[n.ID] {
				setNotificationSelected(st, n.ID, checked)
			}
		}
		card := ui.ButtonBase(c).Grow(1).MinWidth(0).Padding(10, 12).Radius(10).
			Background(t.Background).Border(1, t.Border)
		if card.Hovered() {
			card.Background(t.SurfaceHover)
		}
		card.Children(func() {
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
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
					if len(v.ReactionGroups) > 0 {
						a.notificationReactionLine(c, v.ReactionGroups)
					}
				})
				// 操作者昵称在句首：统一用户标识把头像画在 @昵称前，头像跟着
				// 13px 的标题字号走；聚合表态等没有操作者的消息保持纯文字。
				if v.ActorName != "" {
					a.userTag(c, userTagSpec{
						Name:     v.ActorName,
						Avatar:   v.ActorAvatar,
						Label:    "@" + v.ActorName + " " + v.Action,
						Size:     13,
						Bold:     true,
						MaxLines: 2,
					})
				} else {
					ui.Text(c, v.Action).FontSize(13).Bold().MaxLines(2)
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
		card.OnClick(func() { a.openNotificationTarget(v.TargetURL) })

		if unread {
			a.smallAction(c, "已读", reading, func() { a.markNotificationRead(n.ID) })
		}
	})
}

// notificationReactionLine 是通知头部右侧的聚合表态：每种表情一个计数，
// 点名的用户用统一用户标识带上头像（11px 昵称 → 16.5px 头像）。最多三组
// 名单、每组最多三人；宽度受限时折行，不把左侧的类型与时间挤出卡片。
func (a *app) notificationReactionLine(c *ui.Context, groups []notificationReactionGroup) {
	t := c.Theme()
	ui.Row(c).Gap(4).AlignItems(ui.Center).Wrap().MaxWidthPercent(60).Children(func() {
		for gi, g := range groups {
			if gi > 0 {
				ui.Text(c, " · ").FontSize(11).TextColor(t.TextMuted)
			}
			ui.Textf(c, "%s%d", g.Emoji, g.Count).FontSize(11).TextColor(t.TextMuted)
			if len(g.Users) == 0 {
				continue
			}
			ui.Text(c, "（").FontSize(11).TextColor(t.TextMuted)
			for i, u := range g.Users {
				if i > 0 {
					ui.Text(c, "、").FontSize(11).TextColor(t.TextMuted)
				}
				a.userTag(c, userTagSpec{
					Name:   u.Name,
					Avatar: u.Avatar,
					Label:  "@" + u.Name,
					Size:   11,
					Color:  t.TextMuted,
				})
			}
			ui.Text(c, "）").FontSize(11).TextColor(t.TextMuted)
		}
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
