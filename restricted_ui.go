package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 受限内容的界面。带 extra.restricted 的帖子在正文末尾多一块，三个只读
// 入口都是用户主动点开才请求：端点需开通，自动请求只会给没开通的账号
// 一次无谓的 403。读取失败留在块里可重试；解锁这类点一下就有结果的
// 操作一律用窗口内 toast 报告（见 toasts.go）。

func (a *app) restrictedSection(c *ui.Context, d *detailState) {
	gate := d.topic.Restricted
	if gate == nil {
		return
	}
	unlocked := restrictedUnlocked(d)
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "受限内容").FontSize(commentFontSize).Bold()
		for _, line := range gate.gateLines() {
			ui.Text(c, line).FontSize(metaFontSize).TextColor(t.TextMuted)
		}
		// 正文以 GET /restricted 读到的为准；详情自带正文时也能直接画。
		var body string
		if info := d.restricted.info; info != nil && info.ContentKnown {
			body = info.Content
		} else if gate.ContentKnown {
			body = gate.Content
		}
		if body != "" {
			a.markdownView(c, body, a.md)
		} else if unlocked {
			ui.Text(c, "已解锁。若正文没有显示，请到原文查看。").
				FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}

		// 三个只读入口。详情打开时已经自动拉取过，这里是手动重试 / 重新读取：
		// 每块显示自己的转圈，其余仍可点。手动点击才走登录 / 授权链路。
		busy := d.restricted.busy
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "查看内容").FontSize(11).
				Disabled(d.restricted.load("info").loading || busy).Clicked() {
				a.loadRestricted(d, "info", false)
			}
			if ui.Button(c, "竞拍状态").FontSize(11).
				Disabled(d.restricted.load("auction").loading || busy).Clicked() {
				a.loadRestricted(d, "auction", false)
			}
			if ui.Button(c, "出价记录").FontSize(11).
				Disabled(d.restricted.load("bids").loading || busy).Clicked() {
				a.loadRestricted(d, "bids", false)
			}
			if a.auth.loggedIn() && !unlocked && gate.CoinsKnown && gate.Coins > 0 {
				if ui.Button(c, fmt.Sprintf("花 %d 金币解锁", gate.Coins)).FontSize(11).
					Disabled(d.restricted.anyLoading() || busy).Clicked() {
					d.restricted.confirm = true
				}
			}
		})
		for _, kind := range []string{"info", "auction", "bids"} {
			if l := d.restricted.load(kind); l.loading {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13)
					ui.Text(c, map[string]string{
						"info": "正在读取受限内容…", "auction": "正在读取竞拍状态…", "bids": "正在读取出价记录…",
					}[kind]).FontSize(11).TextColor(t.TextMuted)
				})
			}
		}
		// 整块读取失败属于页面级状态：每块单独说明为什么空着，并给重试入口。
		for _, kind := range []string{"info", "auction", "bids"} {
			if msg := d.restricted.load(kind).err; msg != "" {
				ui.Text(c, map[string]string{
					"info": "内容", "auction": "竞拍状态", "bids": "出价记录",
				}[kind]+"读取失败："+msg).FontSize(11).TextColor(t.Danger).MaxLines(2)
				if ui.Button(c, "重试").FontSize(11).Clicked() {
					a.loadRestricted(d, kind, false)
				}
			}
		}

		if auc := d.restricted.auction; auc != nil {
			a.restrictedAuctionView(c, auc)
		}
		if bids := d.restricted.bids; bids != nil {
			a.restrictedBidsView(c, bids)
		}
		if d.restricted.confirm && !unlocked {
			a.restrictedUnlockPanel(c, d, gate.Coins)
		}
	})
}

func (a *app) restrictedAuctionView(c *ui.Context, auc *auctionInfo) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(3).Padding(8, 10).Radius(8).
		Background(t.SurfaceHover).Children(func() {
		ui.Text(c, "竞拍状态").FontSize(metaFontSize).Bold()
		if auc.PriceKnown {
			ui.Textf(c, "当前价 %s 金币", coinThousands(auc.Price)).
				FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
		if auc.RemainKnown {
			ui.Textf(c, "剩余 %d 秒", auc.RemainSeconds).
				FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		} else if tm, ok := betTime(auc.EndAt); ok {
			ui.Textf(c, "截止 %s", tm.Format("01-02 15:04")).
				FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
		for _, row := range auc.Top {
			label := "领先"
			if row.Name != "" {
				label = row.Name
			}
			if row.CoinsKnown {
				label += " · " + coinThousands(row.Coins) + " 金币"
			}
			ui.Text(c, label).FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
	})
}

func (a *app) restrictedBidsView(c *ui.Context, bids *auctionBids) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(3).Padding(8, 10).Radius(8).
		Background(t.SurfaceHover).Children(func() {
		ui.Text(c, "出价记录").FontSize(metaFontSize).Bold()
		if !bids.Known {
			return
		}
		if len(bids.Rows) == 0 {
			ui.Text(c, "暂无出价记录").FontSize(metaSmallFontSize).TextColor(t.TextMuted)
			return
		}
		for _, row := range bids.Rows {
			name := row.Name
			if name == "" {
				name = "匿名出价"
			}
			line := name
			if row.CoinsKnown {
				line += " · " + coinThousands(row.Coins) + " 金币"
			}
			if tm, ok := betTime(row.At); ok {
				line += " · " + tm.Format("01-02 15:04")
			}
			ui.Text(c, line).FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
	})
}

// restrictedUnlockPanel 是扣币前的最后一步：把这一次的价格和「不可撤销」
// 写在确认按钮旁边，用户点确认才会发 POST /unlock。只显示进度，不在这里
// 写结论，结果由 submitRestrictedUnlock 用 toast 报告。
func (a *app) restrictedUnlockPanel(c *ui.Context, d *detailState, price int) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Padding(8, 10).Radius(8).
		Background(t.SurfaceHover).Children(func() {
		ui.Textf(c, "此次解锁将扣除 %d 金币，确认后不可撤销。", price).
			FontSize(metaSmallFontSize).TextColor(t.TextMuted).MaxLines(2)
		if a.coins != nil && a.coins.balanceOK {
			ui.Textf(c, "余额 %s", coinThousands(a.coins.balance)).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "取消").FontSize(11).Disabled(d.restricted.busy).Clicked() {
				d.restricted.confirm = false
			}
			ui.Spacer(c).Grow(1)
			if d.restricted.busy {
				ui.Spinner(c).Size(13, 13)
				ui.Text(c, "正在解锁…").FontSize(11).TextColor(t.TextMuted)
				return
			}
			if ui.PrimaryButton(c, fmt.Sprintf("确认花 %d 金币解锁", price)).Clicked() {
				a.submitRestrictedUnlock(d)
			}
		})
	})
}
