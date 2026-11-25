package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 通知窗口：通知不再是主窗口里的页面或弹窗，而是一个独立的小窗口。标题栏
// 的铃铛（带未读数值）是它的入口，任何窗口、任何 goroutine 都可以调用
// showNotifications 把它打开。点开一条消息，帖子与评论回到主窗口里打开。
// 数据、长连接与已读落账在 notify.go。
//
// 界面整体走 Lobe UI：列表行、标签、按钮、勾选框、搜索框、转圈与空状态都用
// lobe-mygo 的组件，颜色取 TokensOf(c)，与主窗口及各附属窗口同一套观感。
// 消息行走扁平语言：无圆角、无整圈描边，只在行与行之间留一条细分隔线，悬停
// 铺浅底；不再用圆角描边卡片把每条消息框起来。只有铃铛的红点角标仍是自绘
// Box：它压在图标右下角且不能越出标题栏，像素几何被测试断言，Lobe 的 Badge
// 固定落在内容右上角，不能替代这一定位。

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

// notificationBell 是主窗口标题栏上的通知入口：Lobe 的 ActionIcon 铃铛 +
// 右下角的红色未读角标。图标、悬停态与打开态的底色都交给 ActionIcon，只有
// 红点角标仍是自绘 Box：它要压在图标右下角且不能越出按钮，像素几何被测试
// 断言，Lobe 的 Badge 固定落在内容右上角，替代不了这一定位。图标本身是装饰，
// 可访问名由 Label 提供（「通知（N 条未读）」），读屏与测试都按这个名字找它。
func (a *app) notificationBell(c *ui.Context) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	icon := lucide.Bell()
	if idx := notificationTabIndex(); idx >= 0 {
		icon = tabIcons[idx]
	}
	open := a.notificationWindowVisible()
	b := action.ActionIcon(c, icon, action.ActionIconProps{
		BlockSize: titleBarHeight, GlyphSize: 16, Active: open,
	})
	if count := unreadCountText(a.unread); count != "" && a.notifPrefs().badgeEnabled() {
		// 角标压在铃铛右下角、且留在按钮范围内：ActionIcon 的方块是
		// titleBarHeight×titleBarHeight，角标内缩 2 DIP 既不越出按钮，又贴着图标。
		// 外圈描边用按钮此刻的底色（静止透明、悬停/打开 FillSecondary），压住
		// 铃铛线条时留一条缝，红点不会和图标糊在一起。
		ring := t.Surface
		if open || b.Hovered() {
			ring = tk.FillSecondary
		}
		b.Children(func() {
			ui.Box(c).Absolute().Right(2).Bottom(2).
				MinWidth(14).Height(14).Padding(0, 3).Radius(999).Center().
				Border(1, ring).Background(t.Danger).Children(func() {
				display.Text(c, count, display.TextProps{Strong: true, FontSize: 9, Rows: 1}).
					TextColor(tk.White)
			})
		})
	}
	label := "通知"
	if n := a.unread; n > 0 {
		label = fmt.Sprintf("通知（%d 条未读）", n)
	}
	hint := "打开通知窗口"
	if a.unread > 0 {
		hint = fmt.Sprintf("%d 条未读，点开查看", a.unread)
	}
	b.Label(label).Tooltip(hint)
	b.OnClick(a.showNotifications)
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
		a.requestLogin("登录 2Libra 后才能查看「通知」。")
		return
	}
	a.refreshUnread()
	if a.autoLoad {
		a.loadFor(toastNotifications, idx, 1)
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
	win := a.newAppWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("通知"),
		Width:             notifWindowWidth,
		Height:            notifWindowHeight,
		MinWidth:          notifWindowMinWidth,
		MinHeight:         notifWindowMinHeight,
		StateKey:          "notifications",
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.notificationsWindowView)),
	}, toastNotifications)
	a.notifWin = win
	// 显示状态跟着窗口事件走：用户收起、最小化、关掉窗口时铃铛都要跟着变，
	// 不能只按「窗口对象还在不在」判断。
	win.OnShow(func() { a.setNotificationWindowShown(true) })
	win.OnHide(func() { a.setNotificationWindowShown(false) })
	win.OnClosed(func() {
		a.notifWin = nil
		a.setNotificationWindowShown(false)
		a.authGateWindowClosed(toastNotifications)
		a.accountWindowClosed(win)
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
	tk := lobetheme.TokensOf(c)
	// 消息行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	// 标记已读等操作的结果以 toast 显示在这个窗口里。
	defer a.drainToasts(c, a.claimsToasts(toastNotifications))
	// Esc 收起窗口而不是退出：消息中心随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.hideNotificationWindow)
	// Cmd/Ctrl+R 刷新当前列表：和浏览器一致的习惯，不必再去够右上角的小
	// 刷新按钮。窗口级的快捷键，输入框聚焦时也不耽误。
	c.OnShortcut(ui.Cmd, ui.KeyR, a.reloadNotifications)

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
		// 主窗口同一套画法。图标用 Lucide 铃铛，文字走 Lobe 的 display.Text。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				display.Icon(c, lucide.Bell(), display.IconProps{Size: display.IconSmall}).
					TextColor(tk.Primary)
				display.Text(c, "通知", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				if a.unread > 0 {
					// 未读数本身就是「只看未读」的快捷入口：点一下切到未读
					// 筛选，再点回到全部。比在下方分段条里找「未读」更快。
					toggle := action.Button(c, unreadText, action.ButtonProps{
						Size: lobetheme.Small, Variant: action.Text,
					})
					toggle.Tooltip("只看未读消息；再点一次看全部").OnClick(func() {
						if st.filter == notificationFilterUnread {
							a.setNotificationFilter(notificationFilterAll)
						} else {
							a.setNotificationFilter(notificationFilterUnread)
						}
					})
				} else {
					display.Text(c, unreadText, display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
				switch {
				case !a.notifPrefs().streamEnabled():
					// 关掉长连接是用户的选择，不该显示成「未连接」那样像坏了。
					display.Text(c, "· 轮询中", display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				case a.streamLive:
					display.Text(c, "· 已连接", display.TextProps{
						Type: display.TextSuccess, FontSize: 11, Rows: 1,
					})
				default:
					display.Text(c, "· 未连接", display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.notifWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", func() {
				if win := a.notifWin; win != nil {
					win.Close()
				}
			})
		})
		layout.Divider(c, layout.DividerProps{})

		ui.Column(c).Grow(1).MinHeight(0).Padding(10, 12).Gap(10).Children(func() {
			a.notificationActions(c, st)
			// 关键词搜索与分段筛选可以叠加：接口把 keyword 和 type / unread_only
			// 当独立参数处理。
			a.notificationSearchRow(c, st)
			// 分段筛选直接映射接口的 type / unread_only，切换后按新参数重拉。
			a.notificationFilterRow(c, st)
			layout.Divider(c, layout.DividerProps{})

			a.notificationsList(c, st)
		})
	})
	// 缺权限时就在这个窗口里弹授权：消息列表的请求是从这里发起的。
	a.authDialogFor(c, toastNotifications)
}

// notificationSearchRow 是通知窗口里的关键词搜索：在自己的通知里按内容
// 过滤（接口的 keyword 参数，1–50 字，忽略大小写的包含匹配）。只有点「搜索」
// 或回车才发请求——接口按次计配额，用户还在打字时不该把中间状态发出去。
func (a *app) notificationSearchRow(c *ui.Context, st *tabState) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		// field.Input 返回的是内层输入框，借 Column 的 stretch 把外层那一行
		// 也撑满，再给输入框本身接上无障碍名与长度上限。
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			in := composeInput(c, &st.keywordDraft, field.InputProps{
				Placeholder: "在通知里搜内容（1–50 字）",
				Size:        lobetheme.Small,
				Prefix:      lucide.Search(),
			})
			in.Label("搜索通知内容").MaxLength(notificationKeywordMax)
		})
		// 搜索按钮始终可点：applyNotificationKeyword 会拦掉相同关键词的重复
		// 请求，没必要在列表加载时把它画成淡蓝的禁用态。
		if action.Button(c, "搜索", action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Primary,
		}).Clicked() {
			a.applyNotificationKeyword(st, st.keywordDraft)
		}
		if st.keyword != "" {
			if action.Button(c, "清除搜索", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Text,
			}).Clicked() {
				a.applyNotificationKeyword(st, "")
			}
		}
	})
}

