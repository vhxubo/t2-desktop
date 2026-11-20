package main

import (
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
)

// 表情短码候选面板：一个角贴着正文里那个 ":"，与 @ 候选面板同一套定位与
// 交互（见 mentions_ui.go）。内容是一列 Lobe List 行（图标 + :code: + 名称），
// 点行或回车把 ":" 前缀整段换成短码。没有工具栏入口，键入 ":" 才出现。

const (
	emojiShortcodePanelW    float32 = 280
	emojiShortcodePanelMinW float32 = 176
	// emojiShortcodeMaxH 是内容区高度上限； ":" 上方 / 下方的空间更小时按空间来。
	emojiShortcodeMaxH float32 = 232
	// emojiShortcodeMargin 是面板与窗口边缘至少留的间距，emojiShortcodeGap
	// 是面板与 ":" 那一行之间的缝。
	emojiShortcodeMargin float32 = 8
	emojiShortcodeGap    float32 = 4
	// emojiShortcodeRowH 是 List 一行的估计高度，用来估面板高度、决定朝哪边展开。
	emojiShortcodeRowH float32 = 34
)

// emojiShortcodeAtRect 用与输入框一致的排版算出 ":" 本身的矩形。锚的是 ":"
// 字符（tokenStart），不是会继续移动的输入光标：用户 typing 时面板稳稳钉在
// 那个 ":" 上，不会每敲一个字就往右滑一格。正文或宽度没变时复用排版。
func emojiShortcodeAtRect(c *ui.Context, d *detailState, padX, padY float32) ui.Rect {
	field := d.composerField.Bounds(c)
	width := max(40, field.W-padX*2)
	st := &d.emojiShortcodes
	if st.shape == nil || st.shapeText != d.draft || st.shapeWidth != width {
		st.shape = ui.ShapeText(d.draft, ui.Font{Size: commentFontSize}, width)
		st.shapeText, st.shapeWidth = d.draft, width
	}
	rs := []rune(d.draft)
	at := st.tokenStart
	if at < 0 || at > len(rs) {
		at = 0
	}
	units := 0
	for _, ch := range rs[:at] {
		if utf8.RuneLen(ch) > 2 {
			units += 2
		} else {
			units++
		}
	}
	r := st.shape.Caret(units)
	return ui.Rect{X: field.X + padX + r.X, Y: field.Y + padY + r.Y, W: r.W, H: r.H}
}

// emojiShortcodePopover 让气泡的一个角紧贴 ":"：下方放得下就向下展开，放不下
// 就把面板下沿贴到 ":" 上方；两边都不够时选大的那一边，内容自己滚。
// 气泡只在点开它的那扇窗口里画（页面评论框与独立写评论窗口共用 detailState）。
func (a *app) emojiShortcodePopover(c *ui.Context, d *detailState, host replySurface, padX, padY float32) {
	st := &d.emojiShortcodes
	if !st.open || st.host != host {
		return
	}
	cands := a.emojiShortcodeCandidates(d)
	if len(cands) == 0 {
		// 一个候选都没有时不摆空面板，继续打字或清空搜索词再看。
		return
	}
	at := emojiShortcodeAtRect(c, d, padX, padY)
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
	placement, anchorY := lobeoverlay.Bottom, at.Y+at.H
	if up {
		placement, anchorY = lobeoverlay.Top, at.Y
	}

	ui.Overlay(c, func() {
		// 零尺寸的定位块贴在 ":" 上：Lobe 气泡按它摆面板，自己管点窗外 / Esc 关闭。
		// 气泡水平居中于定位块，所以把定位块放在面板中心的水平位置。
		anchor := ui.Box(c).Absolute().Left(left+width/2).Top(anchorY).Size(0, 0)
		lobeoverlay.Popover(c, anchor, &st.open, lobeoverlay.PopoverProps{Placement: placement}, func() {
			// KeepFocus：在面板里点行不把焦点从输入框抢走，选完光标还在原处。
			ui.Column(c).Width(width).KeepFocus().Label("表情候选面板").Children(func() {
				ui.Scroll(c).MaxHeight(maxH).Children(func() {
					if key := display.List(c, display.ListProps{
						Items:   a.emojiShortcodeRows(c, cands),
						Active:  a.emojiShortcodeActiveKey(d),
						Compact: true,
					}); key != "" {
						for _, em := range cands {
							if "emoji:"+emojiCode(em) != key {
								continue
							}
							st.host = host
							a.insertEmojiShortcode(d, emojiShortcode(em))
							break
						}
					}
				})
			})
		})
	})
}

