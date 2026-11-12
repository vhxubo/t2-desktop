package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 金币窗口的界面：标题栏下面是余额 / 今日获得 / 签到状态，再按「今日、历史、
// 统计、排行」切换。窗口本身在 coins_window.go；这里只画窗口里的内容。
// 金币窗口里的写操作只有签到（POST /v1/me/sign）：余额卡片里有一颗签到
// 按钮，结果以 toast 报告。发帖、回复、表态与打赏在各自的页面里，成功后发出
// 金币变动事件（见 coin_events.go），本页已经加载过的块会一起刷新。

const (
	coinViewToday = iota
	coinViewHistory
	coinViewStats
	coinViewRanking
)

var coinViewLabels = []string{"今日", "历史", "统计", "排行"}

func (a *app) coinsBody(c *ui.Context) {
	cs := a.coins
	if cs == nil {
		return
	}
	// 窗口标题栏已经写着「金币」与余额，这里不再重复一个大标题：第一行
	// 只放视图切换、签到状态与刷新。
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		ui.Row(c).FillWidth().Padding(10, 14, 8, 14).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c, &cs.view, coinViewLabels...).Label("金币视图")
			ui.Spacer(c).Grow(1)
			a.coinSignLine(c, cs)
			busy := cs.baseLoading || cs.todayLoading || cs.historyLoading ||
				cs.dailyLoading || cs.breakdownLoading || cs.rankingLoading || cs.signBusy
			a.smallAction(c, "刷新", busy, func() { a.reloadCoins() })
		})
		ui.Divider(c)
		a.coinBalanceCard(c, cs)
		a.coinCoreError(c, cs)
		switch cs.view {
		case coinViewHistory:
			a.coinHistoryView(c, cs)
		case coinViewStats:
			a.coinStatsView(c, cs)
		case coinViewRanking:
			a.coinRankingView(c, cs)
		default:
			a.coinTodayView(c, cs)
		}
	})
}

// coinSignLine 是分段栏右侧的一行签到状态：只说明今天签没签，按钮放在下面
// 的余额卡片里，避免一行里塞两个入口。
func (a *app) coinSignLine(c *ui.Context, cs *coinState) {
	t := c.Theme()
	switch {
	case cs.signErr != "":
		ui.Text(c, "签到状态暂不可用").FontSize(11).TextColor(t.TextMuted).SingleLine()
	case cs.sign.Known && cs.sign.Signed:
		text := "今日已签到"
		if cs.sign.Streak > 0 {
			text = fmt.Sprintf("今日已签到 · 连续 %d 天", cs.sign.Streak)
		}
		ui.Text(c, text).FontSize(11).TextColor(t.Success).SingleLine()
	case cs.sign.Known:
		ui.Text(c, "今日未签到").FontSize(11).TextColor(t.TextMuted).SingleLine()
	case cs.baseLoaded:
		ui.Text(c, "签到状态未知").FontSize(11).TextColor(t.TextMuted).SingleLine()
	default:
		ui.Text(c, "签到状态加载中…").FontSize(11).TextColor(t.TextMuted).SingleLine()
	}
}

func (a *app) coinBalanceCard(c *ui.Context, cs *coinState) {
	t := c.Theme()
	ui.Box(c).FillWidth().Padding(12, 14).Radius(10).Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(20).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "当前余额").FontSize(11).TextColor(t.TextMuted)
				if cs.baseLoading && !cs.balanceOK {
					ui.Spinner(c).Size(20, 20)
				} else if cs.balanceOK {
					ui.Text(c, coinThousands(cs.balance)+" 金币").FontSize(26).Bold().SingleLine()
				} else {
					ui.Text(c, "—").FontSize(26).Bold()
				}
				if cs.rankOK {
					ui.Textf(c, "站内排名 第 %d 名", cs.rank).FontSize(11).TextColor(t.TextMuted)
				}
			})

			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, "今日获得").FontSize(11).TextColor(t.TextMuted)
				switch {
				case cs.todayLoading && !cs.todayLoaded:
					ui.Spinner(c).Size(18, 18)
				case cs.todayErr != "":
					ui.Text(c, "—").FontSize(20).Bold().TextColor(t.TextMuted)
				default:
					ui.Text(c, "+"+coinAmountText(cs.todayIncome)+" 金币").FontSize(20).Bold().TextColor(t.Success).SingleLine()
					ui.Textf(c, "支出 %s · 净变化 %s", coinExpenseText(cs.todayExpense), coinSignedText(cs.todayNet)).
						FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})

			ui.Column(c).Shrink(0).Gap(4).Children(func() {
				ui.Text(c, "今日签到").FontSize(11).TextColor(t.TextMuted)
				switch {
				case cs.sign.Known && cs.sign.Signed:
					text := "已签到"
					if cs.sign.Streak > 0 {
						text = fmt.Sprintf("已签到 · %d 天", cs.sign.Streak)
					}
					ui.Text(c, text).FontSize(12).Bold().TextColor(t.Success)
				case cs.signBusy:
					ui.Spinner(c).Size(16, 16)
				case !cs.sign.Known && !cs.baseLoaded:
					ui.Text(c, "加载中…").FontSize(12).TextColor(t.TextMuted)
				default:
					a.coinSignButton(c, cs)
				}
			})
		})
	})
}

