package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 抽奖入口放在详情页右上角悬浮工具条里，与「编辑 / 追加附言 / 关闭评论」
// 一样只有帖主看得到：抽奖只允许帖主发起，不为别人放一个点了必然失败的按钮。
// 开过奖之后入口变成「抽奖结果」，点一下直接把名单用 toast 报出来；在途时
// 换成转圈并禁用，避免连点发出多条触发评论。
//
// 中奖人数用一排 1～10 的芯片选，选中项高亮；确认框只负责收集人数，真正的
// 请求和结果都由 lottery.go 的 startLottery 走网络与 toast。

// lotteryButton 是悬浮工具条里那枚抽奖图标。busy 时画转圈，已开奖时提示可
// 查看结果；无障碍名与 tooltip 都要有，点击测试按无障碍名命中。
func (a *app) lotteryButton(c *ui.Context, d *detailState) {
	if !a.canLottery(d) {
		return
	}
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	busy := d.lottery.busy
	opened := d.topic != nil && d.topic.Lottery != nil
	icon, label, tip := lotteryIcon, "发起抽奖", "从这篇帖子的一级评论中随机抽取中奖者"
	if opened {
		label, tip = "抽奖结果", "查看这篇帖子的开奖结果"
	}
	if busy {
		label, tip = "正在开奖", "正在等待开奖结果…"
	}
	b := ui.Box(c).Size(30, 30).Radius(tk.BorderRadiusSM).Center().Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered && !busy {
		b.Background(tk.FillSecondary)
	}
	if busy {
		b.Disabled(true)
	}
	b.Children(func() {
		if busy {
			lobeSpin(c, feedback.SpinProps{CustomSize: 14})
			return
		}
		ink := t.TextMuted
		if hovered {
			ink = t.Text
		}
		ui.Icon(c, icon).Size(17, 17).TextColor(ink)
	})
	b.Label(label).Tooltip(tip)
	if !busy {
		b.OnClick(func() { a.askLottery(d) })
	}
}

// lotteryDialog 是「发起抽奖」的确认框。真要发评论，所以先让人确认人数；
// 框里只放人数芯片与一句说明，结果与进度都不写在页面上（见 lottery.go）。
func (a *app) lotteryDialog(c *ui.Context, d *detailState) {
	if d == nil || d.topic == nil || !d.lottery.awaiting || !a.canLottery(d) {
		return
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "发起抽奖",
		Width:      420,
		OkText:     "开始抽奖",
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			display.Text(c, "以帖主身份发一条一级评论触发开奖，从一级评论作者里随机抽取。同一篇帖子只能抽一次。", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 3,
			})
			display.Text(c, "中奖人数（1～10）", display.TextProps{
				Type: display.TextSecondary, FontSize: 11,
			})
			ui.Row(c).FillWidth().Gap(4).Wrap().KeepFocus().Children(func() {
				for n := lotteryMinWinners; n <= lotteryMaxWinners; n++ {
					n := n
					props := action.ButtonProps{Size: lobetheme.Small}
					if d.lottery.count == n {
						props.Variant = action.Primary
					}
					if action.Button(c, strconv.Itoa(n), props).Clicked() {
						d.lottery.count = n
					}
				}
			})
		})
	})
	if !open {
		d.lottery.awaiting = false
		return
	}
	if ok {
		d.lottery.awaiting = false
		a.startLottery(d)
	}
}
