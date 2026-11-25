package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 金币窗口的界面：标题栏下面是余额 / 今日净收入，再按「今日、历史、统计、
// 排行、兑换」切换。窗口本身在 coins_window.go；这里只画窗口里的内容。
// 窗口里不展示签到状态，也没有签到入口：签到仍在标题栏「我的」账号菜单与
// 命令面板里（见 profile_ui.go、command_palette.go），潘多拉也在账号菜单里。
// 发帖、回复、表态与打赏在各自的页面里；金币余额只在展开账号菜单时请求，
// 本页各块在打开窗口时各自加载。
//
// 整套界面走 Lobe UI：分段、按钮、卡片、标签、提示与空状态都用 lobe-mygo 的
// 组件，颜色取 TokensOf(c)，与通知 / 设置 / 我的等窗口同一套设计语言。转圈沿用
// mygo 原生 Spinner：Lobe 的 Spin 会一直请求动画帧，在刷新仍在进行、无头 tester
// 复建窗口时会把 toast 的淡入帧挤掉。文案尽量精简；无障碍名与工具提示照旧供
// 读屏和点击测试命中。

const (
	coinViewToday = iota
	coinViewHistory
	coinViewStats
	coinViewRanking
	// coinViewRedeem 是「兑换」页：我兑换过的兑换码（GET /v1/me/redeem-codes）。
	// 领取本身在帖子详情页的兑换码块里，这里只回看历史。
	coinViewRedeem
)

var coinViewLabels = []string{"今日", "历史", "统计", "排行", "兑换"}

func (a *app) coinsBody(c *ui.Context) {
	cs := a.coins
	if cs == nil {
		return
	}
	// 窗口标题栏已经写着「金币」与余额，这里不再重复一个大标题：第一行
	// 只放视图切换与刷新。
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Padding(10, 14, 8, 14).Gap(10).AlignItems(ui.Center).Children(func() {
			action.Segmented(c, &cs.view, coinViewLabels, action.SegmentedProps{
				Size: lobetheme.Small,
			}).Label("金币视图")
			ui.Spacer(c).Grow(1)
			busy := cs.baseLoading || cs.todayLoading || cs.historyLoading ||
				cs.dailyLoading || cs.breakdownLoading || cs.rankingLoading ||
				cs.redeemLoading
			a.smallRefreshButton(c, busy, func() { a.reloadCoins() })
		})
		layout.Divider(c, layout.DividerProps{})
		a.coinBalanceCard(c, cs)
		a.coinCoreError(c, cs)
		switch cs.view {
		case coinViewHistory:
			a.coinHistoryView(c, cs)
		case coinViewStats:
			a.coinStatsView(c, cs)
		case coinViewRanking:
			a.coinRankingView(c, cs)
		case coinViewRedeem:
			a.coinRedeemView(c, cs)
		default:
			a.coinTodayView(c, cs)
		}
	})
}

func (a *app) coinBalanceCard(c *ui.Context, cs *coinState) {
	tk := lobetheme.TokensOf(c)
	card := popupRow(c, false).FillWidth().Padding(12, 14)
	card.Children(func() {
		ui.Row(c).FillWidth().Gap(20).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				display.Text(c, "当前余额", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				if cs.baseLoading && !cs.balanceOK {
					lobeSpin(c, feedback.SpinProps{CustomSize: 20})
				} else if cs.balanceOK {
					display.Text(c, coinThousands(cs.balance)+" 金币", display.TextProps{
						Strong: true, FontSize: 26, Rows: 1,
					})
				} else {
					display.Text(c, "—", display.TextProps{Strong: true, FontSize: 26, Rows: 1})
				}
				if cs.rankOK {
					display.Text(c, fmt.Sprintf("站内排名 第 %d 名", cs.rank), display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
			})

			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				display.Text(c, "今日净收入", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				switch {
				case cs.todayLoading && !cs.todayLoaded:
					lobeSpin(c, feedback.SpinProps{CustomSize: 18})
				case cs.todayErr != "":
					display.Text(c, "—", display.TextProps{
						Type: display.TextSecondary, Strong: true, FontSize: 20, Rows: 1,
					})
				default:
					display.Text(c, coinSignedText(cs.todayNet)+" 金币", display.TextProps{
						Strong: true, FontSize: 20, Rows: 1,
					}).TextColor(coinAmountColor(tk, cs.todayNet))
					display.Text(c, fmt.Sprintf("收入 %s · 支出 %s",
						coinSignedText(cs.todayIncome), coinExpenseText(cs.todayExpense)), display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
			})
		})
	})
}

// signInAuthTarget 是签到授权弹窗该画在哪个窗口。签到入口在标题栏「我的」
// 账号菜单与命令面板里（主窗口），金币窗口只是碰巧也会显示结果；授权弹窗
// 必须画在真正发起操作的窗口里——金币窗口没开时硬编码 toastCoins 会被
// demandAuthorizationFor 当成「发起窗口已关掉」而整个丢掉，用户只能看到一条
// 普通错误提示，永远等不到「前往授权」。普通 toast 仍用 toastCoins：窗口没开
// 时它自己就会退回主窗口（见 mainClaimsToasts）。
func (a *app) signInAuthTarget() toastTarget {
	if a.toastWindow(toastCoins) != nil {
		return toastCoins
	}
	return toastMain
}