// coinSignButton 是余额卡片里的签到按钮：只在今天还没签到时出现。签到会
// 改变金币，结果一律交给 toast（见 signInNow），按钮本身不写结果文字。
func (a *app) coinSignButton(c *ui.Context, cs *coinState) {
	b := ui.PrimaryButton(c, "签到")
	tip := "签到领金币"
	if cs.signErr != "" {
		// 读签到状态失败不影响提交：一次签到也可能把缺的权限补出来。
		tip = "签到状态未知，仍可尝试签到"
	}
	// 可访问名与可见文字分开：流水行里的「签到」也是同一个词，读屏与测试
	// 需要能准确指到这颗按钮。
	b.Label("立即签到").Tooltip(tip)
	if b.Clicked() {
		a.signInNow()
	}
}

// signInNow 提交今日签到。未登录或缺少 write:economy 时走全局授权弹窗，
// 授权成功后同一次签到自动重跑；今日重复签到（409 conflict）按「已经签过」
// 处理。成功后先用响应里的状态点亮按钮，再刷新余额与今日明细，因为签到会
// 同时改变金币与流水。
func (a *app) signInNow() {
	cs := a.coins
	if cs == nil || cs.signBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能签到。", a.signInNow)
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
				if a.handleAuthError(err, a.signInNow) {
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
			// 余额、排名与今日明细都跟着签到变了：先发全局金币刷新事件，
			// 把已经打开过的流水、统计与排行一起重拉，再补拉 base 与今日
			// 明细——它们不必先加载过，签到本身已经证明这两块变了。
			a.coinsChanged(coinEventSign)
			a.reloadCoinBase()
			a.reloadCoinToday()
		})
	}()
}

// coinCoreError 展示余额 / 今日 / 流水等核心端点的普通错误。缺权限的失败
// 已经由全局授权弹窗接管（authWait），这里不再重复报错。
func (a *app) coinCoreError(c *ui.Context, cs *coinState) {
	t := c.Theme()
	if cs.balanceErr != "" {
		a.coinInlineError(c, cs.balanceErr, func() { a.reloadCoinBase() })
	}
	if cs.todayNote != "" {
		ui.Text(c, cs.todayNote).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	}
	switch {
	case cs.todayErr != "":
		a.coinInlineError(c, cs.todayErr, func() { a.reloadCoinToday() })
	case cs.todayWait:
		ui.Text(c, "今日金币明细在等待授权后继续加载。").FontSize(11).TextColor(t.TextMuted)
	}
}

func (a *app) coinInlineError(c *ui.Context, text string, retry func()) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(11).TextColor(t.Danger).MaxLines(2).Grow(1)
		a.smallAction(c, "重试", false, retry)
	})
}

func (a *app) coinTodayView(c *ui.Context, cs *coinState) {
	t := c.Theme()
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(8, 14).Gap(10).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "今日明细").FontSize(14).Bold()
			ui.Textf(c, "共 %d 条", len(cs.todayItems)).FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			if cs.todayFrom == "transactions" {
				ui.Text(c, "来自金币流水").FontSize(10).TextColor(t.TextMuted)
			}
		})

		switch {
		case cs.todayLoading && len(cs.todayItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(18, 18)
				ui.Text(c, "正在加载今日金币明细…").FontSize(12).TextColor(t.TextMuted)
			})
		case cs.todayErr != "":
			ui.Text(c, "今日明细暂时无法加载").FontSize(13).Bold()
			ui.Text(c, cs.todayErr).FontSize(11).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.reloadCoinToday()
			}
		case len(cs.todayItems) == 0:
			ui.Text(c, "今天还没有金币变动。").FontSize(12).TextColor(t.TextMuted)
		default:
			for _, tx := range cs.todayItems {
				a.coinTransactionRow(c, tx, true)
			}
		}

		if cs.pandoraSeen {
			a.coinPandoraLine(c, cs)
		}
	})
}

