package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 潘多拉窗口的内容：顶部是一张「今日抽奖」大卡片，明确写出两次抽奖的
// 进度与下一步按钮；下面是本次事件的展示与规则说明。
//
// 两次抽奖的产品约定见 pandora.go 的 canReroll：开启一次 + 重抽一次，
// 服务端就算给了更多重抽次数，这里也不提供第三个入口。

func (a *app) pandoraBody(c *ui.Context, st *pandoraState) {
	ui.Scroll(c).Fill().MinHeight(0).Padding(14, 16).Gap(14).Children(func() {
		a.pandoraHeroCard(c, st)
		if st.err != "" || st.wait {
			a.pandoraErrorCard(c, st)
		}
		if st.status.BoxKnown {
			a.pandoraEventCard(c, st)
		}
		a.pandoraRulesCard(c, st)
	})
}

// pandoraHeroCard 是窗口里最显眼的一块：今天抽到了第几次、还能不能抽，
// 以及那颗真正发起抽奖的按钮。
func (a *app) pandoraHeroCard(c *ui.Context, st *pandoraState) {
	t := c.Theme()
	now := pandoraNow()
	accentTint := t.Accent.Mix(t.Background, 0.90)
	ui.Box(c).FillWidth().Padding(16, 16).Radius(14).
		Background(accentTint).Border(1, t.Accent.Mix(t.Background, 0.62)).
		Children(func() {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
				ui.Box(c).Size(42, 42).Shrink(0).Radius(12).Center().
					Background(t.Accent.Mix(t.Background, 0.80)).Children(func() {
					ui.Icon(c, pandoraIcon).Size(24, 24).TextColor(t.Accent)
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "今日潘多拉").FontSize(18).Bold().SingleLine()
						ui.Spacer(c).Grow(1)
						a.smallAction(c, "刷新", st.loading || st.busy != "", a.reloadPandora)
					})
					ui.Text(c, a.pandoraHeroSubtitle(st)).FontSize(12).
						TextColor(t.TextMuted).MaxLines(2)
				})
				ui.Column(c).Shrink(0).Gap(0).AlignItems(ui.End).Children(func() {
					ui.Textf(c, "%d / 2", st.status.drawsUsed()).FontSize(24).Bold().
						TextColor(t.Accent).SingleLine()
					ui.Text(c, "今日抽奖").FontSize(10).TextColor(t.TextMuted).SingleLine()
				})
			})

			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Stretch).Children(func() {
				a.pandoraStepChip(c, "第一次", "开启盲盒", pandoraStepOne(st, now))
				a.pandoraStepChip(c, "第二次", "再抽一次", pandoraStepTwo(st, now))
			})

			a.pandoraDrawAction(c, st, now)

			if st.status.NextOpenAt != "" {
				ui.Text(c, "下一次发放："+pandoraWhenText(st.status.NextOpenAt)+
					pandoraRemainSuffix(st.status.NextOpenAt)).
					FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}
			ui.Text(c, "抽奖可能消耗金币或改变站内状态，结果与扣费以服务端返回为准。").
				FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		})
}

// pandoraStepState 是两次抽奖里某一步的状态：等待、正在进行、已完成、
// 不可用。颜色与文案都由它决定。
type pandoraStepState int

const (
	pandoraStepPending pandoraStepState = iota
	pandoraStepActive
	pandoraStepDone
	pandoraStepOff
)

func pandoraStepOne(st *pandoraState, now time.Time) pandoraStepState {
	switch {
	case st.status.CanOpen:
		return pandoraStepActive
	case st.status.OpenedToday:
		return pandoraStepDone
	case st.status.Known:
		return pandoraStepOff
	}
	return pandoraStepPending
}

func pandoraStepTwo(st *pandoraState, now time.Time) pandoraStepState {
	switch {
	case st.status.canReroll(now):
		return pandoraStepActive
	case st.status.BoxKnown && st.status.Box.RerollUsed > 0:
		return pandoraStepDone
	case st.status.OpenedToday:
		return pandoraStepOff
	}
	return pandoraStepPending
}

// pandoraStepChip 画「第一次 / 第二次」的进度格。完成用成功色，进行中用
// 强调色，不可用保持中性灰 —— 不靠颜色单独传达状态，旁边始终有文字。
func (a *app) pandoraStepChip(c *ui.Context, step, label string, state pandoraStepState) {
	t := c.Theme()
	background := t.Background
	border := t.Border
	ink := t.TextMuted
	detail := ""
	switch state {
	case pandoraStepActive:
		background = t.Accent.Mix(t.Background, 0.90)
		border = t.Accent
		ink = t.Accent
		detail = "待抽取"
	case pandoraStepDone:
		background = t.Success.Mix(t.Background, 0.90)
		border = t.Success.Mix(t.Background, 0.55)
		ink = t.Success
		detail = "已完成"
	case pandoraStepOff:
		detail = "不可用"
	default:
		detail = "未开始"
	}
	ui.Box(c).Grow(1).MinWidth(0).Padding(9, 10).Radius(9).
		Background(background).Border(1, border).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, step+" · "+label).FontSize(12).Bold().SingleLine().TextColor(ink)
			ui.Text(c, detail).FontSize(10).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// pandoraDrawAction 是抽奖按钮区：有次数时给一颗主按钮，没有时说明为什么。
