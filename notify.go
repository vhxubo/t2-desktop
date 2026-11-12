package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/egoist/mygo"
)

// noticeTargetKeep 是「原生通知 id → 目标地址」映射的容量上限。超了就整体
// 清空：系统通知可能停留很久，但用户最可能点的是最近几条；更早的点击会
// 回到站内消息中心。
const noticeTargetKeep = 200

// showNativeNotice is the production notifier: a real desktop notification
// through the platform (Windows notification-area balloon, macOS Notification
// Center, Linux notification daemon). It is installed only once the app is
// ready, so headless UI tests never touch the platform loop.
//
// 点击由 App.OnNotificationClick 统一接管（见 main.go）：那个回调对「上次
// 运行留下、本次进程里没有对应 Notification 对象」的消息同样会触发，
// 所以这里不再给单条通知挂 OnClick，否则同一次点击会被处理两次。
func showNativeNotice(n nativeNotice) error {
	note := mygo.NewNotification(mygo.NotificationOptions{
		ID:    n.ID,
		Title: n.Title,
		Body:  n.Body,
	})
	return note.Show()
}

// notificationTabIndex 定位通知的数据源。通知不再占据侧边栏的页面：它留在
// tabSpecs 里只是因为列表、分页与筛选沿用同一套加载逻辑，入口是标题栏的
// 铃铛与通知弹窗（见 notifications_ui.go）。
func notificationTabIndex() int {
	for i, s := range tabSpecs {
		if s.Kind == kindNotification {
			return i
		}
	}
	return -1
}

// startNotificationWatch opens the single SSE connection for the signed-in
// user. It is idempotent: repeated calls (login, app ready) reuse it.
func (a *app) startNotificationWatch() {
	if !a.autoLoad || a.watchCancel != nil || !a.auth.loggedIn() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.watchCancel = cancel
	a.watchGen++
	gen := a.watchGen
	a.notifSeen = map[string]bool{}
	a.notifBaseline = false
	a.notifPollGen = 0

	handlers := notificationStreamHandlers{
		onReady: func(unread *int) {
			a.publish(func() {
				if a.watchGen != gen {
					return
				}
				a.streamLive = true
				if unread != nil {
					a.unread = max(0, *unread)
					a.unreadLoading = false
				} else {
					a.refreshUnread()
				}
				// 握手后的第一次拉取是基线：只记录历史消息，不补弹通知。
				a.pollNotifications()
				a.reloadNotificationTab()
			})
		},
		onNotification: func(unread *int) {
			a.publish(func() {
				if a.watchGen != gen {
					return
				}
				if unread != nil {
					a.unread = max(0, *unread)
					a.unreadLoading = false
				} else {
					a.refreshUnread()
				}
				a.scheduleNotificationSync()
			})
		},
		onReconnect: func() {
			a.publish(func() {
				if a.watchGen == gen {
					a.streamLive = false
				}
			})
		},
		onError: func(err error) {
			a.publish(func() {
				if a.watchGen != gen {
					return
				}
				a.streamLive = false
				// 授权问题已经由 auth 层清理登录态；其它错误静默退避重连，
				// 不打扰正在读列表的用户。
				var se *notificationStreamError
				if errors.As(err, &se) && se.kind == streamErrAuth {
					a.unread = 0
				}
			})
		},
	}

	go func() {
		a.client.watchNotifications(ctx, handlers)
		a.publish(func() {
			if a.watchGen != gen {
				return
			}
			a.streamLive = false
			a.watchCancel = nil
		})
	}()
}

func (a *app) stopNotificationWatch() {
	a.watchGen++
	if a.watchCancel != nil {
		a.watchCancel()
		a.watchCancel = nil
	}
	a.streamLive = false
	if a.notifTimer != nil {
		a.notifTimer.Stop()
		a.notifTimer = nil
	}
}

// refreshUnread calibrates the badge. It is a guarded job: concurrent calls
// share the in-flight request, and a newer generation wins.
func (a *app) refreshUnread() {
	if !a.autoLoad {
		return
	}
	if !a.auth.loggedIn() {
		a.unread = 0
		a.unreadLoading = false
		return
	}
	if a.unreadLoading {
		return
	}
	a.unreadLoading = true
	a.unreadGen++
	gen := a.unreadGen
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	go func() {
		defer cancel()
		count, err := a.client.fetchUnreadNotificationCount(ctx)
		a.publish(func() {
			if a.unreadGen != gen {
				return
			}
			a.unreadLoading = false
			if err != nil {
				// 令牌问题就隐藏角标，不影响主列表；网络错误保留上次的值。
				var ae *apiError
				if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403) {
					a.unread = 0
				}
				return
			}
			a.unread = max(0, count)
		})
	}()
}