// signInNow 提交今日签到。未登录或缺少 write:economy 时走全局授权弹窗，
// 授权成功后由用户自己再点签到；今日重复签到（409 conflict）按「已经签过」
// 处理。成功后先用响应里的状态点亮按钮，再刷新余额与今日明细，因为签到会
// 同时改变金币与流水。
func (a *app) signInNow() {
	if a.onSignIn != nil {
		a.onSignIn()
	}
	cs := a.coins
	if cs == nil || cs.signBusy {
		return
	}
	authTarget := a.signInAuthTarget()
	if !a.auth.loggedIn() {
		a.requestLoginFor(authTarget, "登录 2Libra 后才能签到。")
		return
	}
	if cs.sign.Known && cs.sign.Signed {
		a.toast(toastCoins, "今天已经签过了，明天再来")
		return
	}
	cs.signBusy = true
	cs.signGen++
	gen := cs.signGen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), coinSignTimeout)
	go func() {
		defer cancel()
		status, err := a.client.signToday(ctx)
		a.publish(func() {
			if a.coins != cs || cs.signGen != gen {
				return
			}
			cs.signBusy = false
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Code == "conflict" {
					// 今日已签到：服务端拒绝重复签到，状态按已签到处理并同步。
					cs.sign.Known, cs.sign.Signed = true, true
					a.toast(toastCoins, "今天已经签过了，明天再来")
					a.reloadCoinBase()
					return
				}
				// 签到只可能缺 write:economy。服务端有时只回 insufficient_scope
				// 而不带 required_scope，这里补上已知 scope，保证「前往授权」
				// 一定把它带进授权地址，不用回来再补第二次授权。
				if a.handleAuthErrorWithScopesFor(authTarget, err, []string{"write:economy"}) {
					return
				}
				a.toast(toastCoins, friendlyError(err))
				return
			}
			// 成功：响应里的连续天数 / 今日奖励不一定齐全，缺的沿用已知状态。
			next := status
			next.Known, next.Signed = true, true
			if next.Streak == 0 {
				next.Streak = cs.sign.Streak
			}
			cs.sign = next
			text := "签到成功"
			// 只有响应明确给出奖励字段时才报金额；coins 可能是余额，留白更稳。
			if next.RewardKnown && next.Coins > 0 {
				text += "，获得 " + coinAmountText(next.Coins) + " 金币"
			}
			if next.Streak > 1 {
				text += fmt.Sprintf(" · 连续 %d 天", next.Streak)
			}
			a.toast(toastCoins, text)
			// 余额、排名与今日明细都跟着签到变了：补拉 base 与今日明细。
			a.reloadCoinBase()
			a.reloadCoinToday()
		})
	}()
}

// coinCoreError 展示余额 / 今日 / 流水等核心端点的普通错误。缺权限的失败
// 已经由全局授权弹窗接管（authWait），这里不再重复报错。
func (a *app) coinCoreError(c *ui.Context, cs *coinState) {
	if cs.balanceErr != "" {
		a.coinInlineError(c, cs.balanceErr, func() { a.reloadCoinBase() })
	}
	if cs.todayNote != "" {
		display.Text(c, cs.todayNote, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 2,
		})
	}
	switch {
	case cs.todayErr != "":
		a.coinInlineError(c, cs.todayErr, func() { a.reloadCoinToday() })
	case cs.todayWait:
		display.Text(c, "今日金币明细加载失败，请授权后重新打开。", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
	}
}

func (a *app) coinInlineError(c *ui.Context, text string, retry func()) {
	ui.Row(c).FillWidth().Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, text, display.TextProps{
			Type: display.TextDanger, FontSize: 11, Rows: 2,
		}).Grow(1)
		a.smallAction(c, "重试", false, retry)
	})
}

func (a *app) coinTodayView(c *ui.Context, cs *coinState) {
	list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Padding(8, 14).Gap(10).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "今日明细", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
				display.Text(c, fmt.Sprintf("共 %d 条", len(cs.todayItems)), display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				ui.Spacer(c).Grow(1)
				if cs.todayFrom == "transactions" {
					display.Text(c, "来自金币流水", display.TextProps{
						Type: display.TextSecondary, FontSize: 10, Rows: 1,
					})
				}
			})

			switch {
			case cs.todayLoading && len(cs.todayItems) == 0:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{CustomSize: 18})
					display.Text(c, "正在加载今日金币明细…", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
				})
			case cs.todayErr != "":
				feedback.Alert(c, "今日明细暂时无法加载", feedback.AlertProps{
					Type: feedback.Error, Variant: feedback.Soft, Description: cs.todayErr,
				})
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.reloadCoinToday()
				}
			case len(cs.todayItems) == 0:
				display.Empty(c, display.EmptyProps{
					Icon: lucide.Inbox(), Title: "今天还没有金币变动。",
				})
			default:
				for _, tx := range cs.todayItems {
					a.coinTransactionRow(c, tx, true)
				}
			}

		})
	})
	list.Grow(1).FillWidth().MinHeight(0)
}

func (a *app) coinHistoryView(c *ui.Context, cs *coinState) {
	ui.Row(c).FillWidth().Padding(8, 14, 6, 14).Gap(10).AlignItems(ui.Center).Children(func() {
		display.Text(c, "金币流水", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
		rangeSel := cs.historyRange
		action.Segmented(c, &rangeSel, coinRangeLabels, action.SegmentedProps{
			Size: lobetheme.Small,
		}).Label("时间范围")
		if rangeSel != cs.historyRange {
			a.setCoinHistoryRange(rangeSel)
		}
		ui.Spacer(c).Grow(1)
		if cs.historyTotal > 0 {
			display.Text(c, fmt.Sprintf("共 %d 条记录", cs.historyTotal), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		}
	})
	a.coinHistoryReasonRow(c, cs)

	switch {
	case cs.historyLoading && len(cs.historyItems) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			display.Text(c, "正在加载金币流水…", display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
		return
	case cs.historyErr != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "金币流水加载失败", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: cs.historyErr, Selectable: true,
			})
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadCoinHistory()
			}
		})
		return
	case len(cs.historyItems) == 0:
		text := "这个时间范围没有金币记录。"
		if code := coinReasonCode(cs.historyReason); code != "" {
			text = "这个时间范围没有「" + cs.historyReason + "」的金币记录。"
		}
		ui.Column(c).Grow(1).Center().Children(func() {
			display.Empty(c, display.EmptyProps{Icon: lucide.Inbox(), Title: text})
		})
		return
	}

	items := cs.historyItems
	cs.historyList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].ID
		}
		return i
	}
	ui.List(c, &cs.historyList, len(items), func(i int) {
		a.coinTransactionRow(c, items[i], false)
	}).Grow(1).Padding(8, 14).Gap(8)
	a.loadMoreFooter(c, cs.historyMoreErr, cs.historyLoading, func() {
		a.loadCoinHistory(cs.historyPage)
	})
	if cs.historyLoaded && !cs.historyLoading && cs.historyMoreErr == "" && !cs.historyNoMore &&
		len(items) > 0 && cs.historyList.AtEnd() {
		hasMore := cs.historyTotalPages == 0 || cs.historyPage < cs.historyTotalPages
		if hasMore {
			a.loadCoinHistory(cs.historyPage + 1)
		}
	}
}