func (a *app) pandoraDrawAction(c *ui.Context, st *pandoraState, now time.Time) {
	t := c.Theme()
	switch {
	case st.busy == "open":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(16, 16)
			ui.Text(c, "正在开启盲盒…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case st.busy == "reroll":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(16, 16)
			ui.Text(c, "正在重抽…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	if st.status.CanOpen {
		b := ui.PrimaryButton(c, "开启盲盒（第 1 次）")
		b.Label("开启潘多拉盲盒")
		b.Tooltip("开启今天的潘多拉盲盒，抽完还能再重抽一次")
		if b.Clicked() {
			a.openPandoraBox()
		}
		ui.Text(c, "开启后会展示本次事件；今天还有一次重抽机会。").
			FontSize(11).TextColor(t.TextMuted)
		return
	}
	if st.status.canReroll(now) {
		b := ui.PrimaryButton(c, "再抽一次（第 2 次）")
		b.Label("重抽潘多拉盲盒")
		tip := "用一次重抽机会，再抽一个结果"
		if box := st.status.Box; box.RerollDeadlineAt != "" {
			tip += "；截止 " + pandoraClock(box.RerollDeadlineAt)
		}
		b.Tooltip(tip)
		if b.Clicked() {
			a.rerollPandoraBox()
		}
		detail := "重抽会再取一个结果；今天的两次抽奖用完就不再提供入口。"
		if box := st.status.Box; box.RerollDeadlineAt != "" {
			detail = "重抽截止 " + pandoraClock(box.RerollDeadlineAt) + "；" + detail
		}
		ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		return
	}
	switch {
	case st.status.OpenedToday || st.status.HasBox:
		ui.Text(c, "今天的两次抽奖已用完，明天再来。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(2)
	default:
		ui.Text(c, "今天还没有可抽的盲盒，下一次发放后再来。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(2)
	}
}

func (a *app) pandoraHeroSubtitle(st *pandoraState) string {
	switch {
	case st.loading && !st.loaded:
		return "正在读取今日盲盒…"
	case st.wait:
		return "等待授权后继续读取今日盲盒。"
	case st.err != "":
		return "今日盲盒状态暂时不可用，可以稍后刷新"
	case st.status.CanOpen:
		return "第一次抽奖已就绪：开启今天的盲盒，看看会抽到什么事件。"
	case st.status.canReroll(pandoraNow()):
		return "第一次已开启；今天还有一次重抽机会，截止前可以再抽一次。"
	case st.status.OpenedToday:
		return "今天的两次抽奖都已结束，明天 0 点前后会发放新的盲盒。"
	}
	return "今天还没有盲盒发放，下一次发放时间见下方。"
}

func (a *app) pandoraErrorCard(c *ui.Context, st *pandoraState) {
	t := c.Theme()
	text := "今日盲盒暂时读不到：" + st.err
	if st.wait {
		text = "今日盲盒需要授权后才能读取；授权完成会自动继续。"
	}
	ui.Row(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Danger.Mix(t.Background, 0.92)).
		Border(1, t.Danger.Mix(t.Background, 0.60)).
		Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(11).
			TextColor(t.Danger).MaxLines(3).Grow(1)
		a.smallAction(c, "重试", st.loading, a.reloadPandora)
	})
}

// pandoraEventCard 展示本次抽到的事件：稀有度、事件名、站点拼好的标题、
// 数值、生效与到期时间，以及重抽的进度。
func (a *app) pandoraEventCard(c *ui.Context, st *pandoraState) {
	t := c.Theme()
	box := st.status.Box
	rarity := pandoraRarityColor(t, pandoraBoxRarity(box))
	title := "本次事件"
	switch {
	case box.RerollUsed > 0:
		title = "第二次抽奖结果"
	case st.status.OpenedToday || st.status.HasBox:
		title = "第一次抽奖结果"
	}
	ui.Box(c).FillWidth().Padding(14, 16).Radius(12).
		Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(13).Bold().SingleLine()
			ui.Spacer(c).Grow(1)
			if label := pandoraRarityLabel(pandoraBoxRarity(box)); label != "" {
				ui.Box(c).Padding(2, 8).Radius(999).
					Background(rarity.Mix(t.Background, 0.86)).
					Border(1, rarity.Mix(t.Background, 0.55)).Children(func() {
					ui.Text(c, "稀有度 "+label).FontSize(10).Bold().TextColor(rarity).SingleLine()
				})
			}
		})

		name := strings.TrimSpace(box.EventName)
		if name == "" {
			name = strings.TrimSpace(box.EventCode)
		}
		if name != "" {
			nameColor := rarity
			if pandoraRarityLabel(pandoraBoxRarity(box)) == "" {
				nameColor = t.Text
			}
			ui.Text(c, name).FontSize(20).Bold().SingleLine().TextColor(nameColor)
		}
		if eventTitle := strings.TrimSpace(box.EventTitle); eventTitle != "" &&
			!strings.Contains(name, eventTitle) {
			ui.Text(c, eventTitle).FontSize(13).TextColor(t.Text).MaxLines(3)
		}

		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Wrap().Children(func() {
			if box.RolledKnown {
				a.pandoraFact(c, "抽到的数值", coinAmountText(box.RolledValue))
			}
			if box.ApplyAt != "" {
				a.pandoraFact(c, "生效时间", pandoraClock(box.ApplyAt))
			} else if box.OpenAt != "" {
				a.pandoraFact(c, "开启时间", pandoraClock(box.OpenAt))
			}
			if box.ExpireAt != "" {
				remain := pandoraRemainText(box.ExpireAt, pandoraNow())
				value := pandoraClock(box.ExpireAt)
				if remain != "" {
					value += "（" + remain + "）"
				}
				a.pandoraFact(c, "事件到期", value)
			}
			if st.status.ActiveExtended {
				a.pandoraFact(c, "状态", "已延期")
			}
		})

		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, pandoraRerollText(st)).FontSize(11).
				TextColor(t.TextMuted).MaxLines(2).Grow(1)
			if st.status.canReroll(pandoraNow()) {
				b := ui.PrimaryButton(c, "再抽一次")
				b.Label("重抽潘多拉盲盒")
				if b.Clicked() {
					a.rerollPandoraBox()
				}
			}
		})
	})
}