func (a *app) coinHistoryView(c *ui.Context, cs *coinState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, 14, 6, 14).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "金币流水").FontSize(14).Bold()
		if ui.Segmented(c, &cs.historyRange, coinRangeLabels...).Label("时间范围").Changed() {
			a.setCoinHistoryRange(cs.historyRange)
		}
		ui.Spacer(c).Grow(1)
		if cs.historyTotal > 0 {
			ui.Textf(c, "共 %d 条记录", cs.historyTotal).FontSize(11).TextColor(t.TextMuted)
		}
	})
	a.coinHistoryReasonRow(c, cs)

	switch {
	case cs.historyLoading && len(cs.historyItems) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载金币流水…").TextColor(t.TextMuted)
		})
		return
	case cs.historyErr != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "金币流水加载失败").FontSize(15).Bold()
			ui.Text(c, cs.historyErr).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.reloadCoinHistory()
			}
		})
		return
	case len(cs.historyItems) == 0:
		ui.Column(c).Grow(1).Center().Children(func() {
			text := "这个时间范围没有金币记录。"
			if code := coinReasonCode(cs.historyReason); code != "" {
				text = "这个时间范围没有「" + cs.historyReason + "」的金币记录。"
			}
			ui.Text(c, text).TextColor(t.TextMuted)
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
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "类型").FontSize(11).TextColor(t.TextMuted)
		filter := ui.Select(c, &cs.historyReason, coinReasonOptions).Label("金币类型").
			Tooltip("只看某一类金币变动").Width(150)
		if filter.Changed() {
			a.setCoinHistoryReason(cs.historyReason)
		}
		if coinReasonCode(cs.historyReason) != "" {
			a.smallAction(c, "清除筛选", false, func() { a.setCoinHistoryReason(coinReasonAll) })
		}
		ui.Spacer(c).Grow(1)
	})
}

func (a *app) coinStatsView(c *ui.Context, cs *coinState) {
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(8, 14).Gap(12).Children(func() {
		a.coinDailyCard(c, cs)
		a.coinBreakdownCard(c, cs)
	})
}

func (a *app) coinDailyCard(c *ui.Context, cs *coinState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "近 30 天收支").FontSize(14).Bold()
			ui.Text(c, "按日聚合").FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			a.smallAction(c, "刷新", cs.dailyLoading, func() { a.reloadCoinDaily() })
		})
		switch {
		case cs.dailyErr != "":
			ui.Text(c, cs.dailyErr).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		case cs.dailyLoading && len(cs.dailyItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(16, 16)
				ui.Text(c, "正在加载按日收支…").FontSize(11).TextColor(t.TextMuted)
			})
		case len(cs.dailyItems) == 0:
			ui.Text(c, "暂无可展示的按日数据。").FontSize(11).TextColor(t.TextMuted)
		default:
			for _, day := range cs.dailyItems {
				a.coinDailyRow(c, day)
			}
		}
	})
	ui.Divider(c)
}

func (a *app) coinDailyRow(c *ui.Context, day coinDailyFlow) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, day.Date).FontSize(11).TextColor(t.TextMuted).Width(96)
		ui.Text(c, "+"+coinAmountText(day.Income)).FontSize(11).TextColor(t.Success).Width(90).TextAlign(ui.End)
		ui.Text(c, coinExpenseText(day.Expense)).FontSize(11).TextColor(t.Danger).Width(90).TextAlign(ui.End)
		ui.Spacer(c).Grow(1)
		ui.Text(c, coinSignedText(day.Net)).FontSize(12).Bold().TextColor(coinAmountColor(t, day.Net)).TextAlign(ui.End)
	})
}