// scheduleNotificationSync merges bursts of notification_update frames into
// one refresh, like the reference extension's 300ms debounce.
func (a *app) scheduleNotificationSync() {
	if a.notifTimer != nil {
		return
	}
	gen := a.watchGen
	a.notifTimer = time.AfterFunc(400*time.Millisecond, func() {
		a.publish(func() {
			if a.watchGen != gen {
				return
			}
			a.notifTimer = nil
			if !a.auth.loggedIn() {
				return
			}
			a.pollNotifications()
			a.reloadNotificationTab()
		})
	})
}

// pollNotifications fetches the newest messages, records the ones never seen
// and shows a native notification for the unread ones. The first successful
// poll is the baseline: messages that were already unread when the app
// started are recorded without being announced again.
func (a *app) pollNotifications() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	a.notifPollGen++
	gen := a.notifPollGen
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		page, err := a.client.fetchNotifications(ctx, 1, notificationFilterAll)
		if err != nil {
			return // 静默失败：角标与列表保持原样，SSE 会继续推进
		}
		a.publish(func() {
			if a.notifPollGen != gen || !a.auth.loggedIn() {
				return
			}
			if a.notifSeen == nil {
				a.notifSeen = map[string]bool{}
			}
			// 进程长跑时把「已播报过」的集合收敛到当前页：已经滚出视野的
			// 消息不会被重新弹，集合也不会无限增长。
			if len(a.notifSeen) > 1000 {
				seen := make(map[string]bool, len(page.Items))
				for i := range page.Items {
					seen[page.Items[i].ID] = true
				}
				a.notifSeen = seen
			}
			var fresh []*notification
			for i := range page.Items {
				n := &page.Items[i]
				if a.notifSeen[n.ID] {
					continue
				}
				a.notifSeen[n.ID] = true
				if n.IsRead {
					continue
				}
				fresh = append(fresh, n)
			}
			if !a.notifBaseline {
				a.notifBaseline = true
				return
			}
			if len(fresh) == 0 || !a.nativeNotify {
				return
			}
			// 一次事件最多弹 3 条，避免同一批消息刷屏。
			if len(fresh) > 3 {
				fresh = fresh[:3]
			}
			notices := make([]nativeNotice, 0, len(fresh))
			for _, n := range fresh {
				notices = append(notices, notificationNative(n))
			}
			a.showNotices(notices)
		})
	}()
}

// openNotificationNotice handles a click on a native notification. The ID is
// the one the platform kept, so a system notification that outlived the app
// process still resolves to its target; when the mapping is gone (冷启动后
// 系统才把点击交回来), fall back to the site's message center.
func (a *app) openNotificationNotice(id string) {
	a.openNotificationTarget(a.notificationNoticeTarget(id))
}

// notificationNoticeTarget is the click target of one native notification.
func (a *app) notificationNoticeTarget(id string) string {
	if target := a.noticeTargets[id]; target != "" {
		return target
	}
	return siteBaseURL + "/notifications"
}

// rememberNoticeTarget keeps the click target of a shown notification. The
// map is bounded: clicks on older notifications fall back to the site page
// instead of growing it without limit.
func (a *app) rememberNoticeTarget(id, target string) {
	if id == "" || target == "" {
		return
	}
	if a.noticeTargets == nil {
		a.noticeTargets = map[string]string{}
	}
	if len(a.noticeTargets) >= noticeTargetKeep {
		clear(a.noticeTargets)
	}
	a.noticeTargets[id] = target
}

// openNotificationTarget opens the message's target: a 2Libra post or comment
// goes back to the main window, which is brought to the front with the post
// (and the comment anchor) opened; anything else goes to the system browser.
// 通知窗口只是入口：点开帖子后收起它，把画面交回主窗口。
func (a *app) openNotificationTarget(target string) {
	if target == "" {
		return
	}
	it, inApp := parseInAppTopicLink(target)
	if !inApp {
		a.openExternal(target)
		return
	}
	a.openDetail(it)
	a.hideNotificationWindow()
	a.showMainWindow()
}

func (a *app) showNotices(notices []nativeNotice) {
	if len(notices) == 0 || a.notify == nil {
		return
	}
	for _, n := range notices {
		a.rememberNoticeTarget(n.ID, n.Target)
	}
	if a.win == nil {
		// 无头测试：同步送达，便于断言。
		for _, n := range notices {
			_ = a.notify(n)
		}
		return
	}
	// 平台通知可能等待授权，不能卡住 UI 线程。
	go func() {
		for _, n := range notices {
			_ = a.notify(n)
		}
	}()
}

// reloadNotificationTab silently refreshes the open message list after an
// event. Unloaded / loading tabs are left alone: ensureLoaded and the user's
// own actions take care of them.
func (a *app) reloadNotificationTab() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if !st.loaded || st.loading {
		return
	}
	a.load(idx, 1)
}

// onBecameActive calibrates the badge when the window returns to the front.
// Messages read on the site produce no notification_update, so this is what
// makes the badge follow along.
func (a *app) onBecameActive() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	a.refreshUnread()
	a.reloadNotificationTab()
}

// markNotificationRead marks one message read: the row's 「已读」 button.
func (a *app) markNotificationRead(id string) {
	a.markNotificationsRead([]string{id})
}