// applyNotificationKeyword 应用或清除关键词并从第一页重拉。服务端按关键词
// 重新查询并按时间倒序返回，旧的页码没有意义，所以整份列表清掉重来。
func (a *app) applyNotificationKeyword(st *tabState, keyword string) {
	keyword = strings.TrimSpace(keyword)
	if utf8.RuneCountInString(keyword) > notificationKeywordMax {
		a.toast(toastNotifications,
			fmt.Sprintf("关键词最多 %d 个字", notificationKeywordMax))
		return
	}
	if keyword == st.keyword {
		return
	}
	st.keyword = keyword
	st.keywordDraft = keyword
	resetNotificationQuickReply(st)
	st.items = nil
	st.clearNotificationSelected()
	st.loaded = false
	st.loading = false
	st.noMore = false
	st.err = ""
	st.moreErr = ""
	st.page = 1
	st.list.ScrollTo(0, ui.Start)
	if idx := notificationTabIndex(); idx >= 0 {
		a.loadFor(toastNotifications, idx, 1)
	}
}

// notificationActions 是窗口里的操作行：全部已读、已读本页、勾选批量、
// 刷新与原生通知开关。勾选批量只在真勾了消息时出现，避免空按钮占位。
func (a *app) notificationActions(c *ui.Context, st *tabState) {
	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		a.smallAction(c, "全部已读", st.markingAll || (a.unread == 0 && !hasUnreadItem(st.items)),
			func() { a.markAllNotificationsRead(toastNotifications) })
		// 「已读本页」= 把当前已加载的未读消息一次性标为已读；它与站点 Web 的
		// 同名操作一致，底层就是一次带 id 列表的 POST。
		loadedUnread := len(unreadNotificationIDs(st.items))
		loadedLabel := "已读本页"
		if loadedUnread > 0 {
			loadedLabel = fmt.Sprintf("已读本页 %d", loadedUnread)
		}
		a.smallAction(c, loadedLabel, st.loading || loadedUnread == 0,
			func() { a.markLoadedNotificationsRead(toastNotifications) })
		if n := notificationSelectionCount(st); n > 0 {
			a.smallAction(c, fmt.Sprintf("已读选中 %d", n), false,
				func() { a.markSelectedNotificationsRead(toastNotifications) })
		}
		a.smallRefreshButton(c, st.loading, func() { a.reloadNotifications() })
		switch {
		case a.notify == nil:
			display.Text(c, "原生通知不可用", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		case a.notifPrefs().notificationEnabled():
			a.smallAction(c, "原生通知：开", false, func() {
				a.saveNotifyPref(toastNotifications, func() error { return a.notifPrefs().setEnabled(false) })
			})
		default:
			a.smallAction(c, "原生通知：关", false, func() {
				a.saveNotifyPref(toastNotifications, func() error { return a.notifPrefs().setEnabled(true) })
			})
		}
	})
}