// coinHistoryReasonRow 是流水页的类型过滤：选中即从第一页重拉，不需要再点
// 「筛选」。下拉列的是全部已知 reason，第一项「全部类型」不筛。
func (a *app) coinHistoryReasonRow(c *ui.Context, cs *coinState) {
	ui.Row(c).FillWidth().Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, "类型", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		filter := lobeSelect(c, &cs.historyReason, field.SelectProps{
			Options: selectOptions(coinReasonOptions), Size: lobetheme.Small,
		})
		filter.Label("金币类型").Tooltip("只看某一类金币变动").Width(150)
		if filter.Changed() {
			a.setCoinHistoryReason(cs.historyReason)
		}
		if coinReasonCode(cs.historyReason) != "" {
			a.smallAction(c, "清除筛选", false, func() { a.setCoinHistoryReason(coinReasonAll) })
		}
		ui.Spacer(c).Grow(1)
	})
}

// 统计页用两个 Tab 分开两类数据：列表是「近 30 天收支」的逐日明细，图表是
// 「收入 / 支出构成」的占比条。cs.statsTab 默认 0（列表），1 是图表。
const coinStatsChart = 1

var coinStatsTabLabels = []string{"列表", "图表"}

func (a *app) coinStatsView(c *ui.Context, cs *coinState) {
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Shrink(0).Padding(4, 14, 2, 14).Children(func() {
			action.Segmented(c, &cs.statsTab, coinStatsTabLabels, action.SegmentedProps{
				Size: lobetheme.Small,
			}).Label("统计视图")
		})
		body := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			ui.Column(c).FillWidth().Padding(8, 14).Children(func() {
				if cs.statsTab == coinStatsChart {
					a.coinBreakdownCard(c, cs)
				} else {
					a.coinDailyCard(c, cs)
				}
			})
		})
		body.Grow(1).FillWidth().MinHeight(0)
	})
}

func (a *app) coinDailyCard(c *ui.Context, cs *coinState) {
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "近 30 天收支", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
			display.Text(c, "按日聚合", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			ui.Spacer(c).Grow(1)
			a.smallRefreshButton(c, cs.dailyLoading, func() { a.reloadCoinDaily() })
		})
		switch {
		case cs.dailyErr != "":
			display.Text(c, cs.dailyErr, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 2,
			})
		case cs.dailyLoading && len(cs.dailyItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{CustomSize: 16})
				display.Text(c, "正在加载按日收支…", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			})
		case len(cs.dailyItems) == 0:
			display.Text(c, "暂无可展示的按日数据。", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		default:
			for _, day := range cs.dailyItems {
				a.coinDailyRow(c, day)
			}
		}
	})
	layout.Divider(c, layout.DividerProps{})
}

func (a *app) coinDailyRow(c *ui.Context, day coinDailyFlow) {
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		display.Text(c, day.Date, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		}).Width(96)
		display.Text(c, "+"+coinAmountText(day.Income), display.TextProps{
			Type: display.TextSuccess, FontSize: 11, Rows: 1,
		}).Width(90).TextAlign(ui.End)
		display.Text(c, coinExpenseText(day.Expense), display.TextProps{
			Type: display.TextDanger, FontSize: 11, Rows: 1,
		}).Width(90).TextAlign(ui.End)
		ui.Spacer(c).Grow(1)
		display.Text(c, coinSignedText(day.Net), display.TextProps{
			Strong: true, FontSize: 12, Rows: 1,
		}).TextColor(coinAmountColor(tk, day.Net)).TextAlign(ui.End)
	})
}

func (a *app) coinBreakdownCard(c *ui.Context, cs *coinState) {
	title := "收入构成"
	if cs.breakdownType == "expense" {
		title = "支出构成"
	}
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, title, display.TextProps{Strong: true, FontSize: 14, Rows: 1})
			ui.Spacer(c).Grow(1)
			a.coinToggleChip(c, "收入", "按收入分类看构成", cs.breakdownType != "expense", func() { a.setCoinBreakdownType("income") })
			a.coinToggleChip(c, "支出", "按支出分类看构成", cs.breakdownType == "expense", func() { a.setCoinBreakdownType("expense") })
		})
		// 时间范围快捷行：选「注册至今」时会去取账号注册日当作起点。
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			for i, label := range coinBreakdownRangeLabels {
				r := i
				a.coinToggleChip(c, label, coinBreakdownRangeTip(r), cs.breakdownRange == r, func() { a.setCoinBreakdownRange(r) })
			}
			ui.Spacer(c).Grow(1)
			if start, end := coinBreakdownDates(cs.breakdownRange, cs.registeredDate); start != "" {
				display.Text(c, start+" ~ "+end, display.TextProps{
					Type: display.TextSecondary, FontSize: 10, Rows: 1,
				})
			}
		})
		switch {
		case cs.breakdownErr != "":
			display.Text(c, cs.breakdownErr, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 2,
			})
		case cs.breakdownLoading && len(cs.breakdownItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{CustomSize: 16})
				display.Text(c, "正在加载分类数据…", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			})
		case len(cs.breakdownItems) == 0:
			display.Text(c, coinBreakdownRangeLabels[cs.breakdownRange]+"暂无"+title+"数据。", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		default:
			total := 0.0
			for _, b := range cs.breakdownItems {
				total += b.Amount
			}
			for _, b := range cs.breakdownItems {
				a.coinBreakdownRow(c, b, total)
			}
		}
	})
}

