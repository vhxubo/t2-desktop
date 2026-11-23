package main

import (
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 潘多拉抽卡卡片：只展示当前这一张。没有盒子时给一颗抽取按钮；抽到之后
// 展示事件与稀有度，若还能重抽，旁边给一个重置图标，点它才抽第二张。
// 进度格、倒计时、规则说明和结果 toast 都不在这一层出现。

func (a *app) pandoraCard(c *ui.Context, st *pandoraState) {
	tk := lobetheme.TokensOf(c)
	switch {
	case st.err != "" || st.wait:
		// 整块读不到时说明原因并给重试入口，这是页面级状态，留在卡里。
		ui.Column(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, pandoraCardError(st)).FontSize(12).TextColor(tk.ErrorText).
				MaxLines(2)
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadPandora()
			}
		})
	case st.busy != "":
		a.pandoraCardProgress(c, "抽取中…")
	case st.loading && !st.loaded:
		a.pandoraCardProgress(c, "读取中…")
	case st.status.BoxKnown && (st.status.OpenedToday || st.status.HasBox):
		a.pandoraDrawnCard(c, st)
	case st.status.CanOpen:
		a.pandoraUnopenedCard(c)
	default:
		ui.Text(c, "今天没有可抽的盲盒").FontSize(13).TextColor(tk.TextSecondary)
	}
}

func (a *app) pandoraCardProgress(c *ui.Context, text string) {
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Padding(36, 0).Gap(10).AlignItems(ui.Center).
		Justify(ui.Center).Children(func() {
		lobeSpin(c, feedback.SpinProps{CustomSize: 18})
		ui.Text(c, text).FontSize(13).TextColor(tk.TextSecondary)
	})
}

// pandoraUnopenedCard 是还没抽的卡背：一个礼物盒加一颗抽取按钮，不写多余
// 的规则说明。
func (a *app) pandoraUnopenedCard(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).FillWidth().Gap(16).Children(func() {
		ui.Box(c).FillWidth().MinHeight(180).Radius(14).Center().
			Background(tk.FillTertiary).Border(1, tk.Border).Children(func() {
			ui.Icon(c, pandoraIcon).Size(46, 46).TextColor(tk.Primary)
		})
		b := action.Button(c, "抽取", action.ButtonProps{Variant: action.Primary})
		b.Label("抽取潘多拉盲盒")
		if b.Clicked() {
			a.openPandoraBox()
		}
	})
}

// pandoraDrawnCard 展示本次抽到的那一张：稀有度、事件名与效果。抽完第一张
// 后若还有重抽机会，给一个重置图标；不点它就停在这张结果上，不发第二次
// 请求。
func (a *app) pandoraDrawnCard(c *ui.Context, st *pandoraState) {
	tk := lobetheme.TokensOf(c)
	box := st.status.Box
	rarity := pandoraBoxRarity(box)
	tint := pandoraRarityColor(c.Theme(), rarity)
	label := pandoraRarityLabel(rarity)
	name := strings.TrimSpace(box.EventName)
	if name == "" {
		name = strings.TrimSpace(box.EventCode)
	}
	effect := strings.TrimSpace(box.EventTitle)

	ui.Column(c).FillWidth().Gap(12).Children(func() {
		ui.Box(c).FillWidth().MinHeight(180).Padding(20, 16).Radius(14).
			Background(tint.Mix(tk.BgContainer, 0.88)).
			Border(1, tint.Mix(tk.BgContainer, 0.55)).Children(func() {
			ui.Column(c).Fill().Gap(10).AlignItems(ui.Center).Justify(ui.Center).
				Children(func() {
					ui.Box(c).Size(52, 52).Radius(14).Center().
						Background(tint.Mix(tk.BgContainer, 0.78)).Children(func() {
						ui.Icon(c, pandoraIcon).Size(28, 28).TextColor(tint)
					})
					if label != "" {
						ui.Text(c, label).FontSize(11).Bold().TextColor(tint)
					}
					if name != "" {
						ui.Text(c, name).FontSize(22).Bold().TextColor(tk.Text)
					}
					if effect != "" {
						ui.Text(c, effect).FontSize(13).TextColor(tk.TextSecondary).
							MaxLines(2)
					}
				})
		})
		// 重置只负责「再抽一次」：点了才发第二次请求，不点就什么都不做。
		if st.status.canReroll(pandoraNow()) {
			ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
				b := action.ActionIcon(c, lucide.RotateCcw(), action.ActionIconProps{
					Size:  lobetheme.Large,
					Title: "重抽一次",
				})
				if b.Clicked() {
					a.rerollPandoraBox()
				}
			})
		}
	})
}

func pandoraCardError(st *pandoraState) string {
	if st.wait {
		return "盲盒加载失败，请授权后重新打开"
	}
	return "盲盒暂时读不到：" + st.err
}

// pandoraBoxRarity 优先用 rarity，缺省回退 tier_code（线上两者通常相同）。
func pandoraBoxRarity(box pandoraBox) string {
	if box.Rarity != "" {
		return box.Rarity
	}
	return box.TierCode
}