// smallAction 是各窗口复用的一枚轻量文字按钮：直接用 Lobe 的 Button（文字
// 变体自带透明底与悬停底色），不再给通用 ui.Button 手涂背景、描边与字号。
// 操作结果仍由 toast 报告，所以这里不留内联反馈。
func (a *app) smallAction(c *ui.Context, label string, disabled bool, onClick func()) {
	b := action.Button(c, label, action.ButtonProps{Size: lobetheme.Small, Variant: action.Text})
	b.Disabled(disabled)
	if b.Clicked() {
		onClick()
	}
}

// smallRefreshButton 是各窗口标题行共用的图标式「刷新」：平时只画一枚
// lucide.RefreshCw，鼠标扫过时给 Lobe ActionIcon 的浅底与说明；请求在途时
// 退成同尺寸的禁用转圈，避免连点。label 仍是「刷新」，读屏和点击测试照此命中。
func (a *app) smallRefreshButton(c *ui.Context, busy bool, onClick func()) {
	const label, tip = "刷新", "重新加载"
	if busy {
		b := ui.ButtonBase(c).Size(24, 24).Center().Disabled(true).
			Radius(6).Background(ui.Transparent).Border(1, ui.Transparent)
		b.Label(label).Tooltip(tip)
		b.Children(func() { lobeSpin(c, feedback.SpinProps{CustomSize: 14}) })
		return
	}
	b := action.ActionIcon(c, lucide.RefreshCw(), action.ActionIconProps{
		Size: lobetheme.Small,
	})
	b.Label(label).Tooltip(tip)
	b.OnClick(onClick)
}

