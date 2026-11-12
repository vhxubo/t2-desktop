package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 通知 tab 的界面：头部（未读数 / 实时连接 / 全部已读 / 刷新 / 在站点查看 /
// 原生通知开关）、分段筛选、消息列表与只读操作。数据与状态在 notify.go。

func (a *app) notificationsBody(c *ui.Context, st *tabState) {
	t := c.Theme()
	spec := tabSpecs[notificationTabIndex()]

	ui.Row(c).FillWidth().Padding(10, 14, 6, 14).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, spec.Label).FontSize(16).Bold()
		unreadText := "无未读"
		if a.unread > 0 {
			unreadText = fmt.Sprintf("%d 条未读", a.unread)
		}
		ui.Text(c, unreadText).FontSize(12).TextColor(t.TextMuted)
		switch {
		case !a.auth.loggedIn():
		case a.streamLive:
			ui.Text(c, "· 实时通知已连接").FontSize(11).TextColor(t.Success)
		default:
			ui.Text(c, "· 实时通知未连接").FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
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
		// 勾选批量：只有勾了未读消息才出现，避免空按钮占位。
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

	// 分段筛选直接映射接口的 type / unread_only，切换后按新参数重拉。
	ui.Row(c).FillWidth().Padding(0, 14, 8, 14).Gap(6).Wrap().Children(func() {
		for i := range notificationFilters {
			a.notificationFilterChip(c, i, st)
		}
	})
	ui.Divider(c)

	if st.actionErr != "" {
		ui.Row(c).FillWidth().Padding(6, 14).Children(func() {
			ui.Text(c, st.actionErr).FontSize(11).TextColor(t.Danger).MaxLines(2)
		})
	}

	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(28, 28)
			ui.Text(c, "正在加载通知…").TextColor(t.TextMuted)
		})
	case st.err != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "加载失败").FontSize(16).Bold()
			ui.Text(c, st.err).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.reloadNotifications()
			}
			if strings.Contains(st.err, "登录") {
				if ui.Button(c, "去登录").Clicked() {
					a.startLogin()
				}
			}
		})
	case len(st.items) == 0:
		meta := notificationFilterAt(st.filter)
		ui.Column(c).Grow(1).Center().Gap(8).Padding(24).Children(func() {
			ui.Text(c, meta.EmptyTitle).FontSize(15).Bold()
			ui.Text(c, meta.EmptyHint).TextColor(t.TextMuted).MaxLines(3)
			if st.filter != notificationFilterAll {
				if ui.Button(c, "查看全部").Clicked() {
					a.setNotificationFilter(notificationFilterAll)
				}
			}
		})
	default:
		items := st.items
		st.list.Key = func(i int) any {
			if i >= 0 && i < len(items) {
				return items[i].Key
			}
			return i
		}
		ui.List(c, &st.list, len(items), func(i int) {
			a.notificationRow(c, items[i])
		}).Grow(1).Padding(8, 14).Gap(10)
		// 无限滚动：滚到底且还有下一页时自动加载。
		if st.loaded && !st.loading && !st.noMore && len(st.items) > 0 && st.list.AtEnd() {
			hasMore := st.totalPages == 0 || st.page < st.totalPages
			if hasMore {
				a.load(notificationTabIndex(), st.page+1)
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
		card := ui.ButtonBase(c).Grow(1).MinWidth(0).Padding(12, 14).Radius(10).
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
					if v.Reactions != "" {
						ui.Text(c, v.Reactions).FontSize(11).TextColor(t.TextMuted).SingleLine()
					}
				})
				title := v.Action
				if v.ActorName != "" {
					title = "@" + v.ActorName + " " + v.Action
				}
				ui.Text(c, title).FontSize(13).Bold().MaxLines(2)
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
