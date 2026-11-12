package main

import "time"

// 金币变动的全局事件。
//
// 任何会让金币发生变化的操作，成功后都调用 coinsChanged 报告一次，由这一个
// 入口统一刷新金币显示，而不是每个入口各自记住要刷新哪几块界面：
//
//	标题栏余额    始终刷新；金币窗口的数据已经加载过时，余额随它的 base
//	              请求一并更新，标题栏读的也是同一份 cs.balance
//	金币窗口      只重拉已经加载过的块：余额 / 排名 / 签到 / 盲盒、今日明细，
//	              以及打开过的流水、统计与排行。从没打开过窗口时一次多余
//	              请求都不发，等用户打开时 ensureCoinsLoaded 会补齐
//
// 事件名与金币流水的 reason 机器码对齐，方便在测试与日志里对上账。开放 API
// 里会改变金币的操作与接入位置：
//
//	coinEventSign        签到 POST /v1/me/sign                      signInNow
//	coinEventPost        发帖 POST /v1/topics 🔑（含随帖扣费的金币池、    submitCompose
//	                     兑换码红包等字段）
//	coinEventReply       回复 POST /v1/topics/:short_id/replies     submitReply
//	coinEventReaction    表态 POST /v1/topics/:id/reaction 或        toggleReaction
//	                     /v1/replies/:id/reaction
//	coinEventReward      打赏 POST /v1/rewards 🔑                    submitReward
//	coinEventBadge       佩戴 / 取消佩戴徽章                          toggleProfileBadge
//	coinEventPandora     开盲盒 / 重抽 POST /v1/pandora/open|reroll  openPandoraBox
//	                     🔑                                          rerollPandoraBox
//	coinEventIncoming    收到表态 / 打赏 / 回复等新通知                pollNotifications
//	coinEventForeground  回到前台，校准在站点上的金币变动              onBecameActive
//
// 金币池注资、竞猜、兑换码领取、盲盒延期、付费墙解锁、更换头像这些能力这个
// 应用还没有入口；以后加上时，同样在成功后调用 coinsChanged。
// 用户直接在站点上完成它们不会产生站内通知，由 coinEventForeground 与标题栏
// 金币数的定时刷新兜底。
type coinEvent string

const (
	coinEventSign       coinEvent = "checkin"       // 签到
	coinEventPost       coinEvent = "post"          // 发帖（含随帖扣费的项目）
	coinEventReply      coinEvent = "comment"       // 回复 / 评论
	coinEventReaction   coinEvent = "send_reaction" // 表态（发送表情）
	coinEventReward     coinEvent = "send_reward"   // 打赏
	coinEventBadge      coinEvent = "badge_equip"   // 佩戴 / 取消佩戴徽章
	coinEventPandora    coinEvent = "pandora"       // 开盲盒 / 重抽
	coinEventIncoming   coinEvent = "incoming"      // 收到表态 / 打赏 / 回复等新通知
	coinEventForeground coinEvent = "foreground"    // 回到前台校准站点上的变动
)

// coinRefreshDelay 是全局金币刷新的合并窗口：连着发几条回复、点几个表情时
// 只发一轮请求，不会每个动作都打一轮。节奏与通知的 400ms 去抖一致。
var coinRefreshDelay = 400 * time.Millisecond

// coinsChanged 是「金币可能变了」的全局事件入口。签到、发帖、回复、表态、
// 打赏，以及收到别人的表态 / 打赏等事件成功后都调用它，由这里统一刷新标题栏
// 余额与金币窗口里已经加载过的数据。
//
// 事件只排队、不立刻发请求：coinRefreshDelay 内的多次调用合并成一次刷新。
// 只在 UI 线程调用（各写操作都在 publish 回来的回调里发事件）。
func (a *app) coinsChanged(event coinEvent) {
	if a.onCoinsChanged != nil {
		a.onCoinsChanged(event)
	}
	if !a.autoLoad || !a.auth.loggedIn() || a.coins == nil {
		return
	}
	a.scheduleCoinRefresh()
}

// scheduleCoinRefresh 排一次合并后的刷新。已经有一次排着时就复用，不叠加
// 定时器；gen 让停掉之后还在路上的定时器不再生效。
func (a *app) scheduleCoinRefresh() {
	if a.coinRefreshTimer != nil {
		return
	}
	a.coinRefreshGen++
	gen := a.coinRefreshGen
	a.coinRefreshTimer = time.AfterFunc(coinRefreshDelay, func() {
		a.publish(func() {
			if a.coinRefreshGen != gen {
				return
			}
			a.coinRefreshTimer = nil
			if !a.auth.loggedIn() {
				return
			}
			a.refreshCoinsNow()
		})
	})
}

// stopCoinRefresh 丢掉还没触发的合并刷新（退出登录、主窗口关闭时调用）。
func (a *app) stopCoinRefresh() {
	a.coinRefreshGen++
	if a.coinRefreshTimer != nil {
		a.coinRefreshTimer.Stop()
		a.coinRefreshTimer = nil
	}
}

// coinPageLoaded 报告金币窗口的数据是否已经拉过：只有拉过才值得在全局事件
// 里刷新，否则等用户打开窗口时 ensureCoinsLoaded 会补齐。
func (a *app) coinPageLoaded() bool {
	cs := a.coins
	return cs != nil && (cs.baseLoaded || cs.baseLoading)
}

// refreshCoinsNow 真正发出刷新请求。金币窗口的数据已经加载过时按块重拉，
// 其中 base 带着余额，标题栏读同一份 cs.balance；否则只校准标题栏。
func (a *app) refreshCoinsNow() {
	cs := a.coins
	if cs == nil {
		return
	}
	if cs.baseWait {
		// 首屏还在等全局授权补 read:economy：别再重复发注定 403 的请求。
		return
	}
	if a.coinPageLoaded() {
		a.refreshLoadedCoinSections()
		return
	}
	a.refreshCoinTagBalance()
}

// refreshLoadedCoinSections 重拉金币窗口里已经加载过的数据块。没加载过的块
// 保持不动：用户的一次表态不该顺带把流水、统计与排行全请求一遍。
func (a *app) refreshLoadedCoinSections() {
	cs := a.coins
	if cs == nil {
		return
	}
	if cs.baseLoaded || cs.baseLoading {
		a.reloadCoinBase()
	}
	if cs.todayLoaded || cs.todayLoading {
		a.reloadCoinToday()
	}
	if cs.historyLoaded || cs.historyLoading {
		a.reloadCoinHistory()
	}
	if cs.dailyLoaded || cs.dailyLoading {
		a.reloadCoinDaily()
	}
	if cs.breakdownLoaded || cs.breakdownLoading {
		a.loadCoinBreakdown()
	}
	if cs.rankingLoaded || cs.rankingLoading {
		a.reloadCoinRanking()
	}
}
