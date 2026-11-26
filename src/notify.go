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

// notificationFallbackInterval 是关掉实时长连接后的轮询间隔。SSE 正常时一条
// 连接就够；关掉后没有推送，只能定时拉未读数与新消息。一分钟既能及时发现
// 新消息，又不至于给 API 太多压力（限流闸门仍然共用）。
const notificationFallbackInterval = 60 * time.Second

// nativeNotifier 返回当前可用的平台通知函数。它在应用就绪后才装上，晚于
// app 构建，所以窗口与发送路径都实时查 desktop，不能拿构造时的快照。
// 无头测试没有进程级单例时返回 nil，不会触碰真实系统通知。
func (a *app) nativeNotifier() func(nativeNotice) error {
	if a == nil || a.desktop == nil {
		return nil
	}
	return a.desktop.notify
}

// showNativeNotice is the production notifier: a real desktop notification
// through the platform (Windows notification-area balloon, macOS Notification
// Center, Linux notification daemon). It is installed only once the app is
// ready, so headless UI tests never touch the platform loop.
//
// 点击由 App.OnNotificationClick 统一接管（见 main.go）：那个回调对「上次
// 运行留下、本次进程里没有对应 Notification 对象」的消息同样会触发，
// 所以这里不再给单条通知挂 OnClick，否则同一次点击会被处理两次。
func showNativeNotice(n nativeNotice) error {
	return mygo.NewNotification(mygo.NotificationOptions{
		ID:     n.ID,
		Title:  n.Title,
		Body:   n.Body,
		Silent: n.Silent,
	}).Show()
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
	// 关掉实时长连接时只起轮询兜底：未读数、列表与桌面通知照常更新，只是
	// 不再维持 SSE。偏好变化会调用 stop/start 这一对来切换，不会两路并存。
	if !a.notifPrefs().streamEnabled() {
		a.startNotificationFallbackPoll()
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
	// 轮询兜底与 SSE 共用 watchGen 判过期，这里一并停掉：退出登录、窗口关闭
	// 与切到 SSE 都会走这条路径。
	a.stopNotificationFallbackPoll()
}

// startNotificationFallbackPoll 起一轮延时轮询，每轮回到 UI 线程后重新排下一轮。
// 用 watchGen 判过期：start/stop 长连接、退出登录都会让它失效。
func (a *app) startNotificationFallbackPoll() {
	if a.notifPollTimer != nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	gen := a.watchGen
	a.notifPollTimer = time.AfterFunc(notificationFallbackInterval, func() {
		a.publish(func() {
			if a.watchGen != gen {
				return
			}
			a.notifPollTimer = nil
			if !a.notifPrefs().streamEnabled() || !a.auth.loggedIn() {
				return
			}
			a.refreshUnread()
			a.pollNotifications()
			a.reloadNotificationTab()
			a.startNotificationFallbackPoll()
		})
	})
}

func (a *app) stopNotificationFallbackPoll() {
	if a.notifPollTimer != nil {
		a.notifPollTimer.Stop()
		a.notifPollTimer = nil
	}
}

// applyNotificationStreamPref 让长连接偏好立即生效。只在 UI 线程调用：第一次
// 开启要建立连接，关掉要断开并接管轮询；两者都复用已有的 start/stop 路径。
func (a *app) applyNotificationStreamPref() {
	a.stopNotificationWatch()
	a.startNotificationWatch()
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
		counts, err := a.client.fetchUnreadNotificationCount(ctx,
			a.sectionReadAtParam(sectionHome), a.sectionReadAtParam(sectionCommunity))
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
					a.sectionUnread = notificationUnreadCounts{}
				}
				return
			}
			a.unread = max(0, counts.Total)
			a.sectionUnread = counts
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
		// 原生通知只播报最新一页的未读消息。窗口里的搜索条件只影响列表；
		// 设置里的「提醒关键词 / 分类」是另一套独立偏好，在下面逐条判定。
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
				// 先记账再过滤：用户关掉某个分类时，旧消息不会在重新打开开关后
				// 被当成新消息补弹一遍。
				a.notifSeen[n.ID] = true
				if n.IsRead || !a.notifPrefs().shouldNotify(n) {
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
			// 一次事件最多弹 N 条，避免同一批消息刷屏；条数由设置里的档位决定。
			if limit := a.notifPrefs().burstLimit(); limit > 0 && len(fresh) > limit {
				fresh = fresh[:limit]
			}
			notices := make([]nativeNotice, 0, len(fresh))
			for _, n := range fresh {
				notices = append(notices, a.notificationNative(n))
			}
			if !a.notifPrefs().notificationEnabled() {
				return
			}
			a.showNotices(notices)
		})
	}()
}