func coinBreakdownRangeTip(r int) string {
	switch r {
	case coinBreakdownYear:
		return "统计最近一年的分类构成"
	case coinBreakdownAll:
		return "从注册当天起统计全部分类构成"
	default:
		return "只统计本月 1 日至今的分类构成"
	}
}

func (a *app) coinBreakdownRow(c *ui.Context, b coinBreakdown, total float64) {
	tk := lobetheme.TokensOf(c)
	ratio := b.Ratio
	if ratio <= 0 && total > 0 {
		ratio = b.Amount / total
	}
	if ratio < 0 {
		ratio = 0
	}
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			display.Text(c, coinReasonLabel(b.Reason), display.TextProps{FontSize: 11, Rows: 1}).Grow(1)
			display.Text(c, coinAmountText(b.Amount), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			display.Text(c, fmt.Sprintf("%.1f%%", ratio*100), display.TextProps{
				Type: display.TextSecondary, FontSize: 10, Rows: 1,
			}).Width(46).TextAlign(ui.End)
		})
		bar := ui.Box(c).FillWidth().Height(5).Radius(3).Background(tk.FillTertiary)
		bar.Children(func() {
			width := float32(ratio * 100)
			if width > 100 {
				width = 100
			}
			ui.Box(c).WidthPercent(width).Height(5).Radius(3).Background(tk.Primary)
		})
	})
}

func (a *app) coinRankingView(c *ui.Context, cs *coinState) {
	ui.Row(c).FillWidth().Padding(8, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, "金币排行榜", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
		display.Text(c, fmt.Sprintf("前 %d 名", coinRankingLimit), display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		ui.Spacer(c).Grow(1)
		a.smallRefreshButton(c, cs.rankingLoading, func() { a.reloadCoinRanking() })
	})
	switch {
	case cs.rankingErr != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "排行榜暂时无法加载", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: cs.rankingErr, Selectable: true,
			})
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadCoinRanking()
			}
		})
		return
	case cs.rankingLoading && len(cs.rankingItems) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			display.Text(c, "正在加载排行榜…", display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
		return
	case len(cs.rankingItems) == 0:
		ui.Column(c).Grow(1).Center().Children(func() {
			display.Empty(c, display.EmptyProps{Icon: lucide.Trophy(), Title: "暂无排行数据。"})
		})
		return
	}

	items := cs.rankingItems
	cs.rankingList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Username
		}
		return i
	}
	ui.List(c, &cs.rankingList, len(items), func(i int) {
		a.coinRankRow(c, items[i])
	}).Grow(1).Padding(8, 14).Gap(6)
}

func (a *app) coinRankRow(c *ui.Context, e coinRankEntry) {
	tk := lobetheme.TokensOf(c)
	row := popupRow(c, true).FillWidth().Padding(8, 14)
	row.Children(func() {
		// popupRow 是列容器：整行字段必须装进同一个 Row，否则会竖着排成多行。
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			display.Text(c, fmt.Sprintf("%d", e.Rank), display.TextProps{
				Strong: true, FontSize: 14, Rows: 1,
			}).Width(32).Shrink(0).TextColor(coinRankColor(tk, e.Rank))
			// 昵称吃掉中间剩余空间，过长时单行省略；「我」不参与压缩。
			a.userTag(c, userTagSpec{
				Name:   e.Username,
				Avatar: e.AvatarURL,
				Size:   13,
				Bold:   true,
				Badges: e.Badges,
				Pro:    e.IsPro,
			}).Grow(1).MinWidth(0)
			if name := a.auth.username(); name != "" && strings.EqualFold(name, e.Username) {
				display.Text(c, "（我）", display.TextProps{
					Type: display.TextSecondary, FontSize: 10, Rows: 1,
				}).Shrink(0)
			}
			// 金币固定在最右侧：等宽数字加粗，用金币金，金额一眼可辨。
			ui.Text(c, coinThousands(e.Balance)+" 金币").
				Font(sidebarFont).FontSize(13).FontWeight(700).
				TextColor(tk.Gold).Shrink(0).TextAlign(ui.End).SingleLine()
		})
	})
	if e.Username != "" {
		row.Tooltip("查看用户主页")
		row.OnClick(func() {
			a.openExternal(siteBaseURL + "/user/" + url.PathEscape(e.Username) + "/about")
		})
	}
}

func (a *app) coinTransactionRow(c *ui.Context, tx coinTransaction, today bool) {
	tk := lobetheme.TokensOf(c)
	row := popupRow(c, true).FillWidth().Padding(10, 12)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					display.Text(c, coinReasonLabel(tx.Reason), display.TextProps{
						Strong: true, FontSize: 12, Rows: 1,
					})
					if tx.IsAlias {
						display.Text(c, "分身", display.TextProps{
							Type: display.TextSecondary, FontSize: 10, Rows: 1,
						})
					}
					display.Text(c, coinWhen(tx.CreatedAt, today), display.TextProps{
						Type: display.TextSecondary, FontSize: 10, Rows: 1,
					})
				})
				if detail := coinTransactionDetail(tx); detail != "" {
					// 「收到打赏 @某人」里的用户名单独交给统一用户标识，
					// 头像跟着 12px 的详情字号走。
					before, user, after := coinTransactionDetailParts(tx)
					if user == "" {
						display.Text(c, detail, display.TextProps{
							Type: display.TextSecondary, FontSize: 12, Rows: 2,
						})
					} else {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Wrap().Children(func() {
							if before != "" {
								display.Text(c, before, display.TextProps{
									Type: display.TextSecondary, FontSize: 12, Rows: 1,
								})
							}
							a.userTag(c, userTagSpec{
								Name:   user,
								Avatar: tx.FromAvatar,
								Label:  "@" + user,
								Size:   12,
								Color:  tk.TextSecondary,
								Badges: tx.FromBadges,
								Pro:    tx.FromIsPro,
							})
							if after != "" {
								display.Text(c, after, display.TextProps{
									Type: display.TextSecondary, FontSize: 12, Rows: 1,
								})
							}
						})
					}
				}
			})
			ui.Column(c).Shrink(0).Gap(1).Children(func() {
				display.Text(c, coinSignedText(tx.Amount), display.TextProps{
					Strong: true, FontSize: 15, Rows: 1,
				}).TextColor(coinAmountColor(tk, tx.Amount)).TextAlign(ui.End)
				if tx.HasDiscount {
					display.Text(c, "原价 "+coinAmountText(tx.BaseAmount), display.TextProps{
						Type: display.TextSecondary, FontSize: 9, Rows: 1,
					}).TextAlign(ui.End)
				}
			})
		})
	})
	if tx.RelatePath != "" {
		row.Tooltip("打开相关帖子或页面")
		row.OnClick(func() { a.handleLink(tx.RelatePath) })
	}
}