// notificationFilterRow 用 Lobe 的 Segmented 画分段筛选：选项直接来自
// notificationFilters 的文案，绑定 st.filter；切换后走 setNotificationFilter
// 清掉旧页并按新参数重拉。Segmented 自己带滑动指示块与选中配色。
func (a *app) notificationFilterRow(c *ui.Context, st *tabState) {
	options := make([]string, len(notificationFilters))
	for i := range notificationFilters {
		options[i] = notificationFilters[i].Label
	}
	selected := st.filter
	action.Segmented(c, &selected, options, action.SegmentedProps{
		Size: lobetheme.Small, Block: true,
	}).Tooltip(notificationFilterAt(st.filter).Hint)
	if selected != st.filter {
		a.setNotificationFilter(selected)
	}
}

// notificationsList 画窗口里的消息区：加载中、错误、空状态与列表。
func (a *app) notificationsList(c *ui.Context, st *tabState) {
	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 28})
			display.Text(c, "正在加载通知…", display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "加载失败", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: st.err, Selectable: true,
			})
			ui.Row(c).Gap(8).Children(func() {
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.reloadNotifications()
				}
				if strings.Contains(st.err, "登录") || strings.Contains(st.err, "授权") {
					if action.Button(c, "去授权", action.ButtonProps{
						Size: lobetheme.Small, Variant: action.Primary,
					}).Clicked() {
						a.requestLoginFor(toastNotifications, "登录并授权后才能继续查看「通知」。")
					}
				}
			})
		})
	case len(st.items) == 0:
		// 关键词筛选没有结果与「本来就没有通知」是两回事：前者要给出关键词
		// 与清除入口，后者才该建议去别的分类看看。
		if st.keyword != "" {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Search(),
				Title:       "没有匹配的通知",
				Description: "没有找到包含「" + st.keyword + "」的消息。换个关键词，或者清除搜索。",
				Action: func() {
					if action.Button(c, "清除搜索", action.ButtonProps{
						Size: lobetheme.Small,
					}).Clicked() {
						a.applyNotificationKeyword(st, "")
					}
				},
			})
			return
		}
		meta := notificationFilterAt(st.filter)
		display.Empty(c, display.EmptyProps{
			Icon: lucide.Inbox(), Title: meta.EmptyTitle, Description: meta.EmptyHint,
			Action: func() {
				if st.filter != notificationFilterAll {
					if action.Button(c, "查看全部", action.ButtonProps{
						Size: lobetheme.Small,
					}).Clicked() {
						a.setNotificationFilter(notificationFilterAll)
					}
				}
			},
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
		}).Grow(1).MinHeight(0).Padding(4, 0)
		a.loadMoreFooter(c, st.moreErr, st.loading, func() {
			a.loadFor(toastNotifications, idx, st.page)
		})
		// 无限滚动：滚到底且还有下一页时自动加载。续拉失败后停在提示上，
		// 等用户点「重试」，避免每次重建都重发同一个失败的请求。
		if st.loaded && !st.loading && st.moreErr == "" && !st.noMore &&
			len(st.items) > 0 && st.list.AtEnd() {
			hasMore := st.totalPages == 0 || st.page < st.totalPages
			if hasMore {
				a.loadFor(toastNotifications, idx, st.page+1)
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
	resetNotificationQuickReply(st)
	st.items = nil
	st.clearNotificationSelected()
	st.loaded = false
	st.loading = false
	st.noMore = false
	st.err = ""
	st.moreErr = ""
	st.page = 1
	st.list.ScrollTo(0, ui.Start)
	a.loadFor(toastNotifications, idx, 1)
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
	resetNotificationQuickReply(st)
	st.loaded = false
	st.items = nil
	st.clearNotificationSelected()
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.loadFor(toastNotifications, idx, 1)
}

// resetNotificationQuickReply 收起快捷回复并丢掉草稿：列表要被整份替换时，
// 旧消息卡片上的输入框不该继续挂在新列表里。
func resetNotificationQuickReply(st *tabState) {
	if st == nil {
		return
	}
	st.quickReplyOpen = ""
	st.quickReplyDraft = ""
	st.quickReplyFocus = false
}

// notificationRow 一条消息就是一行和列表同宽的扁平列表项：勾选框、未读点与
// 「标为已读」都在行内，表态的表情与人名写在同一行。整行是一张可点的 Lobe
// Block，点开的是这条消息的目标。
func (a *app) notificationRow(c *ui.Context, it Item) {
	n := it.Notif
	if n == nil {
		return
	}
	tk := lobetheme.TokensOf(c)
	v := notificationViewOf(n)
	unread := !n.IsRead
	st := a.tabs[notificationTabIndex()]
	reading := st != nil && st.notificationReading(n.ID)

	// 通知行走 Lobe 的扁平列表语言：无圆角、无整圈描边，只在行与行之间留
	// 一条细分隔线；悬停铺 FillTertiary，整行仍是一张可点的 Block。正文不再
	// 被卡片边框切碎，与主页列表行、「我的」窗口同一套视觉语言。
	card := layout.Block(c, layout.BlockProps{Clickable: true, Variant: layout.BlockBorderless}).
		FillWidth().Padding(a.rowPadY(), 12).Gap(a.rowGap()).Radius(0).
		BorderWidth(0, 0, 1, 0).BorderColor(tk.BorderSecondary)
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(a.rowGap()).Children(func() {
			// 行头：勾选框（仅未读）、未读点、类型、时间，未读时右上角是
			// 「标为已读」。这些都在行内，每行不再为它们各留一条竖带——旧
			// 写法把勾选框和「已读」摆在行外面，正文被挤成窄窄一条。
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				if unread && st != nil {
					was := st.notificationChecked(n.ID)
					checked := was
					choice.Checkbox(c, &checked, "", choice.CheckboxProps{Size: 14}).
						Shrink(0).Label("勾选这条消息").Tooltip("勾选后可批量标记已读")
					if checked != was {
						setNotificationSelected(st, n.ID, checked)
					}
				}
				if unread {
					feedback.Badge(c, feedback.BadgeProps{Color: tk.Primary}, nil).Shrink(0)
				}
				display.Tag(c, v.TypeLabel, display.TagProps{
					Size: lobetheme.Small, Color: notificationTagColor(v.Tone),
					Variant: display.TagFilled, Round: true,
				})
				if v.ActorIsAlias {
					display.Text(c, "匿名", display.TextProps{
						Type: display.TextSecondary, FontSize: tk.FontSizeSM, Rows: 1,
					})
				}
				if when := relativeTime(n.CreatedAt); when != "" {
					display.Text(c, when, display.TextProps{
						Type: display.TextSecondary, FontSize: tk.FontSizeSM, Rows: 1,
					})
				}
				ui.Spacer(c).Grow(1)
				if unread {
					a.notificationQuickReplyAction(c, st, n, v)
					a.notificationReadAction(c, reading, func() { a.markNotificationRead(toastNotifications, n.ID) })
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
					Pro:      v.ActorIsPro,
				})
			default:
				action := v.Action
				if v.Kind == "reaction" {
					// 表态消息但服务端没给表情和人名：补一个主语，句子不能只剩动词。
					action = "有人" + action
				}
				display.Text(c, action, display.TextProps{Strong: true, FontSize: 13, Rows: 2})
			}
			if v.TargetTitle != "" {
				display.Text(c, "《"+v.TargetTitle+"》", display.TextProps{
					FontSize: tk.FontSize, Rows: 1,
				})
			}
			if v.Excerpt != "" {
				display.Text(c, v.Excerpt, display.TextProps{
					FontSize: tk.FontSize, Rows: 3,
				})
			}
			if v.Detail != "" {
				display.Text(c, v.Detail, display.TextProps{
					Type: notificationDetailTextType(v.DetailTone), FontSize: tk.FontSize, Rows: 2,
				})
			}
			if len(v.Meta) > 0 {
				display.Text(c, strings.Join(v.Meta, " · "), display.TextProps{
					Type: display.TextSecondary, FontSize: tk.FontSizeSM, Rows: 2,
				})
			}
			if st != nil && st.quickReplyOpen == n.ID {
				a.notificationQuickReplyEditor(c, st, n, v)
			}
		})
	})
	// 可访问名说明这一下点开的是什么：读屏读得到，测试也按它取卡片的位置。
	card.Label("打开消息：" + v.Action)
	card.OnClick(func() {
		// 未读消息把 id 带过去，落地到目标评论后再标为已读；已读的消息不用
		// 再发一次已读请求。
		id := ""
		if unread {
			id = n.ID
		}
		a.openNotificationTarget(v.TargetURL, id)
	})
}