// notificationNative 把消息转成系统通知。提示音是进程级偏好，Silent 在 UI
// 线程上由偏好决定，平台通知协程只读这一份快照。
func (a *app) notificationNative(n *notification) nativeNotice {
	notice := notificationNative(n)
	notice.Silent = !a.notifPrefs().soundEnabled()
	return notice
}

// noticeTarget 是这条系统通知的点击目标。系统通知的点击回调只带 id，靠这个
// 映射落回对应的帖子 / 评论；映射已经清掉（进程重启、通知太旧）时返回空串，
// 由进程级单例决定退回消息中心（见 openNotice）。
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

// openNotificationTarget opens the message's target inside the app: a 2Libra
// post or comment goes back to the main window with the post (and the comment
// anchor) opened; a user profile opens that person's card; every other target
// (RSS, votes, system messages...) stays in the notification window, whose row
// already shows the message. Nothing here hands a notification to the system
// browser: the browser is usually logged into a different account, so a jump
// there would take the user away from the message they just clicked.
// 通知窗口只是入口：点开帖子时主窗口照常切到详情页，通知窗口自己不关 ——
// 读完后还能回到原来的消息列表与滚动位置。
//
// notifID 是点开的这条消息：目标带评论定位时先只记账，等那层楼出现在评论区
// 可视区再标为已读（见 markNotificationSeen）。目标没有评论定位（表态了你的
// 帖子、收藏了你的帖子…）时不动已读状态；留在消息中心的目标同样不动 ——
// 消息本来就在屏幕上，不需要再发一次已读。目标带评论定位时默认收起左侧正文，
// 让评论区占满整行直接露出目标那层楼，用户仍可手动展开。
func (a *app) openNotificationTarget(target, notifID string) {
	if target == "" {
		return
	}
	it, inApp := parseInAppTopicLink(target)
	if !inApp {
		if name, ok := parseUserAboutLink(target); ok {
			a.showUserCard(name)
			return
		}
		// 消息窗口已经开着时，这条消息就在屏幕上：不要重新拉第一页，否则
		// 用户正读到的位置会被刷掉。系统通知点进来时窗口没开，才需要打开它。
		if !a.notifShown {
			a.showNotifications()
		}
		return
	}
	a.openDetail(it)
	if it.CommentID != "" && a.detail != nil {
		// 过滤会让目标行藏起来：定位进来时一律让位，先退出过滤再定位。
		clearAuthorFilter(a.detail)
		// 从通知跳到具体评论：默认收起正文，评论区占满整行，进来即见目标楼层。
		// 同时清掉右栏的收起，避免两边都收起后什么也看不到。
		a.detail.bodyCollapsed = true
		a.detail.replyCollapsed = false
		if notifID != "" {
			a.detail.notifID = notifID
			a.detail.notifCommentID = it.CommentID
		}
	}
	a.showMainWindowAfterDetail()
}