func (a *app) coinBreakdownCard(c *ui.Context, cs *coinState) {
	t := c.Theme()
	title := "收入构成"
	if cs.breakdownType == "expense" {
		title = "支出构成"
	}
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(14).Bold()
			ui.Text(c, "本月").FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			a.coinToggleChip(c, "收入", cs.breakdownType != "expense", func() { a.setCoinBreakdownType("income") })
			a.coinToggleChip(c, "支出", cs.breakdownType == "expense", func() { a.setCoinBreakdownType("expense") })
		})
		switch {
		case cs.breakdownErr != "":
			ui.Text(c, cs.breakdownErr).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		case cs.breakdownLoading && len(cs.breakdownItems) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(16, 16)
				ui.Text(c, "正在加载分类数据…").FontSize(11).TextColor(t.TextMuted)
			})
		case len(cs.breakdownItems) == 0:
			ui.Text(c, "本月暂无"+title+"数据。").FontSize(11).TextColor(t.TextMuted)
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

func (a *app) coinBreakdownRow(c *ui.Context, b coinBreakdown, total float64) {
	t := c.Theme()
	ratio := b.Ratio
	if ratio <= 0 && total > 0 {
		ratio = b.Amount / total
	}
	if ratio < 0 {
		ratio = 0
	}
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, coinReasonLabel(b.Reason)).FontSize(11).Grow(1).SingleLine()
			ui.Text(c, coinAmountText(b.Amount)).FontSize(11).TextColor(t.TextMuted)
			ui.Textf(c, "%.1f%%", ratio*100).FontSize(10).TextColor(t.TextMuted).Width(46).TextAlign(ui.End)
		})
		bar := ui.Box(c).FillWidth().Height(5).Radius(3).Background(t.SurfaceHover)
		bar.Children(func() {
			width := float32(ratio * 100)
			if width > 100 {
				width = 100
			}
			ui.Box(c).WidthPercent(width).Height(5).Radius(3).Background(t.Accent)
		})
	})
}

func (a *app) coinRankingView(c *ui.Context, cs *coinState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "金币排行榜").FontSize(14).Bold()
		ui.Textf(c, "前 %d 名", coinRankingLimit).FontSize(11).TextColor(t.TextMuted)
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", cs.rankingLoading, func() { a.reloadCoinRanking() })
	})
	switch {
	case cs.rankingErr != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "排行榜暂时无法加载").FontSize(15).Bold()
			ui.Text(c, cs.rankingErr).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.reloadCoinRanking()
			}
		})
		return
	case cs.rankingLoading && len(cs.rankingItems) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载排行榜…").TextColor(t.TextMuted)
		})
		return
	case len(cs.rankingItems) == 0:
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "暂无排行数据。").TextColor(t.TextMuted)
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
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 14).Radius(8).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Textf(c, "%d", e.Rank).Width(36).FontSize(14).Bold().
			TextColor(coinRankColor(t, e.Rank))
		// 排行榜用户名同样走统一用户标识：13px 昵称配 19.5px 头像。
		a.userTag(c, userTagSpec{
			Name:   e.Username,
			Avatar: e.AvatarURL,
			Size:   13,
			Bold:   true,
			Badges: e.Badges,
		})
		if e.IsPro {
			ui.Text(c, "PRO").FontSize(9).TextColor(t.Accent)
		}
		if name := a.auth.username(); name != "" && strings.EqualFold(name, e.Username) {
			ui.Text(c, "（我）").FontSize(10).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		ui.Text(c, coinThousands(e.Balance)+" 金币").FontSize(12).TextColor(t.TextMuted).SingleLine()
	})
	if e.Username != "" {
		row.Tooltip("查看用户主页")
		row.OnClick(func() {
			a.openExternal(siteBaseURL + "/user/" + url.PathEscape(e.Username) + "/about")
		})
	}
}

func (a *app) coinPandoraLine(c *ui.Context, cs *coinState) {
	t := c.Theme()
	status := "今日盲盒：暂无"
	switch {
	case cs.pandora.OpenedToday:
		status = "今日盲盒：已开启"
	case cs.pandora.CanOpen:
		status = "今日盲盒：可开启"
	case cs.pandora.HasBox:
		status = "今日盲盒：已领取"
	}
	row := ui.Row(c).FillWidth().Padding(8, 10).Radius(8).Gap(8).
		AlignItems(ui.Center).Background(t.Surface)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Icon(c, pandoraIcon).Size(14, 14).TextColor(t.Accent)
		ui.Text(c, status).FontSize(11).TextColor(t.Text).SingleLine()
		if cs.pandora.EventTitle != "" {
			ui.Text(c, "· "+cs.pandora.EventTitle).FontSize(11).
				TextColor(t.TextMuted).SingleLine()
		}
		ui.Spacer(c).Grow(1)
		ui.Text(c, "打开潘多拉").FontSize(10).TextColor(t.Accent).SingleLine()
	})
	row.Label("潘多拉盲盒").Tooltip("打开潘多拉窗口，开启盲盒或再抽一次").
		OnClick(a.showPandora)
}

