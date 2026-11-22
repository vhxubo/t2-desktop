package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// lotteryDialog 是「发起抽奖」的确认框：帖主在详情操作组里点入口后展开，
// 选好中奖人数再确认。确认后的网络流程全在 lottery.go 的 startLottery，
// 结果用详情窗口的 toast 报告，框里不写结论。
func (a *app) lotteryDialog(c *ui.Context, d *detailState) {
	if d == nil || d.topic == nil || !d.lottery.awaiting {
		return
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "发起抽奖？",
		Width:      400,
		OkText:     "确认发起抽奖",
		CancelText: "取消",
	}, func() {
		t := c.Theme()
		ui.Text(c, "抽奖助手会以你的身份发一条评论，开奖结果稍后出现在这篇帖子下。每篇帖子只能开一次奖。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)
		ui.Text(c, "中奖人数").FontSize(12).TextColor(t.TextMuted)
		options := make([]string, 0, lotteryMaxWinners-lotteryMinWinners+1)
		for n := lotteryMinWinners; n <= lotteryMaxWinners; n++ {
			options = append(options, strconv.Itoa(n))
		}
		selected := d.lottery.count - lotteryMinWinners
		if selected < 0 || selected >= len(options) {
			selected = lotteryDefaultWinners - lotteryMinWinners
		}
		action.Segmented(c, &selected, options, action.SegmentedProps{
			Size: lobetheme.Small, Block: true,
		})
		if n := selected + lotteryMinWinners; n != d.lottery.count {
			d.lottery.count = n
		}
	})
	// 点窗外、✕、Esc 与「取消」都与取消同义。
	if !open {
		d.lottery.awaiting = false
		return
	}
	if ok {
		a.startLottery(d)
	}
}