// coinTransactionDetailParts 把详情拆成「用户名之前 / 昵称 / 之后」：只有
// 「收到打赏 @某人」这类文案里含用户名，拆开后用户组件才能在昵称旁画头像。
// 没有用户名时 user 为空，before 就是完整文案。
func coinTransactionDetailParts(tx coinTransaction) (before, user, after string) {
	detail := coinTransactionDetail(tx)
	if tx.FromUser == "" {
		return detail, "", ""
	}
	marker := "@" + tx.FromUser
	if i := strings.Index(detail, marker); i >= 0 {
		return detail[:i], tx.FromUser, detail[i+len(marker):]
	}
	return detail, "", ""
}

func coinTransactionDetail(tx coinTransaction) string {
	title := cleanText(tx.RelateTitle)
	comment := cleanText(tx.RelateComment)
	from := ""
	if tx.FromUser != "" {
		from = "@" + tx.FromUser
	}
	switch tx.Reason {
	case "checkin":
		if len(tx.CheckData) > 0 {
			return "签到检定结果"
		}
		return "签到成功"
	case "post":
		if title != "" {
			return "发表帖子《" + truncate(title, 40) + "》"
		}
	case "comment":
		if title != "" && comment != "" {
			return "《" + truncate(title, 30) + "》· " + truncate(comment, 50)
		}
		if title != "" {
			return "《" + truncate(title, 40) + "》"
		}
		if comment != "" {
			return truncate(comment, 60)
		}
	case "send_reaction", "get_reaction":
		emoji := ""
		if tx.Emoji != "" {
			emoji = " " + tx.Emoji
		}
		if title != "" {
			return "《" + truncate(title, 30) + "》" + emoji
		}
		if comment != "" {
			return truncate(comment, 40) + emoji
		}
	case "send_reward", "receive_reward":
		prefix := "打赏"
		if tx.Reason == "receive_reward" {
			prefix = "收到打赏"
		}
		if from != "" && tx.Reason == "receive_reward" {
			prefix += " " + from
		}
		if title != "" {
			return prefix + " · 《" + truncate(title, 30) + "》"
		}
		if comment != "" {
			return prefix + " · " + truncate(comment, 40)
		}
		return prefix
	case "daily_active_reward":
		return "每日活跃奖励"
	case "weekly_balance_reward":
		if title != "" {
			return title
		}
		return "每周平衡值奖励"
	case "register":
		return "注册奖励"
	case "inviteGain":
		return "邀请奖励"
	case "makeup":
		return "补签"
	}
	if title != "" && comment != "" {
		return "《" + truncate(title, 30) + "》· " + truncate(comment, 50)
	}
	if title != "" {
		return "《" + truncate(title, 40) + "》"
	}
	if comment != "" {
		return truncate(comment, 60)
	}
	if from != "" {
		return from
	}
	return ""
}

func coinWhen(s string, today bool) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			local := t.In(coinNow().Location())
			if today {
				return local.Format("15:04")
			}
			return local.Format("2006-01-02 15:04")
		}
	}
	return relativeTime(s)
}

func coinAmountColor(tk *lobetheme.Tokens, v float64) ui.Color {
	switch {
	case v > 0:
		return tk.Success
	case v < 0:
		return tk.Error
	default:
		return tk.TextSecondary
	}
}

func coinRankColor(tk *lobetheme.Tokens, rank int) ui.Color {
	switch rank {
	case 1:
		return tk.Warning
	case 2:
		return tk.Primary
	case 3:
		return tk.Success
	default:
		return tk.TextSecondary
	}
}

// coinToggleChip 是一枚 Lobe 芯片：选中用 Primary 实心，未选中用透明文字按钮，
// 无障碍名与工具提示照旧，读屏和点击测试靠它们命中。
func (a *app) coinToggleChip(c *ui.Context, label, tip string, active bool, onClick func()) {
	variant := action.Text
	if active {
		variant = action.Primary
	}
	b := action.Button(c, label, action.ButtonProps{Size: lobetheme.Small, Variant: variant})
	b.Radius(999).Label(label).Tooltip(tip)
	if b.Clicked() {
		onClick()
	}
}

// ---- 加载 ----

func (a *app) ensureCoinsLoaded() {
	cs := a.coins
	if cs == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	a.ensureCoinBase()
	a.ensureCoinToday()
	switch cs.view {
	case coinViewHistory:
		a.ensureCoinHistory()
	case coinViewStats:
		a.ensureCoinStats()
	case coinViewRanking:
		a.ensureCoinRanking()
	case coinViewRedeem:
		a.ensureCoinRedeem()
	}
}

func (a *app) ensureCoinBase() {
	cs := a.coins
	if cs == nil || cs.baseLoading || cs.baseLoaded || cs.baseWait {
		return
	}
	a.loadCoinBase()
}