// markLoadedNotificationsRead marks every unread message that is already on
// screen. 站内 Web 的「已读本页」就是这个语义；超过 100 条时由数据层分片。
func (a *app) markLoadedNotificationsRead() {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	a.markNotificationsRead(unreadNotificationIDs(a.tabs[idx].items))
}

// markSelectedNotificationsRead marks the messages the user ticked.
func (a *app) markSelectedNotificationsRead() {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	a.markNotificationsRead(selectedNotificationIDs(a.tabs[idx]))
}

// markNotificationsRead posts one 「按条已读」 request for the whole batch.
// Repeated clicks on a message already in flight are coalesced, an empty list
// never reaches the server (id: [] is a 400), and the local list is updated
// only after the server accepted the batch.
func (a *app) markNotificationsRead(ids []string) {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	ids = cleanNotificationIDs(ids)
	if len(ids) == 0 {
		return
	}
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if st.reading == nil {
		st.reading = map[string]bool{}
	}
	pending := make([]string, 0, len(ids))
	for _, id := range ids {
		if st.reading[id] {
			continue
		}
		pending = append(pending, id)
	}
	if len(pending) == 0 {
		return
	}
	for _, id := range pending {
		st.reading[id] = true
	}
	st.actionErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := a.client.markNotificationsRead(ctx, pending)
		a.publish(func() {
			for _, id := range pending {
				delete(st.reading, id)
			}
			if err != nil {
				// 缺 read:me 或登录失效：走全局授权，回调成功后把这批 id 重发
				// 一次，已读状态照样落账。
				batch := append([]string(nil), pending...)
				a.handleAuthError(err, func() { a.markNotificationsRead(batch) })
				st.actionErr = "标记已读失败：" + friendlyError(err)
				return
			}
			a.applyNotificationRead(st, pending)
			if a.onNotificationsRead != nil {
				a.onNotificationsRead(append([]string(nil), pending...))
			}
		})
	}()
}

// applyNotificationRead mirrors an accepted batch locally: the rows become
// read, 「未读」 filter drops them, and the badge follows immediately instead
// of waiting for the next unread-count poll.
func (a *app) applyNotificationRead(st *tabState, ids []string) {
	read := make(map[string]bool, len(ids))
	for _, id := range ids {
		read[id] = true
	}
	fresh := 0
	for i := range st.items {
		n := st.items[i].Notif
		if n == nil || !read[n.ID] {
			continue
		}
		if !n.IsRead {
			n.IsRead = true
			fresh++
		}
		delete(st.selected, n.ID)
	}
	if st.filter == notificationFilterUnread {
		out := st.items[:0]
		for _, it := range st.items {
			if it.Notif != nil && read[it.Notif.ID] {
				continue
			}
			out = append(out, it)
		}
		st.items = out
		st.total = max(0, st.total-fresh)
	}
	if fresh > 0 {
		a.unread = max(0, a.unread-fresh)
	}
	a.refreshUnread()
}

func (a *app) markAllNotificationsRead() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if st.markingAll {
		return
	}
	st.markingAll = true
	st.actionErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := a.client.markAllNotificationsRead(ctx)
		a.publish(func() {
			st.markingAll = false
			if err != nil {
				a.handleAuthError(err, func() { a.markAllNotificationsRead() })
				st.actionErr = "全部已读失败：" + friendlyError(err)
				return
			}
			st.selected = nil
			for i := range st.items {
				if n := st.items[i].Notif; n != nil {
					n.IsRead = true
				}
			}
			if st.filter == notificationFilterUnread {
				st.items = nil
				st.total = 0
			}
			a.unread = 0
			a.unreadLoading = false
		})
	}()
}

// unreadNotificationIDs collects the unread ids of the loaded rows in display
// order; duplicates cannot occur because a row is one message.
func unreadNotificationIDs(items []Item) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if it.Notif != nil && !it.Notif.IsRead && it.Notif.ID != "" {
			ids = append(ids, it.Notif.ID)
		}
	}
	return ids
}

// selectedNotificationIDs is the ticked set, sorted for a stable request
// order. Ticks that no longer match a loaded unread row are dropped.
func selectedNotificationIDs(st *tabState) []string {
	if len(st.selected) == 0 {
		return nil
	}
	loaded := make(map[string]bool, len(st.items))
	for _, it := range st.items {
		if it.Notif != nil && !it.Notif.IsRead {
			loaded[it.Notif.ID] = true
		}
	}
	ids := make([]string, 0, len(st.selected))
	for id, on := range st.selected {
		if on && loaded[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// notificationSelectionCount is what the 「已读选中」 button shows.
func notificationSelectionCount(st *tabState) int {
	return len(selectedNotificationIDs(st))
}

func setNotificationSelected(st *tabState, id string, on bool) {
	if st == nil || id == "" {
		return
	}
	if on {
		if st.selected == nil {
			st.selected = map[string]bool{}
		}
		st.selected[id] = true
		return
	}
	delete(st.selected, id)
}
