package main

import (
	"context"
	"errors"
	"net/url"
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
	opts := mygo.NotificationOptions{
		ID:       n.ID,
		Title:    n.Title,
		Subtitle: n.Subtitle,
		Body:     n.Body,
	}
	// 一个账号一叠：macOS 的通知中心按账号分开堆，两个账号的消息不会混在
	// 一起（其它平台忽略这个字段）。
	if n.AccountID != "" {
		opts.Group = "t2-account:" + n.AccountID
	}
	return mygo.NewNotification(opts).Show()
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
		// 原生通知只播报最新一页的未读消息：不按关键词过滤，用户设的搜索条件
		// 只影响窗口里的列表。
		page, err := a.client.fetchNotifications(ctx, 1, notificationFilterAll, "")
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
			if len(fresh) == 0 {
				return
			}
			// 新消息里可能就有金币变动（收到表态、收到打赏、评论被置顶、内容
			// 检定等），emit 一次全局金币刷新；合并窗口会把同一批消息收敛成
			// 一次请求。
			a.coinsChanged(coinEventIncoming)
			// 一次事件最多弹 3 条，避免同一批消息刷屏。
			if len(fresh) > 3 {
				fresh = fresh[:3]
			}
			notices := make([]nativeNotice, 0, len(fresh))
			for _, n := range fresh {
				notices = append(notices, a.notificationNative(n))
			}
			if !a.nativeNotify {
				return
			}
			a.showNotices(notices)
		})
	}()
}

// notificationNative 给一条消息补上「这是哪个账号收到的」再交给系统通知。
// 单账号不写账号名：窗口标题已经说明身份，副标题只会多占一行。
func (a *app) notificationNative(n *notification) nativeNotice {
	return notificationNative(n, a.accountID, a.noticeAccountName())
}

// noticeAccountName 是系统通知副标题里的账号名。没有账号（未登录窗口、旧版
// 单账号会话）时返回空串，通知里就不写这一行。
func (a *app) noticeAccountName() string {
	if a.accountID == "" || !a.multiAccount() {
		return ""
	}
	// 名字取这个窗口自己的会话：通知说的是这份令牌收到的消息。
	if a.auth != nil {
		if name := a.auth.username(); name != "" {
			return name
		}
	}
	if a.book != nil {
		if acc := a.book.account(a.accountID); acc != nil {
			return acc.name()
		}
	}
	return ""
}