// ensureCoinSignSynced 在账号菜单打开时按需同步今日签到状态。
//
// 成功结果按自然日缓存（内存与本地缓存）：同一天再开菜单不再发请求，隔天、
// 重新登录或重新登录后重新拉。失败时静默退避，不弹授权也不显示错误——菜单
// 每帧都可能重建，没有退避就会一直重发一个注定失败的请求。
func (a *app) ensureCoinSignSynced() {
	cs := a.coins
	if cs == nil || !a.autoLoad || !a.auth.loggedIn() || cs.signSyncing {
		return
	}
	today := coinTodayDate()
	if cs.signSyncedDate == today {
		return
	}
	if !cs.signSyncTriedAt.IsZero() && time.Since(cs.signSyncTriedAt) < coinSignSyncRetry {
		return
	}
	// 昨天的「已签到」不能挂在今天：先清掉，期间菜单回到未同步的初始态。
	if cs.signSyncedDate != "" && cs.signSyncedDate != today {
		cs.sign = coinSignStatus{}
		cs.signErr = ""
	}
	cs.signSyncSeq++
	seq := cs.signSyncSeq
	cs.signSyncing = true
	cs.signSyncTriedAt = time.Now()
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), coinSignTimeout)
	go func() {
		defer cancel()
		status, err := a.client.fetchCoinSign(ctx)
		a.publish(func() {
			if a.coins != cs || cs.signSyncSeq != seq {
				return
			}
			cs.signSyncing = false
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话。
			if err != nil || !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			a.applyCoinSign(status)
		})
	}()
}

// applyCoinSign 把读到的签到状态落到当前账号的状态与本地缓存里。调用方负责
// 确认结果仍属于当前会话，并且运行在 UI 线程。
func (a *app) applyCoinSign(sign coinSignStatus) {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.sign = sign
	cs.signSyncedDate = coinTodayDate()
	cs.signErr = ""
	a.saveCoinCache()
}

func (a *app) loadCoinBase() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.baseSeq++
	seq := cs.baseSeq
	cs.baseLoading = true
	cs.baseLoaded = false
	cs.baseWait = false
	cs.balanceErr = ""
	cs.signErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		balance, balanceErr := a.client.fetchCoinBalance(ctx)
		rankCoins, rank, rankErr := a.client.fetchCoinBalanceRanking(ctx)
		sign, signErr := a.client.fetchCoinSign(ctx)
		pandora, pandoraErr := a.client.fetchCoinPandora(ctx)
		a.publish(func() {
			if a.coins != cs || cs.baseSeq != seq {
				return
			}
			cs.baseLoading = false
			cs.baseLoaded = true
			authHandled := false
			if balanceErr != nil {
				if a.handleAuthErrorFor(toastCoins, balanceErr) {
					authHandled, cs.baseWait = true, true
				} else {
					cs.balanceErr = friendlyError(balanceErr)
				}
			}
			if rankErr == nil && rank > 0 {
				if rankCoins != 0 {
					balance, balanceErr = rankCoins, nil
				}
				cs.rank, cs.rankOK = rank, true
			}
			if balanceErr == nil {
				cs.balance, cs.balanceOK = balance, true
				cs.balanceErr = ""
			} else if !authHandled {
				cs.balanceErr = friendlyError(balanceErr)
			}
			if signErr != nil {
				if !authHandled && a.handleAuthErrorFor(toastCoins, signErr) {
					cs.baseWait = true
				} else if !authHandled {
					cs.signErr = friendlyError(signErr)
				}
			} else {
				// 金币页和账号菜单共用这一份签到状态：同一天不会再单独同步。
				a.applyCoinSign(sign)
			}
			if pandoraErr == nil {
				cs.pandora = pandora
				cs.pandoraSeen = pandora.Known
			}
		})
	}()
}

func (a *app) ensureCoinToday() {
	cs := a.coins
	if cs == nil || cs.todayLoading || cs.todayLoaded || cs.todayWait {
		return
	}
	a.loadCoinToday()
}

func (a *app) loadCoinToday() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.todaySeq++
	seq := cs.todaySeq
	cs.todayLoading = true
	cs.todayLoaded = false
	cs.todayWait = false
	cs.todayErr = ""
	cs.todayNote = ""
	today := coinTodayDate()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchCoinTodayTransactions(ctx)
		from := "today-transaction"
		note := ""
		if err != nil {
			fallback, ferr := a.client.fetchCoinTransactions(ctx, 1, today, today, "")
			if ferr != nil {
				a.publish(func() {
					if a.coins != cs || cs.todaySeq != seq {
						return
					}
					cs.todayLoading = false
					cs.todayLoaded = true
					if a.handleAuthErrorFor(toastCoins, ferr) {
						cs.todayWait = true
						return
					}
					cs.todayErr = friendlyError(ferr)
				})
				return
			}
			items = fallback.Items
			from = "transactions"
			var ae *apiError
			if errors.As(err, &ae) && (ae.Code == "insufficient_scope" || ae.Status == 403) {
				note = "今日明细接口未开通，已按金币流水统计今天。"
			} else {
				note = "今日明细接口暂时不可用，已按金币流水统计今天。"
			}
		}
		income, expense, net := coinTotals(items)
		a.publish(func() {
			if a.coins != cs || cs.todaySeq != seq {
				return
			}
			cs.todayLoading = false
			cs.todayLoaded = true
			cs.todayItems = items
			a.prefetchCoinTransactions(items)
			cs.todayIncome, cs.todayExpense, cs.todayNet = income, expense, net
			cs.todayFrom = from
			cs.todayNote = note
			cs.todayErr = ""
		})
	}()
}

func (a *app) ensureCoinHistory() {
	cs := a.coins
	if cs == nil || cs.historyLoading || cs.historyLoaded || cs.historyWait {
		return
	}
	a.loadCoinHistory(1)
}

