package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 评论输入框里的 @ 候选面板。面板固定在窗口右下角，不跟随光标、也没有
// 工具栏入口；键入 @ 即打开，名字后自动补空格，拼串前排掉自己，最多 50 个 @。
// 名单为空 / 无权 / 超过 50 人时隐藏对应入口，而不是点一下没反应。
//
// 界面用 Lobe 组件：搜索框、按钮、标签、转圈与空 / 错误状态，配色取 Lobe
// token。文案保持中文，点击测试按无障碍名（Label）命中。
//
// 页面评论框和独立写评论窗口共用 detailState，所以面板状态带 host：只有
// 点开它的那扇窗口画内容。

const (
	// mentionPanelShown 限制一次画出的参与人行数：名单全量不分页，几百人
	// 一次铺满面板既慢又没法挑，搜索框可以把范围缩到要找的人。
	mentionPanelShown = 50
	// mentionPanelWidth 是候选面板宽度，尽量窄，只放得下名字与两个拼串按钮。
	mentionPanelWidth = 232
	// mentionPanelMaxH 是判断下方空间够不够放面板的估计高度。
	mentionPanelMaxH = 240
)

// mentionAtRect 用与输入框一致的排版算出 @ 符号本身的矩形。锚的是 @ 字符
// （tokenStart），不是会继续移动的输入光标：用户 typing 时面板稳稳钉在那个
// @ 上。正文或宽度没变时复用排版，长草稿不每帧重算。
func mentionAtRect(c *ui.Context, d *detailState, padX, padY float32) ui.Rect {
	field := d.composerField.Bounds(c)
	width := max(40, field.W-padX*2)
	st := &d.mentions
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

// mentionPopover 让面板的一个角紧贴 @ 符号：下方放得下就从 @ 的左下角向下
// 展开，放不下就把面板下沿贴到 @ 上方。没有工具栏入口，也不在窗口里乱定位。
func (a *app) mentionPopover(c *ui.Context, d *detailState, host replySurface, padX, padY float32) {
	st := &d.mentions
	if !st.open || st.host != host {
		return
	}
	at := mentionAtRect(c, d, padX, padY)
	_, winH := c.Size()
	ui.Overlay(c, func() {
		dismiss := ui.Box(c).Absolute().Left(0).Top(0).Fill()
		dismiss.Label("收起提及候选").OnClick(func() { st.open = false })

		tk := lobetheme.TokensOf(c)
		panel := ui.Column(c).Width(mentionPanelWidth).Padding(10).Radius(tk.BorderRadius).
			Background(tk.BgElevated).TextColor(tk.Text).FontSize(tk.FontSize).
			Label("提及候选面板")
		if winH-at.Y < mentionPanelMaxH {
			// 下方不够：面板下沿贴住 @ 的上方。
			panel.Absolute().Left(at.X).Bottom(winH - at.Y)
		} else {
			// 下方够：面板左上角贴住 @ 的左下角。
			panel.Absolute().Left(at.X).Top(at.Y + at.H)
		}
		tk.Elevate(panel, lobetheme.BoxShadowSecondary)
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			st.open = false
		}
		panel.Children(func() { a.mentionPanel(c, d, host) })
	})
}

// mentionPanel 只放可操作的项：两个拼串按钮、搜索框、参与人列表；加载用
// 转圈，失败给重试按钮。没有任何说明性文案。
func (a *app) mentionPanel(c *ui.Context, d *detailState, host replySurface) {
	st := &d.mentions
	data := a.mentionData(d, d.draft)
	exclude := a.mentionExclude(d)

	ui.Column(c).Width(mentionPanelWidth).Gap(4).Children(func() {
		random := ""
		if data.Slots > 0 {
			random = pickRandomMention(data.Guardians, exclude)
		}
		all := buildMentions(data.Participants, exclude, data.Slots)
		if random != "" || (all != "" && data.Total <= mentionNotifyLimit) {
			ui.Row(c).FillWidth().Gap(4).Wrap().KeepFocus().Children(func() {
				if all != "" && data.Total <= mentionNotifyLimit {
					if action.Button(c, "@所有人", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.insertMention(d, all)
					}
				}
				if random != "" {
					if action.Button(c, "@随机守护者", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.insertMention(d, mentionAt(random))
					}
				}
			})
		}
		if data.PeopleBusy && !st.participantsLoaded {
			feedback.Spin(c, feedback.SpinProps{Size: lobetheme.Small}, nil)
		}
		if data.PeopleErr != "" {
			display.Text(c, data.PeopleErr, display.TextProps{
				Type: display.TextWarning, FontSize: 11, Rows: 2,
			})
			if !data.PeopleBusy && !data.PeopleDenied {
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.reloadMentionParticipants(d)
				}
			}
		}
		if st.guardiansErr != "" && !st.guardiansLoaded && !data.GuardiansBusy {
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadMentionGuardians(d)
			}
		}

		if len(data.Participants) == 0 {
			return
		}
		filtered := mentionFiltered(data.Participants, st.query)
		if len(filtered) == 0 {
			return
		}
		shown := filtered
		if len(shown) > mentionPanelShown {
			shown = shown[:mentionPanelShown]
		}
		ui.Scroll(c).Height(180).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				for _, p := range shown {
					a.mentionPersonRow(c, d, host, p)
				}
			})
		})
	})
}