// notificationReadAction 是未读消息卡片右上角的「标为已读」。它只是卡片里的
// 一个小动作，不占一整列：Lobe 的文字按钮，悬停换强调色，点它不会连带打开消息。
func (a *app) notificationReadAction(c *ui.Context, busy bool, onClick func()) {
	label := "标为已读"
	if busy {
		label = "正在标记…"
	}
	b := action.Button(c, label, action.ButtonProps{Size: lobetheme.Small, Variant: action.Text})
	b.Disabled(busy).Tooltip("只把这一条标为已读")
	if b.Clicked() {
		onClick()
	}
}

/* ------------------------------------------------------------------
   通知快捷回复
---------------------------------------------------------------- */

// notificationQuickReplyAction 是未读消息卡片右上角的「快捷回复」入口，与
// 「标为已读」同排，不额外占一行。只对未读消息显示：已读消息已经看过，不再
// 需要从通知里直接回一句。v.CanReply 由 notificationQuickReplyTarget 判定；
// 缺帖子定位、或三级评论缺挂载点的消息不出现入口。
func (a *app) notificationQuickReplyAction(c *ui.Context, st *tabState, n *notification, v notificationView) {
	if st == nil || n == nil || !v.CanReply || v.ReplyShortID == "" || st.quickReplyOpen == n.ID {
		return
	}
	b := action.Button(c, "快捷回复", action.ButtonProps{
		Size: lobetheme.Small, Variant: action.Text,
	})
	b.Tooltip("直接在这里回复这条消息")
	if b.Clicked() {
		if st.quickReplyPosting {
			a.toast(toastNotifications, "上一条回复还在发送，请稍候")
			return
		}
		st.quickReplyOpen = n.ID
		st.quickReplyDraft = ""
		st.quickReplyFocus = true
	}
}