// loadCoinHistory 拉第 page 页。page==1 时重建列表；page>1 时按 ID 去重后
// 追加。续拉失败只记 moreErr，保留已有内容。
func (a *app) loadCoinHistory(page int) {
	cs := a.coins
	if cs == nil || page < 1 || cs.historyLoading {
		return
	}
	if page == 1 {
		cs.historySeq++
		cs.historyLoaded = false
		cs.historyItems = nil
		cs.historyNoMore = false
		cs.historyErr = ""
		cs.historyMoreErr = ""
		cs.historyList.ScrollTo(0, ui.Start)
	}
	seq := cs.historySeq
	cs.historyLoading = true
	cs.historyWait = false
	cs.historyPage = page
	start, end := coinRangeDates(cs.historyRange)
	reason := coinReasonCode(cs.historyReason)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchCoinTransactions(ctx, page, start, end, reason)
		a.publish(func() {
			if a.coins != cs || cs.historySeq != seq {
				return
			}
			cs.historyLoading = false
			cs.historyLoaded = true
			if err != nil {
				if a.handleAuthErrorFor(toastCoins, err) {
					cs.historyWait = true
					return
				}
				if page == 1 {
					cs.historyErr = friendlyError(err)
				} else {
					cs.historyMoreErr = friendlyError(err)
				}
				return
			}
			if page == 1 {
				cs.historyItems = res.Items
			} else {
				seen := make(map[string]bool, len(cs.historyItems))
				for _, tx := range cs.historyItems {
					seen[tx.ID] = true
				}
				added := 0
				for _, tx := range res.Items {
					if !seen[tx.ID] {
						cs.historyItems = append(cs.historyItems, tx)
						seen[tx.ID] = true
						added++
					}
				}
				if added == 0 {
					cs.historyNoMore = true
				}
			}
			cs.historyPage = page
			cs.historyTotal = res.Total
			cs.historyTotalPages = res.TotalPages
			if len(res.Items) == 0 || (res.TotalPages > 0 && page >= res.TotalPages) {
				cs.historyNoMore = true
			}
			cs.historyErr = ""
			cs.historyMoreErr = ""
			a.prefetchCoinTransactions(res.Items)
		})
	}()
}

// resetCoinHistory 清空流水列表与分页状态，并让在途请求的结果作废；调用方
// 随后从第一页重拉。时间范围与类型过滤共用这一份重置逻辑。
func (a *app) resetCoinHistory() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.historySeq++
	cs.historyLoading = false
	cs.historyLoaded = false
	cs.historyWait = false
	cs.historyItems = nil
	cs.historyPage = 0
	cs.historyTotal = 0
	cs.historyTotalPages = 0
	cs.historyNoMore = false
	cs.historyErr = ""
	cs.historyMoreErr = ""
	cs.historyList.ScrollTo(0, ui.Start)
}

func (a *app) setCoinHistoryRange(r int) {
	cs := a.coins
	if cs == nil {
		return
	}
	if r < coinRangeAll || r > coinRange30Days {
		r = coinRangeAll
	}
	// 分段控件在 Changed 报告之前就把选中的新值写进了 cs.historyRange，
	// 所以这里不能比较新旧值：那样每次切换都会被当成「没变」而不重拉。
	cs.historyRange = r
	a.resetCoinHistory()
	a.loadCoinHistory(1)
}

// setCoinHistoryReason 切换类型过滤。和 setCoinHistoryRange 一样不比较新旧
// 标签：下拉在 Changed 报告前已经写入 cs.historyReason。
func (a *app) setCoinHistoryReason(label string) {
	cs := a.coins
	if cs == nil {
		return
	}
	if label == "" {
		label = coinReasonAll
	}
	cs.historyReason = label
	a.resetCoinHistory()
	a.loadCoinHistory(1)
}

func (a *app) ensureCoinStats() {
	cs := a.coins
	if cs == nil {
		return
	}
	if !cs.dailyLoaded && !cs.dailyLoading {
		a.loadCoinDaily()
	}
	if !cs.breakdownLoaded && !cs.breakdownLoading {
		a.loadCoinBreakdown()
	}
}

func (a *app) loadCoinDaily() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.dailySeq++
	seq := cs.dailySeq
	cs.dailyLoading = true
	cs.dailyLoaded = false
	cs.dailyErr = ""
	start := coinNow().AddDate(0, 0, -(coinFlowDays - 1)).Format("2006-01-02")
	end := coinTodayDate()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchCoinDailyFlow(ctx, start, end)
		a.publish(func() {
			if a.coins != cs || cs.dailySeq != seq {
				return
			}
			cs.dailyLoading = false
			cs.dailyLoaded = true
			if err != nil {
				cs.dailyErr = coinOptionalNote(err)
				return
			}
			coinSortDailyNewestFirst(items)
			cs.dailyItems = items
			cs.dailyErr = ""
		})
	}()
}

func (a *app) loadCoinBreakdown() {
	cs := a.coins
	if cs == nil {
		return
	}
	// 「注册至今」要先知道注册日。先清掉上一次的范围数据，避免切换后
	// 短暂显示旧区间的分类；取日期期间保持 loading，取到后再真正拉分类。
	cs.breakdownSeq++
	seq := cs.breakdownSeq
	cs.breakdownLoading = true
	cs.breakdownLoaded = false
	cs.breakdownItems = nil
	cs.breakdownErr = ""
	if cs.breakdownRange == coinBreakdownAll && cs.registeredDate == "" {
		// 注册日可能在 ensure 里同步复用成功；无论哪种，分类都由它接着拉。
		a.ensureCoinRegisteredDate(seq)
		return
	}
	start, end := coinBreakdownDates(cs.breakdownRange, cs.registeredDate)
	a.fetchCoinBreakdown(cs, seq, start, end)
}

func (a *app) fetchCoinBreakdown(cs *coinState, seq uint64, start, end string) {
	flowType := cs.breakdownType
	if flowType == "" {
		flowType = "income"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchCoinBreakdown(ctx, start, end, flowType)
		a.publish(func() {
			if a.coins != cs || cs.breakdownSeq != seq {
				return
			}
			cs.breakdownLoading = false
			cs.breakdownLoaded = true
			if err != nil {
				cs.breakdownErr = coinOptionalNote(err)
				return
			}
			coinSortBreakdownDesc(items)
			cs.breakdownItems = items
			cs.breakdownErr = ""
		})
	}()
}