// noticeTarget 是这条系统通知的点击目标。系统通知的点击回调只带 id，靠这
// 个映射落回对应的帖子 / 评论；映射已经清掉（窗口关过、进程重启、通知太旧）
// 时返回空串，由账号册决定退回哪儿（见 openNotice）。
func (a *app) noticeTarget(id string) string {
	if id == "" {
		return ""
	}
	return a.noticeTargets[id]
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
// 通知窗口只是入口：点开帖子时主窗口照常切到详情页，通知窗口自己不关 ——
// 读完后还能回到原来的消息列表与滚动位置。
//
// notifID 是点开的这条消息：目标带评论定位时先只记账，等那层楼出现在评论区
// 可视区再标为已读（见 markNotificationSeen）。目标没有评论定位（表态了你的
// 帖子、收藏了你的帖子…）时不动已读状态；交给系统浏览器的目标同样不动，
// 用户还没在应用里看到内容。
func (a *app) openNotificationTarget(target, notifID string) {
	if target == "" {
		return
	}
	it, inApp := parseInAppTopicLink(target)
	if !inApp {
		a.openExternal(target)
		return
	}
	a.openDetail(it)
	if notifID != "" && it.CommentID != "" && a.detail != nil {
		a.detail.notifID = notifID
		a.detail.notifCommentID = it.CommentID
	}
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
	a.loadFor(toastNotifications, idx, 1)
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
	// 在站点上签到、打赏、开盲盒、兑换后金币会变，而这类操作不会发通知：
	// 窗口回到前台时先把标题栏的金币数校准一次，定时器不在也补上；金币
	// 窗口的数据已经加载过的话，一并走全局金币刷新事件。
	a.startCoinTagWatch()
	if a.coinPageLoaded() {
		a.coinsChanged(coinEventForeground)
	}
}

// markNotificationRead marks one message read: the row's 「已读」 button.
// owner 是发起这次已读的窗口：缺权限时的授权弹窗回到那里。
func (a *app) markNotificationRead(owner toastTarget, id string) {
	a.markNotificationsRead(owner, []string{id})
}

// markLoadedNotificationsRead marks every unread message that is already on
// screen. 站内 Web 的「已读本页」就是这个语义；超过 100 条时由数据层分片。
func (a *app) markLoadedNotificationsRead(owner toastTarget) {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	a.markNotificationsRead(owner, unreadNotificationIDs(a.tabs[idx].items))
}

// markSelectedNotificationsRead marks the messages the user ticked.
func (a *app) markSelectedNotificationsRead(owner toastTarget) {
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	a.markNotificationsRead(owner, selectedNotificationIDs(a.tabs[idx]))
}

// publishWritesOK 报告此刻能不能发「写」请求（按条已读、全部已读）。它不看
// autoLoad：autoLoad 只该拦住加载，不该吞掉一个已经发生的界面动作。无头测试
// 里 autoLoad 关着、默认客户端仍是线上地址直连 http.DefaultClient，所以额外
// 要求接口指向本地假服务，避免测试把已读写进线上账号。
func (a *app) publishWritesOK() bool {
	if a.auth == nil || !a.auth.loggedIn() {
		return false
	}
	if a.autoLoad {
		return true
	}
	return a.client != nil && isLocalTestBase(a.client.base())
}

// isLocalTestBase 判断接口地址是不是本地回环地址（无头测试的 httptest 服务）。
func isLocalTestBase(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// markNotificationsRead posts one 「按条已读」 request for the whole batch.
// Repeated clicks on a message already in flight are coalesced, an empty list
// never reaches the server (id: [] is a 400), and the local list is updated
// only after the server accepted the batch.
func (a *app) markNotificationsRead(owner toastTarget, ids []string) {
	if !a.publishWritesOK() {
		return
	}
	ids = cleanNotificationIDs(ids)
	a.markNotificationsReadConfirmed(owner, ids)
}

// markNotificationsReadConfirmed 是已经确认可以发写请求之后的提交路径：点行
// 标已读、「已读本页」、「已读选中」，以及详情页里「看到了这条消息」都从这里
// 走，共用同一张在途表：同一条消息在请求落地之前被两个入口先后提交时只发一次，
// 本地也只扣一次未读数。owner 是发起这次已读的窗口。
func (a *app) markNotificationsReadConfirmed(owner toastTarget, ids []string) {
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
				a.handleAuthErrorFor(owner, err, func() { a.markNotificationsRead(owner, batch) })
				a.toast(toastNotifications, "标记已读失败："+friendlyError(err))
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
	var fresh []string
	for i := range st.items {
		n := st.items[i].Notif
		if n == nil || !read[n.ID] {
			continue
		}
		if !n.IsRead {
			// 本地已经是已读的消息（两个入口先后提交过同一条）不重复扣未读数。
			n.IsRead = true
			fresh = append(fresh, n.ID)
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
		st.total = max(0, st.total-len(fresh))
	}
	if len(fresh) > 0 {
		a.unread = max(0, a.unread-len(fresh))
	}
	a.refreshUnread()
}

func (a *app) markAllNotificationsRead(owner toastTarget) {
	if !a.publishWritesOK() {
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := a.client.markAllNotificationsRead(ctx)
		a.publish(func() {
			st.markingAll = false
			if err != nil {
				a.handleAuthErrorFor(owner, err, func() { a.markAllNotificationsRead(owner) })
				a.toast(toastNotifications, "全部已读失败："+friendlyError(err))
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