// notificationQuickReplyEditor 是展开后的单行回复：输入框与取消 / 发送同一行，
// 回车发送，只为快速回一句，不铺开评论编辑器。
func (a *app) notificationQuickReplyEditor(c *ui.Context, st *tabState, n *notification, v notificationView) {
	if st == nil || n == nil || !v.CanReply || v.ReplyShortID == "" {
		return
	}
	posting := st.quickReplyPosting
	placeholder := "写一条回复…"
	if v.ActorName != "" {
		placeholder = "回复 @" + v.ActorName + "…"
	}
	// 输入框的 key 固定成这条消息：展开后上下方的兄弟节点变化，焦点不会漂到
	// 别的控件上。
	c.Key("notification-quick-reply-" + n.ID)
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			in := composeInput(c, &st.quickReplyDraft, field.InputProps{
				Placeholder: placeholder, Size: lobetheme.Small,
			})
			in.Label("快捷回复内容").Bind(&st.quickReplyField)
			if st.quickReplyFocus {
				st.quickReplyField.Focus(c)
				st.quickReplyFocus = false
			}
			st.quickReplyField.OnShortcut(c, 0, ui.KeyEnter, func() {
				a.submitNotificationQuickReply(st, n, v)
			})
		})
		if action.Button(c, "取消", action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Text,
		}).Disabled(posting).Clicked() {
			st.quickReplyOpen = ""
			st.quickReplyDraft = ""
			st.quickReplyFocus = false
		}
		if a.auth == nil || !a.auth.loggedIn() {
			if action.Button(c, "登录", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Primary,
			}).Tooltip("登录后回复").Disabled(posting).Clicked() {
				a.submitNotificationQuickReply(st, n, v)
			}
			return
		}
		sendLabel := "发送"
		if posting {
			sendLabel = "发送中"
		}
		if action.Button(c, sendLabel, action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Primary,
		}).Disabled(posting).Clicked() {
			a.submitNotificationQuickReply(st, n, v)
		}
	})
}