func (a *app) pandoraFact(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Column(c).Shrink(0).Gap(1).Children(func() {
		ui.Text(c, label).FontSize(10).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).FontSize(12).TextColor(t.Text).SingleLine()
	})
}

// pandoraRerollText 说明当前重抽状态；服务端给了截止时间就一起写出来。
func pandoraRerollText(st *pandoraState) string {
	box := st.status.Box
	switch {
	case box.RerollUsed > 0:
		return "重抽已用完：今天的两次抽奖都结束了。"
	case st.status.canReroll(pandoraNow()):
		if box.RerollDeadlineAt != "" {
			return "今天还能重抽一次，截止 " + pandoraClock(box.RerollDeadlineAt) + "。"
		}
		return "今天还能重抽一次。"
	case box.RerollLimit <= 0:
		return "这个盲盒没有提供重抽次数。"
	default:
		return "重抽机会已过期或已用完。"
	}
}

func (a *app) pandoraRulesCard(c *ui.Context, st *pandoraState) {
	t := c.Theme()
	balance := ""
	if cs := a.coins; cs != nil && cs.balanceOK {
		balance = "当前金币 " + coinThousands(cs.balance) + "。"
	}
	ui.Column(c).FillWidth().Gap(6).Padding(0, 2).Children(func() {
		ui.Text(c, "关于潘多拉").FontSize(12).Bold().SingleLine()
		for _, line := range []string{
			"每天 0 点前后（东八区）发放一个盲盒，开启后得到一个随机事件。",
			"本应用把一天的抽奖定为两次：先开启，再重抽一次；重抽会用服务端返回的新结果更新展示。",
			"开盒与重抽可能扣金币或改变站内状态，扣费与结果以服务端为准；网络失败不会自动重试，需再点一次。",
			balance,
		} {
			if line != "" {
				ui.Text(c, "· "+line).FontSize(10).TextColor(t.TextMuted).MaxLines(3)
			}
		}
	})
}

// pandoraRemainSuffix 给「下一次发放」补一句剩余时间，解析不了就留空。
func pandoraRemainSuffix(raw string) string {
	if remain := pandoraRemainText(raw, pandoraNow()); remain != "" {
		return "（" + remain + "）"
	}
	return ""
}

// pandoraBoxRarity 优先用 rarity，缺省回退 tier_code（线上两者通常相同）。
func pandoraBoxRarity(box pandoraBox) string {
	if box.Rarity != "" {
		return box.Rarity
	}
	return box.TierCode
}
