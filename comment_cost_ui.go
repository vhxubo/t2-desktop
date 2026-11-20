package main

import (
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 评论框上方的状态栏：最左边是「走个面」的活跃度开关（持有才出现），然后
// 是字数、当前佩戴与预计发评金币、一句结论，最右边是最多两枚可省金币的
// 徽章按钮。规则与数据在 comment_cost.go。
//
// 整条只有一行、高度固定：推荐随字数变化时不会把输入框上下推动。文案放不下
// 时省略，完整内容放在悬停提示里。结果（切换成功 / 失败、潘多拉锁定）用窗口
// 内的 toast 报告，不在这一行里留结论性红字。

const (
	// commentCostBarHeight 是状态栏的固定高度：与输入框一行的中线对齐。
	commentCostBarHeight float32 = 24
)

// commentCostBar 画评论框上方的状态栏。未登录时不画（没有账号数据可算），
// 数据还没到的时候只报字数与进度，不编一个「未佩戴」出来。
func (a *app) commentCostBar(c *ui.Context, draft string) {
	if !a.commentCostBarVisible() {
		return
	}
	// 打开评论框就把徽章与佩戴状态读回来：有缓存与失败退避，每帧调用只发一次
	// 请求。Headless 测试里 autoLoad 为 false，这里什么都不做。
	a.ensureCommentCostData()
	t := c.Theme()
	st := a.commentCost
	length := commentLength(draft)

	row := ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).
		Height(commentCostBarHeight).KeepFocus()
	row.Children(func() {
		model, ready := a.commentCostModelNow(draft)
		if !ready {
			a.commentCostLoadingBar(c, st, length)
			return
		}
		a.commentActivityButton(c, model, st)
		countColor := t.TextMuted
		if model.Warn {
			countColor = t.Danger
		}
		ui.Text(c, model.Count).FontSize(11).TextColor(countColor).SingleLine().Shrink(0)
		// 状态与结论是「长文案」：宽度不够时先各自省略（MinWidth(0) 才允许
		// 收缩），完整内容留在悬停提示里，不会把右边的按钮挤出评论框。
		tip := strings.TrimSpace(model.Status)
		if model.Hint != "" {
			if tip != "" {
				tip += " · "
			}
			tip += model.Hint
		}
		if model.Status != "" {
			ui.Text(c, model.Status).FontSize(11).TextColor(t.TextMuted).
				SingleLine().Shrink(1).MinWidth(0).Tooltip(tip)
		}
		// 右对齐的留白：宽度充足时状态贴左、按钮贴右；不够时先压缩状态与
		// 结论，不挤压按钮。
		ui.Spacer(c).Grow(1)
		if model.Hint != "" {
			ui.Text(c, model.Hint).FontSize(11).
				TextColor(commentToneColor(t, model.Tone)).
				SingleLine().Shrink(3).MinWidth(0).MaxWidthPercent(30).
				Tooltip(model.Hint)
		}
		for _, act := range model.Actions {
			a.commentCostActionButton(c, st, act)
		}
		if st != nil && st.busyID != "" {
			lobeSpin(c, feedback.SpinProps{CustomSize: 12})
		}
	})
}

// commentCostLoadingBar 是数据还没到（或读取失败）时的那一行：字数照常显示，
// 后面只说明进度；失败时给一个重试入口，不要留一条永远空着的状态栏。
func (a *app) commentCostLoadingBar(c *ui.Context, st *commentCostState, length int) {
	t := c.Theme()
	ui.Text(c, fmt.Sprintf("%d 字", length)).FontSize(11).
		TextColor(t.TextMuted).SingleLine().Shrink(0)
	if st == nil {
		return
	}
	if st.err != "" {
		ui.Text(c, "徽章信息读取失败").FontSize(11).TextColor(t.TextMuted).
			SingleLine().Grow(1).MinWidth(0).Tooltip(st.err)
		if ui.Button(c, "重试").FontSize(11).Padding(2, 8).Radius(6).
			Tooltip("重新读取我的徽章与佩戴状态").KeepFocus().Clicked() {
			a.reloadCommentCostData()
		}
		return
	}
	ui.Text(c, "正在读取徽章信息…").FontSize(11).TextColor(t.TextMuted).
		SingleLine().Grow(1).MinWidth(0)
	lobeSpin(c, feedback.SpinProps{CustomSize: 12})
}

// commentActivityButton 是「走个面」的佩戴 / 摘下开关：它和评论费用无关，
// 所以独立于费用模型，只在持有这枚徽章时出现。潘多拉锁徽章期间禁用。
func (a *app) commentActivityButton(c *ui.Context, model commentCostModel, st *commentCostState) {
	act := model.Activity
	if act == nil {
		return
	}
	t := c.Theme()
	btn := ui.Button(c, act.Label).FontSize(11).Padding(2, 8).
		Radius(6).KeepFocus()
	btn.Background(t.Surface).Border(1, t.Border).TextColor(t.TextMuted)
	if act.Worn {
		btn.Border(1, t.Accent).TextColor(t.Accent)
	}
	disabled := act.Locked || (st != nil && st.busyID != "")
	btn.Disabled(disabled)
	btn.Tooltip(act.Tip)
	if btn.Clicked() && !disabled {
		a.setCommentCostBadge(act.Badge, !act.Worn, "")
	}
}

// commentCostActionButton 是「换成这枚更省金币的徽章」的按钮。切换会替换当前
// 佩戴的徽章，也可能扣金币，这些说明都放在悬停提示里。
func (a *app) commentCostActionButton(c *ui.Context, st *commentCostState, act commentCostAction) {
	t := c.Theme()
	btn := ui.Button(c, act.Label).FontSize(11).Padding(2, 8).
		Radius(6).KeepFocus()
	btn.Background(t.Surface).Border(1, t.Border).TextColor(t.Accent)
	busy := st != nil && st.busyID != ""
	btn.Disabled(busy)
	btn.Tooltip(act.Tip)
	if btn.Clicked() && !busy {
		a.setCommentCostBadge(act.Badge, true, "")
	}
}

// commentToneColor 把状态栏文字的语义色映射到主题颜色。
func commentToneColor(t *ui.Theme, tone commentCostTone) ui.Color {
	switch tone {
	case commentToneGood:
		return t.Success
	case commentToneWarn:
		return t.Warning
	case commentToneBad:
		return t.Danger
	}
	return t.TextMuted
}