// submitNotificationQuickReply 发一次快捷回复。未登录时先走授权弹窗，登录
// 成功后由用户自己再点发送；缺 write:comment 时同样走授权弹窗。
// 网络结果经 publish 回到 UI 线程，失败保留草稿与输入框。
func (a *app) submitNotificationQuickReply(st *tabState, n *notification, v notificationView) {
	if st == nil || n == nil || st.quickReplyPosting {
		return
	}
	content := strings.TrimSpace(st.quickReplyDraft)
	if content == "" {
		a.toast(toastNotifications, "回复内容不能为空")
		return
	}
	if !v.CanReply || v.ReplyShortID == "" {
		a.toast(toastNotifications, "这条消息暂时无法直接回复，请点开帖子回复")
		return
	}
	if a.auth == nil || !a.auth.loggedIn() {
		a.requestLoginFor(toastNotifications, "登录后才能回复消息。")
		return
	}
	if a.client == nil {
		return
	}
	shortID, target, id := v.ReplyShortID, v.ReplyTarget, n.ID
	st.quickReplyPosting = true
	// 与详情页的评论提交同一个超时；App 的 client 走配置的代理，不另建客户端。
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		_, err := a.client.createReply(ctx, shortID, content, target)
		a.publish(func() {
			st.quickReplyPosting = false
			if err != nil {
				a.handleAuthErrorFor(toastNotifications, err)
				a.toast(toastNotifications, "回复失败："+friendlyError(err))
				return
			}
			if st.quickReplyOpen == id {
				st.quickReplyOpen = ""
				st.quickReplyDraft = ""
				st.quickReplyFocus = false
			}
			if v.ActorName != "" {
				a.toast(toastNotifications, "已回复 @"+v.ActorName)
			} else {
				a.toast(toastNotifications, "回复已发送")
			}
			// 回复本身就是对这条消息的处理：顺手标为已读，未读点与角标随之
			// 落账（markNotificationRead 自带在途去重）。
			if !n.IsRead {
				a.markNotificationRead(toastNotifications, id)
			}
		})
	}()
}

// notificationReactionLine 画表态消息的「用了什么表情、都有谁」：表情合并成
// 一行胶囊，不同表情不再各占一组括号；人名去重后只出现一次——同一个人用了两
// 种表情也只写一个名字——名字之间用顿号相连，超出上限的人用「等 N 人」收尾。
// 这一行后面接着的就是这条消息要说什么（「表态了你的帖子」）。
func (a *app) notificationReactionLine(c *ui.Context, o notificationReactionOverview, actionText string) {
	tk := lobetheme.TokensOf(c)
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
				display.Text(c, "、", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			}
			a.userTag(c, userTagSpec{
				Name:   u.Name,
				Avatar: u.Avatar,
				Label:  "@" + u.Name,
				Size:   11,
				Color:  tk.TextSecondary,
				Badges: u.Badges,
				Pro:    u.IsPro,
			})
		}
		if others := notificationReactionOthers(o, len(names)); others > 0 {
			display.Text(c, fmt.Sprintf("等 %d 人", others), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		}
		display.Text(c, actionText, display.TextProps{FontSize: 12, Rows: 1})
	})
}

// notificationReactionChip 是一枚表情胶囊：与详情页的表态摘要是同一套圆角
// 胶囊。只有一次表态时不写数字，「👍 1」比「👍」更吵。
func (a *app) notificationReactionChip(c *ui.Context, g notificationReactionGroup) {
	label := g.Emoji
	if g.Count > 1 {
		label = fmt.Sprintf("%s %d", g.Emoji, g.Count)
	}
	display.Tag(c, label, display.TagProps{
		Size: lobetheme.Small, Variant: display.TagOutlined, Round: true,
	}).Label(fmt.Sprintf("%s，%d 次表态", g.Emoji, g.Count))
}

// notificationTagColor 把消息语气映射成 Lobe Tag 的状态色，类型标签直接用
// 组件自己的配色，不再给通用文本手涂颜色（空值是中性色）。
func notificationTagColor(tone string) display.TagColor {
	switch tone {
	case "success":
		return display.TagSuccess
	case "warning":
		return display.TagWarning
	case "danger":
		return display.TagError
	case "primary", "info":
		return display.TagInfo
	}
	return display.TagColor("")
}

// notificationDetailTextType 把补充文案的语气映射成 Lobe 文本的类型，
// 颜色由 display.Text 按 token 给出。
func notificationDetailTextType(tone string) display.TextType {
	switch tone {
	case "success":
		return display.TextSuccess
	case "warning":
		return display.TextWarning
	case "danger":
		return display.TextDanger
	case "primary", "info":
		return display.TextInfo
	}
	return display.TextSecondary
}
