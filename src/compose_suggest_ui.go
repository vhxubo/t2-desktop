package main

import (
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 发帖窗口里贴着 @ / : 展开的候选气泡。定位、交互与评论里的那两块一致
// （见 mentions_ui.go、emoji_shortcode_ui.go）：锚的是符号本身而不是会移动的
// 光标；下方放得下就向下展开，放不下就翻到上面；宽度由最长的一行撑开。

// wireComposeSuggest 把一个 Lobe 多行输入框接上 @ / : 两套候选：写完的提及
// 在正文里上色加粗、每帧按光标复核正在输入的 token、OnChange 开合面板、
// OnTextKey 处理上下键 / 回车 / Escape 与整块删除，最后把气泡画在输入框旁。
// 帖子正文与动态正文都走这一条，只有传进来的 e 不同。
func (a *app) wireComposeSuggest(c *ui.Context, area ui.Element, e composeSuggestEditor, st *composeState) {
	t := c.Theme()
	var ranges []ui.TextRange
	for _, b := range e.s.mentions.blocksFor(*e.body) {
		ranges = append(ranges, ui.TextRange{
			Start: b.Start, End: b.End, Color: t.Accent, Weight: 600,
		})
	}
	if len(ranges) > 0 {
		area.TextRanges(ranges...)
	}
	// 选区先读进状态（元素出构建帧就失效），面板才能按光标复核 token。
	e.ed.syncSelection(area)
	if e.field.Focused(c) {
		caret := e.ed.selEnd
		a.syncComposeMentionToken(e, caret, st)
		a.syncComposeEmojiToken(e, caret)
	}
	area.OnChange(func() {
		_, caret := area.TextSelection()
		a.onComposeMentionInput(e, caret, st)
		a.onComposeEmojiInput(e, caret)
	})
	area.OnTextKey(func(k ui.TextKey) bool {
		return a.composeEmojiTextKey(e, k) || a.composeMentionTextKey(e, k)
	})
	// 气泡画在输入框之后：锚点用输入框的 committed 位置，与构建顺序无关。
	a.composeMentionPopover(c, e, st, composeSuggestPadX, composeSuggestPadY)
	a.composeEmojiShortcodePopover(c, e)
}

// composeSuggestAtRect 用与输入框一致的排版算出 @ / : 符号本身的矩形。
// shape* 是各自状态的排版缓存，正文或宽度没变时复用，长草稿不每帧整形。
func composeSuggestAtRect(c *ui.Context, e composeSuggestEditor, tokenStart int, padX, padY float32,
	shape **ui.TextLayout, shapeText *string, shapeWidth *float32) ui.Rect {
	field := e.field.Bounds(c)
	width := max(40, field.W-padX*2)
	if *shape == nil || *shapeText != *e.body || *shapeWidth != width {
		*shape = ui.ShapeText(*e.body, ui.Font{Size: e.font}, width)
		*shapeText, *shapeWidth = *e.body, width
	}
	rs := []rune(*e.body)
	at := max(0, min(tokenStart, len(rs)))
	units := 0
	for _, ch := range rs[:at] {
		if utf8.RuneLen(ch) > 2 {
			units += 2
		} else {
			units++
		}
	}
	r := (*shape).Caret(units)
	return ui.Rect{X: field.X + padX + r.X, Y: field.Y + padY + r.Y, W: r.W, H: r.H}
}

// composeMentionAtRect 算 @ 的位置。
func composeMentionAtRect(c *ui.Context, e composeSuggestEditor, padX, padY float32) ui.Rect {
	s := &e.s.mentions
	return composeSuggestAtRect(c, e, s.tokenStart, padX, padY,
		&s.shape, &s.shapeText, &s.shapeWidth)
}

// composeEmojiAtRect 算 : 的位置。
func composeEmojiAtRect(c *ui.Context, e composeSuggestEditor, padX, padY float32) ui.Rect {
	s := &e.s.emojiShortcodes
	return composeSuggestAtRect(c, e, s.tokenStart, padX, padY,
		&s.shape, &s.shapeText, &s.shapeWidth)
}

// composeMentionPopover 在 @ 旁画候选气泡。面板只在候选或状态行非空时出现，
// 不摆一块空面板；Lobe 自己管点窗外 / Esc 关闭。
func (a *app) composeMentionPopover(c *ui.Context, e composeSuggestEditor, st *composeState, padX, padY float32) {
	s := &e.s.mentions
	if !s.open {
		if s.search.pending != "" || s.search.busy || s.search.query != "" {
			s.resetSearch()
		}
		return
	}
	data := a.composeMentionData(e, *e.body)
	rows := a.composeMentionRows(e.s, *e.body, s.query)
	status := mentionPanelStatus(data, s)
	if len(rows) == 0 && !status {
		return
	}
	at := composeMentionAtRect(c, e, padX, padY)
	winW, winH := c.Size()
	width := s.panelWidth(rows)
	chrome := float32(0)
	if status {
		chrome = mentionStatusH
	}
	above := at.Y - mentionPanelMargin - mentionPanelGap
	below := winH - at.Y - at.H - mentionPanelMargin - mentionPanelGap
	want := mentionRowsHeight(rows) + chrome
	up := below < want && above > below
	space := below
	if up {
		space = above
	}
	maxH := min(mentionPanelMaxH, max(mentionRowH, space-chrome))

	left := at.X
	if left+width > winW-mentionPanelMargin {
		left = winW - mentionPanelMargin - width
	}
	left = max(left, mentionPanelMargin)
	anchorY := at.Y + at.H
	if up {
		anchorY = at.Y
	}

	ui.Overlay(c, func() {
		anchor := ui.Box(c).Absolute().Left(left+width/2).Top(anchorY).Size(0, 0)
		suggestPopover(c, anchor, &s.open, !up, func() {
			ui.Column(c).Width(width).KeepFocus().Label("提及候选面板").Children(func() {
				a.composeMentionPanel(c, e, st, rows, data, maxH)
			})
		})
	})
}

// composeMentionPanel 只放可操作的项：加载 / 失败重试与候选行。行集合由
// composeMentionRows 给，键盘高亮的那一行就是回车会插进去的那一个。
func (a *app) composeMentionPanel(c *ui.Context, e composeSuggestEditor, st *composeState,
	rows []mentionRow, data mentionResult, maxH float32) {
	s := &e.s.mentions
	if data.GuardiansBusy && !s.guardiansLoaded {
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
	}
	if s.guardiansErr != "" && !s.guardiansLoaded && !data.GuardiansBusy {
		display.Text(c, "守护者名单加载失败："+s.guardiansErr, display.TextProps{
			Type: display.TextWarning, FontSize: 11, Rows: 2,
		})
		if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			a.reloadComposeGuardians(e, st)
		}
	}
	if data.SearchBusy && len(data.Search) == 0 {
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
	}
	if data.SearchErr != "" {
		display.Text(c, "搜索用户失败："+data.SearchErr, display.TextProps{
			Type: display.TextWarning, FontSize: 11, Rows: 2,
		})
		if !data.SearchBusy {
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.retryComposeMentionSearch(e, st)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	ui.Scroll(c).MaxHeight(maxH).Children(func() {
		if key := display.List(c, display.ListProps{
			Items:   a.mentionListItems(c, rows),
			Active:  a.composeMentionActiveKey(e),
			Compact: true,
		}); key != "" {
			for _, row := range rows {
				if row.Key != key {
					continue
				}
				insertComposeMention(e, row.Text)
				break
			}
		}
	})
}

// composeMentionActiveKey 是键盘上下键当前指着的那一行的 List key。
func (a *app) composeMentionActiveKey(e composeSuggestEditor) string {
	s := &e.s.mentions
	if !s.typing || !s.open {
		return ""
	}
	people := a.composeMentionPicks(e)
	if s.active < 0 || s.active >= len(people) {
		return ""
	}
	return mentionPersonKey(people[s.active])
}

// composeEmojiShortcodePopover 在 : 旁画候选气泡。没有候选时不摆空面板。
func (a *app) composeEmojiShortcodePopover(c *ui.Context, e composeSuggestEditor) {
	s := &e.s.emojiShortcodes
	if !s.open || !s.typing {
		return
	}
	cands := a.composeEmojiCandidates(e.s)
	if len(cands) == 0 {
		return
	}
	at := composeEmojiAtRect(c, e, composeSuggestPadX, composeSuggestPadY)
	winW, winH := c.Size()
	avail := winW - emojiShortcodeMargin*2
	width := min(emojiShortcodePanelW, avail)
	if width < emojiShortcodePanelMinW {
		width = min(emojiShortcodePanelMinW, avail)
	}
	width = max(width, 40)

	rowsH := float32(len(cands)) * emojiShortcodeRowH
	above := at.Y - emojiShortcodeMargin - emojiShortcodeGap
	below := winH - at.Y - at.H - emojiShortcodeMargin - emojiShortcodeGap
	up := below < rowsH && above > below
	space := below
	if up {
		space = above
	}
	maxH := min(emojiShortcodeMaxH, max(emojiShortcodeRowH, space))

	left := at.X
	if left+width > winW-emojiShortcodeMargin {
		left = winW - emojiShortcodeMargin - width
	}
	left = max(left, emojiShortcodeMargin)
	anchorY := at.Y + at.H
	if up {
		anchorY = at.Y
	}

	ui.Overlay(c, func() {
		anchor := ui.Box(c).Absolute().Left(left+width/2).Top(anchorY).Size(0, 0)
		suggestPopover(c, anchor, &s.open, !up, func() {
			ui.Column(c).Width(width).KeepFocus().Label("表情候选面板").Children(func() {
				ui.Scroll(c).MaxHeight(maxH).Children(func() {
					if code := a.emojiShortcodeRows(c, a.composeEmojiActiveKey(e.s), cands); code != "" {
						insertComposeEmoji(e, code)
					}
				})
			})
		})
	})
}