func (a *app) coinTransactionRow(c *ui.Context, tx coinTransaction, today bool) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(8).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, coinReasonLabel(tx.Reason)).FontSize(12).Bold().SingleLine()
					if tx.IsAlias {
						ui.Text(c, "分身").FontSize(10).TextColor(t.TextMuted)
					}
					ui.Text(c, coinWhen(tx.CreatedAt, today)).FontSize(10).TextColor(t.TextMuted).SingleLine()
				})
				if detail := coinTransactionDetail(tx); detail != "" {
					// 「收到打赏 @某人」里的用户名单独交给统一用户标识，
					// 头像跟着 12px 的详情字号走。
					before, user, after := coinTransactionDetailParts(tx)
					if user == "" {
						ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
					} else {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Wrap().Children(func() {
							if before != "" {
								ui.Text(c, before).FontSize(12).TextColor(t.TextMuted)
							}
							a.userTag(c, userTagSpec{
								Name:   user,
								Avatar: tx.FromAvatar,
								Label:  "@" + user,
								Size:   12,
								Color:  t.TextMuted,
								Badges: tx.FromBadges,
							})
							if after != "" {
								ui.Text(c, after).FontSize(12).TextColor(t.TextMuted)
							}
						})
					}
				}
			})
			ui.Column(c).Shrink(0).Gap(1).Children(func() {
				ui.Text(c, coinSignedText(tx.Amount)).FontSize(15).Bold().
					TextColor(coinAmountColor(t, tx.Amount)).TextAlign(ui.End)
				if tx.HasDiscount {
					ui.Text(c, "原价 "+coinAmountText(tx.BaseAmount)).FontSize(9).
						TextColor(t.TextMuted).TextAlign(ui.End)
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

func coinAmountColor(t *ui.Theme, v float64) ui.Color {
	switch {
	case v > 0:
		return t.Success
	case v < 0:
		return t.Danger
	default:
		return t.TextMuted
	}
}

func coinRankColor(t *ui.Theme, rank int) ui.Color {
	switch rank {
	case 1:
		return t.Warning
	case 2:
		return t.Accent
	case 3:
		return t.Success
	default:
		return t.TextMuted
	}
}

func (a *app) coinToggleChip(c *ui.Context, label string, active bool, onClick func()) {
	t := c.Theme()
	chip := ui.Button(c, label).FontSize(11).Padding(2, 9).Radius(999)
	if active {
		chip.Background(t.Accent).TextColor(t.AccentText)
	} else {
		chip.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
	}
	if chip.Clicked() {
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
	}
}

func (a *app) ensureCoinBase() {
	cs := a.coins
	if cs == nil || cs.baseLoading || cs.baseLoaded || cs.baseWait {
		return
	}
	a.loadCoinBase()
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
				if a.handleAuthError(balanceErr, func() { a.ensureCoinBase() }) {
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
				if !authHandled && a.handleAuthError(signErr, func() { a.ensureCoinBase() }) {
					cs.baseWait = true
				} else if !authHandled {
					cs.signErr = friendlyError(signErr)
				}
			} else {
				cs.sign = sign
				cs.signErr = ""
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
					if a.handleAuthError(ferr, func() { a.ensureCoinToday() }) {
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
				if a.handleAuthError(err, func() { a.ensureCoinHistory() }) {
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
	cs.breakdownSeq++
	seq := cs.breakdownSeq
	cs.breakdownLoading = true
	cs.breakdownLoaded = false
	cs.breakdownErr = ""
	flowType := cs.breakdownType
	if flowType == "" {
		flowType = "income"
	}
	start := coinMonthStart()
	end := coinTodayDate()
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
	view, historyRange, breakdownType := cs.view, cs.historyRange, cs.breakdownType
	historyReason := cs.historyReason
	*cs = *newCoinState()
	cs.view = view
	cs.historyRange = historyRange
	cs.historyReason = historyReason
	cs.breakdownType = breakdownType
	a.ensureCoinsLoaded()
}