// mentionFiltered 按用户名筛选名单，忽略大小写；空词返回全部。
func mentionFiltered(people []mentionPerson, query string) []mentionPerson {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@")))
	if q == "" {
		return people
	}
	out := make([]mentionPerson, 0, len(people))
	for _, p := range people {
		if strings.Contains(strings.ToLower(p.Username), q) {
			out = append(out, p)
		}
	}
	return out
}

// mentionPersonRow 是名单里的一行：头像、用户名、分身标记，点一下把
// "@名字 " 插进正文。无障碍名带 @，和拼串入口区分得开。
func (a *app) mentionPersonRow(c *ui.Context, d *detailState, host replySurface, p mentionPerson) {
	t := c.Theme()
	a.prefetchAvatars(p.Avatar)
	row := ui.ButtonBase(c).FillWidth().Padding(4, 8).Radius(8).Gap(8).
		AlignItems(ui.Center).KeepFocus()
	row.Background(ui.Transparent)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		avatarView(c, p.Username, a.avatarBitmap(p.Avatar), 22)
		ui.Text(c, "@"+p.Username).FontSize(12).TextColor(t.Text).
			SingleLine().Grow(1).MinWidth(0)
		if p.IsAlias {
			display.Tag(c, "分身", display.TagProps{Size: lobetheme.Small})
		}
	})
	row.Label("@" + p.Username)
	if row.Clicked() {
		st := &d.mentions
		st.host = host
		a.insertMention(d, mentionAt(p.Username))
	}
}

// insertMention 把拼好的提及插进光标处。insertText 会记下插入后的选区，
// replyTextArea 下一帧 syncSelection 把它还给输入框；这里顺手收起气泡并
// 切回编辑视图，免得在预览里插入了却看不见。
func (a *app) insertMention(d *detailState, text string) {
	if text == "" {
		return
	}
	st := &d.mentions
	if st.typing && st.tokenStart >= 0 && st.tokenEnd >= st.tokenStart {
		// 键入 @ 触发的面板：替换掉正文里正在输入的 @前缀，而不是在后面
		// 再追加一个 @名字（那会留下 "@jim @jimmy " 这样的重复串）。
		d.mdEditor.replaceMentionToken(&d.draft, st.tokenStart, st.tokenEnd, text)
	} else {
		d.mdEditor.insertText(&d.draft, text)
	}
	d.preview = 0
	st.typing = false
	st.open = false
	st.query = ""
	st.tokenStart, st.tokenEnd = -1, -1
	st.active = 0
}

// mentionDeleteBlock 让写完的 @名字 像一个整体：退格 / Delete 一次清掉整块。
// k.Text() 是按键到来时输入控件里最新的文本（可能比绑定值还新），光标取
// 上一帧提交到状态的选区；命中整块后用 k.SetText 同步回输入控件。
func (a *app) mentionDeleteBlock(d *detailState, k ui.TextKey, backspace bool) bool {
	if d.mdEditor.selStart != d.mdEditor.selEnd {
		return false // 有选区时交回输入控件正常处理
	}
	text := k.Text()
	rs := []rune(text)
	caret := d.mdEditor.selEnd
	if caret < 0 || caret > len(rs) {
		caret = len(rs)
	}
	blocks := mentionBlocks(text)
	var hit *mentionBlock
	for i := range blocks {
		if backspace {
			if blocks[i].End == caret {
				hit = &blocks[i]
			}
		} else if blocks[i].Start == caret {
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

// mentionTextKey 让键入 @ 弹出的候选可以纯键盘操作：上下键换人，回车 / Tab
// 采纳，Escape 收起。面板没由输入打开时不消费按键，Ctrl+Enter 发送照旧。
// OnTextKey 在帧间运行，Element 已失效，所以改正文必须走 k.SetText。
func (a *app) mentionTextKey(d *detailState, k ui.TextKey) bool {
	// 提及块整体删除：与面板是否打开无关。光标紧跟在 @名字 的尾随空格后
	// 按退格（或停在块首按 Delete），一次删掉整块，而不是一个字一个字退。
	if k.Key == ui.KeyBackspace || k.Key == ui.KeyDelete {
		return a.mentionDeleteBlock(d, k, k.Key == ui.KeyBackspace)
	}
	st := &d.mentions
	if !st.typing || !st.open {
		return false
	}
	switch k.Key {
	case ui.KeyUp, ui.KeyDown:
		cands := a.mentionCandidates(d)
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
		cands := a.mentionCandidates(d)
		if st.active < 0 || st.active >= len(cands) {
			return false
		}
		a.insertMention(d, mentionAt(cands[st.active]))
		k.SetText(d.draft)
		return true
	case ui.KeyEscape:
		st.typing = false
		st.open = false
		st.query = ""
		st.tokenStart, st.tokenEnd = -1, -1
		return true
	}
	return false
}