func (a *app) showNotices(notices []nativeNotice) {
	// 老板键藏起窗口时一并屏蔽桌面通知：未读数、列表与「已见到」记账照常
	// 更新（见 pollNotifications），只是不弹窗、不发声，也不会留下点击目标。
	if a == nil || a.bossKeyHidden || len(notices) == 0 {
		return
	}
	notify := a.nativeNotifier()
	if notify == nil {
		return
	}
	for _, n := range notices {
		a.rememberNoticeTarget(n.ID, n.Target)
	}
	if a.win == nil {
		// 无头测试：同步送达，便于断言。
		for _, n := range notices {
			_ = notify(n)
		}
		return
	}
	// 平台通知可能等待授权，不能卡住 UI 线程。
	go func() {
		for _, n := range notices {
			_ = notify(n)
		}
	}()
}

// reloadNotificationTab silently refreshes the open message list after an
// event. 它不再清空列表重拉第一页：用户可能正看第 2、3 页，SSE 每来一条
// 消息就把视线弹回顶部是明显的问题。这里只做静默合并：新消息插到已加载
// 内容前面，后续页与滚动位置都保留。未加载 / 正在加载的列表留给
// ensureLoaded 和用户自己的操作。
func (a *app) reloadNotificationTab() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	idx := notificationTabIndex()
	if idx < 0 {
		return
	}
	st := a.tabs[idx]
	if !st.loaded || st.loading || st.refreshing {
		return
	}
	a.silentRefreshTab(toastNotifications, idx)
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
	pending := st.beginNotificationRead(ids)
	if len(pending) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := a.client.markNotificationsRead(ctx, pending)
		a.publish(func() {
			st.endNotificationRead(pending)
			if err != nil {
				// 缺 read:me 或登录失效：走全局授权弹窗，由用户自己重新操作。
				a.handleAuthErrorFor(owner, err)
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
	}
	st.dropNotificationSelected(ids)
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
				a.handleAuthErrorFor(owner, err)
				a.toast(toastNotifications, "全部已读失败："+friendlyError(err))
				return
			}
			st.clearNotificationSelected()
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
	sel := st.notificationSelection()
	if len(sel) == 0 {
		return nil
	}
	loaded := make(map[string]bool, len(st.items))
	for _, it := range st.items {
		if it.Notif != nil && !it.Notif.IsRead {
			loaded[it.Notif.ID] = true
		}
	}
	ids := make([]string, 0, len(sel))
	for id, on := range sel {
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
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	if on {
		if st.selected == nil {
			st.selected = map[string]bool{}
		}
		st.selected[id] = true
		return
	}
	delete(st.selected, id)
}

// beginNotificationRead 把还不在途的 id 标为在途，返回这次真正要发的一批。
// 与 endNotificationRead 一起保证同一张表在后台结果落地时也能安全访问。
func (st *tabState) beginNotificationRead(ids []string) []string {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
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
	for _, id := range pending {
		st.reading[id] = true
	}
	return pending
}

// endNotificationRead 清掉在途标记：请求成功与失败都要收尾。
func (st *tabState) endNotificationRead(ids []string) {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	for _, id := range ids {
		delete(st.reading, id)
	}
}

// notificationReading 报告这条消息是否正在提交已读。
func (st *tabState) notificationReading(id string) bool {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	return st.reading[id]
}

// notificationChecked 返回这条消息此刻是否被勾选。
func (st *tabState) notificationChecked(id string) bool {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	return st.selected[id]
}

// notificationSelection 给出一份勾选表的快照，调用方遍历它时不必持锁。
func (st *tabState) notificationSelection() map[string]bool {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	if len(st.selected) == 0 {
		return nil
	}
	out := make(map[string]bool, len(st.selected))
	for id, on := range st.selected {
		out[id] = on
	}
	return out
}

// dropNotificationSelected 去掉已经落账的消息的勾选。
func (st *tabState) dropNotificationSelected(ids []string) {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	for _, id := range ids {
		delete(st.selected, id)
	}
}

// clearNotificationSelected 清空全部勾选。
func (st *tabState) clearNotificationSelected() {
	st.notifMu.Lock()
	defer st.notifMu.Unlock()
	st.selected = nil
}