// ensureCoinRegisteredDate 取账号注册日，供「注册至今」当起点。优先复用「我的」
// 窗口已经拉到的本人资料，没加载过才单独请求一次 /v1/me；失败只提示不反复重试，
// 用户点「刷新」时由 reloadCoins 清掉状态再试。
func (a *app) ensureCoinRegisteredDate(seq uint64) {
	cs := a.coins
	if cs == nil || cs.registeredDate != "" || cs.registeredLoading {
		return
	}
	// 「我的」窗口看过一次就能直接复用，省一个请求。
	if a.profile != nil && a.profile.infoLoaded {
		if d := coinRegisteredDate(a.profile.user.CreatedAt); d != "" {
			cs.registeredDate = d
			cs.registeredTried = true
			start, end := coinBreakdownDates(coinBreakdownAll, d)
			a.fetchCoinBreakdown(cs, seq, start, end)
			return
		}
	}
	if cs.registeredTried {
		cs.breakdownLoading = false
		cs.breakdownLoaded = true
		cs.breakdownErr = "未能获取注册日期，点击右上角「刷新」重试"
		return
	}
	cs.registeredLoading = true
	cs.registeredTried = true
	cs.registeredSeq++
	rs := cs.registeredSeq
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		d, err := a.client.fetchCoinRegisteredDate(ctx)
		a.publish(func() {
			if a.coins != cs || cs.registeredSeq != rs {
				return
			}
			cs.registeredLoading = false
			if err == nil {
				cs.registeredDate = d
			}
			// 注册日期是账号级信息，拿到就留着给以后用。期间用户切过范围、
			// 又切回「注册至今」时 breakdownSeq 已经变了，这里用当前的 seq
			// 重新拉一次，别让那一次切换永远停在 loading。
			if cs.breakdownRange != coinBreakdownAll || !cs.breakdownLoading {
				return
			}
			if cs.registeredDate == "" {
				cs.breakdownLoading = false
				cs.breakdownLoaded = true
				cs.breakdownErr = "未能获取注册日期，点击右上角「刷新」重试"
				return
			}
			start, end := coinBreakdownDates(coinBreakdownAll, cs.registeredDate)
			a.fetchCoinBreakdown(cs, cs.breakdownSeq, start, end)
		})
	}()
}

func (a *app) setCoinBreakdownRange(r int) {
	cs := a.coins
	if cs == nil || r < 0 || r >= len(coinBreakdownRangeLabels) || r == cs.breakdownRange {
		return
	}
	cs.breakdownRange = r
	cs.breakdownSeq++
	cs.breakdownLoading = false
	cs.breakdownLoaded = false
	cs.breakdownItems = nil
	cs.breakdownErr = ""
	a.loadCoinBreakdown()
}

func (a *app) setCoinBreakdownType(flowType string) {
	cs := a.coins
	if cs == nil || flowType == "" || flowType == cs.breakdownType {
		return
	}
	cs.breakdownType = flowType
	cs.breakdownSeq++
	cs.breakdownLoading = false
	cs.breakdownLoaded = false
	cs.breakdownItems = nil
	cs.breakdownErr = ""
	a.loadCoinBreakdown()
}

func (a *app) ensureCoinRanking() {
	cs := a.coins
	if cs == nil || cs.rankingLoading || cs.rankingLoaded {
		return
	}
	a.loadCoinRanking()
}

func (a *app) loadCoinRanking() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.rankingSeq++
	seq := cs.rankingSeq
	cs.rankingLoading = true
	cs.rankingLoaded = false
	cs.rankingErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchCoinRanking(ctx)
		a.publish(func() {
			if a.coins != cs || cs.rankingSeq != seq {
				return
			}
			cs.rankingLoading = false
			cs.rankingLoaded = true
			if err != nil {
				cs.rankingErr = coinOptionalNote(err)
				return
			}
			cs.rankingItems = items
			cs.rankingErr = ""
			urls := make([]string, 0, len(items))
			for _, e := range items {
				urls = append(urls, e.AvatarURL)
			}
			a.prefetchAvatars(urls...)
			if !cs.rankOK {
				name := a.auth.username()
				for _, e := range items {
					if name != "" && strings.EqualFold(name, e.Username) {
						cs.rank, cs.rankOK = e.Rank, true
						break
					}
				}
			}
		})
	}()
}

func (a *app) reloadCoinBase() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.baseSeq++
	cs.baseLoading = false
	cs.baseLoaded = false
	cs.baseWait = false
	cs.balanceErr = ""
	cs.signErr = ""
	a.loadCoinBase()
}

func (a *app) reloadCoinToday() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.todaySeq++
	cs.todayLoading = false
	cs.todayLoaded = false
	cs.todayWait = false
	cs.todayErr = ""
	cs.todayNote = ""
	a.loadCoinToday()
}

func (a *app) reloadCoinHistory() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.historySeq++
	cs.historyLoading = false
	cs.historyLoaded = false
	cs.historyWait = false
	cs.historyErr = ""
	cs.historyMoreErr = ""
	a.loadCoinHistory(1)
}

func (a *app) reloadCoinDaily() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.dailySeq++
	cs.dailyLoading = false
	cs.dailyLoaded = false
	cs.dailyErr = ""
	a.loadCoinDaily()
}

func (a *app) reloadCoinRanking() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.rankingSeq++
	cs.rankingLoading = false
	cs.rankingLoaded = false
	cs.rankingErr = ""
	a.loadCoinRanking()
}

// reloadCoins 是顶部「刷新」：保留当前视图、时间范围、类型过滤与收入/支出
// 页签，其余状态全部清空后按需重拉。
func (a *app) reloadCoins() {
	cs := a.coins
	if cs == nil {
		return
	}
	view, historyRange, breakdownType, breakdownRange := cs.view, cs.historyRange, cs.breakdownType, cs.breakdownRange
	historyReason := cs.historyReason
	*cs = *newCoinState()
	cs.view = view
	cs.historyRange = historyRange
	cs.historyReason = historyReason
	cs.breakdownType = breakdownType
	cs.breakdownRange = breakdownRange
	a.ensureCoinsLoaded()
}