// emojiShortcodeRows 把候选翻成 Lobe List 的行：左边是表情图（还没下载好时
// 留一个等大的空位），中间是短码，右边是名称。短码本身就是要插入的正文。
func (a *app) emojiShortcodeRows(c *ui.Context, cands []emojiInfo) []display.ListItem {
	tk := c.Theme()
	out := make([]display.ListItem, 0, len(cands))
	for _, em := range cands {
		em := em
		out = append(out, display.ListItem{
			Key:   "emoji:" + emojiCode(em),
			Label: emojiShortcode(em),
			Extra: em.Name,
			Avatar: func() {
				if src := a.articleImage(em.ImgURL, imageCacheStable); src != nil {
					ui.Image(c, src).Size(20, 20).Fit(ui.Contain)
				} else {
					ui.Box(c).Size(20, 20).Radius(4).Background(tk.SurfaceHover)
				}
			},
		})
	}
	return out
}

// insertEmojiShortcode 把选中的短码写进正文。键入 ":" 打开的面板不是「插入」
// 而是「整块替换」：从 ":" 起、到这段末尾（含已补上的结尾冒号）全换成
// ":sad:"。光标停在段中间挑候选也换整块，只换光标前那一段会留下 ":sa:sad:"。
func (a *app) insertEmojiShortcode(d *detailState, text string) {
	if text == "" {
		return
	}
	st := &d.emojiShortcodes
	if st.typing && st.tokenStart >= 0 {
		start, end := emojiShortcodeWordRange(d.draft, st.tokenStart)
		d.mdEditor.replaceMentionToken(&d.draft, start, max(end, st.tokenEnd), text)
	} else {
		d.mdEditor.insertText(&d.draft, text)
	}
	d.preview = 0
	st.reset()
}

// emojiShortcodeDeleteBlock 让写完的 ":sad:" 像一个整体：光标落在短码里任何
// 位置按退格 / Delete，一次清掉整块，而不是一个字符一个字符往回删。k.Text()
// 是按键到来时输入控件里最新的文本（可能比绑定值还新），光标取上一帧提交到
// 状态的选区；命中整块后用 k.SetText 同步回输入控件。
func (a *app) emojiShortcodeDeleteBlock(d *detailState, k ui.TextKey, backspace bool) bool {
	if d.mdEditor.selStart != d.mdEditor.selEnd {
		return false // 有选区时交回输入控件正常处理
	}
	text := k.Text()
	rs := []rune(text)
	caret := d.mdEditor.selEnd
	if caret < 0 || caret > len(rs) {
		caret = len(rs)
	}
	blocks := emojiShortcodeBlocks(text)
	var hit *emojiBlock
	for i := range blocks {
		b := blocks[i]
		if backspace {
			// 光标在块内任意位置（含结尾冒号之后）：整块删掉。块首按退格
			// 是删前一个字，不算命中。
			if caret > b.Start && caret <= b.End {
				hit = &blocks[i]
				break
			}
		} else if caret >= b.Start && caret < b.End {
			hit = &blocks[i]
			break
		}
	}
	if hit == nil {
		return false
	}
	out := string(rs[:hit.Start]) + string(rs[hit.End:])
	at := hit.Start
	d.draft = out
	d.mdEditor.pendingSel = composeSelection{ok: true, start: at, end: at}
	d.mdEditor.selStart, d.mdEditor.selEnd = at, at
	k.SetText(out)
	return true
}

// emojiShortcodeTextKey 让键入 ":" 弹出的候选可以纯键盘操作：上下键换表情，
// 回车 / Tab 采纳，Escape 收起。整块删除与面板是否打开无关。与 mentionTextKey
// 串在 OnTextKey 里，各自只认自己的块与面板（见 replyTextArea）。
func (a *app) emojiShortcodeTextKey(d *detailState, k ui.TextKey) bool {
	if k.Key == ui.KeyBackspace || k.Key == ui.KeyDelete {
		return a.emojiShortcodeDeleteBlock(d, k, k.Key == ui.KeyBackspace)
	}
	st := &d.emojiShortcodes
	if !st.typing || !st.open {
		return false
	}
	cands := a.emojiShortcodeCandidates(d)
	switch k.Key {
	case ui.KeyUp, ui.KeyDown:
		if len(cands) == 0 {
			return false
		}
		step := 1
		if k.Key == ui.KeyUp {
			step = -1
		}
		st.active = (st.active + step + len(cands)) % len(cands)
		return true
	case ui.KeyEnter, ui.KeyTab:
		if st.active < 0 || st.active >= len(cands) {
			return false
		}
		a.insertEmojiShortcode(d, emojiShortcode(cands[st.active]))
		k.SetText(d.draft)
		return true
	case ui.KeyEscape:
		st.reset()
		return true
	}
	return false
}
