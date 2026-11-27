package main

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// excerptMark 是纯文本摘要里可点的一段。Name 非空表示 @提及，URL 非空表示
// 图片链接；Start/End 是 rune 下标 [Start, End)，与 displayMentions 同口径。
type excerptMark struct {
	Start, End int
	Name, URL  string
}

// excerptMarks 扫出摘要里的 @提及与图片链接，按起点排序并去掉重叠。列表行与
// 通知卡片都把这几种文字接成可点的组件，点开的操作由调用方统一的 excerptView
// 处理（资料卡 / 图片预览）。
func excerptMarks(text string) []excerptMark {
	var marks []excerptMark
	for _, m := range displayMentions(text) {
		marks = append(marks, excerptMark{Start: m.Start, End: m.End, Name: m.Name})
	}
	for _, idx := range linkInText.FindAllStringIndex(text, -1) {
		raw := strings.TrimRight(text[idx[0]:idx[1]], ".,;:!?)]}，。；！？、》\"'")
		if raw == "" || !isImageLink(raw) {
			continue
		}
		start := utf8.RuneCountInString(text[:idx[0]])
		marks = append(marks, excerptMark{
			Start: start,
			End:   start + utf8.RuneCountInString(raw),
			URL:   raw,
		})
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].Start < marks[j].Start })
	out := marks[:0]
	for _, mk := range marks {
		if len(out) > 0 && mk.Start < out[len(out)-1].End {
			continue
		}
		out = append(out, mk)
	}
	return out
}

// excerptStyle 是摘要的排版参数：字号、行高、颜色与最多行数。Color 透明表示
// 沿用主题色，不单独设置。
type excerptStyle struct {
	Size       float32
	LineHeight float32
	Color      ui.Color
	MaxLines   int
}

// excerptView 把一行摘要画成可换行的富文本：@名字点开用户资料卡，图片链接
// 点开预览窗口；没有可点内容时退回普通文本，排版与原先完全一致。普通文字也
// 用 span 拼进同一个 RichText，换行与省略号按整段计算，不会被组件切断。
func (a *app) excerptView(c *ui.Context, text string, st excerptStyle, label string) ui.Element {
	marks := excerptMarks(text)
	if len(marks) == 0 {
		t := ui.Text(c, text).FontSize(st.Size).MaxLines(st.MaxLines).Label(label)
		if st.LineHeight > 0 {
			t = t.LineHeight(st.LineHeight)
		}
		if st.Color.A > 0 {
			t = t.TextColor(st.Color)
		}
		return t
	}
	root := ui.RichText(c).FontSize(st.Size).MaxLines(st.MaxLines).FillWidth().Label(label)
	if st.LineHeight > 0 {
		root = root.LineHeight(st.LineHeight)
	}
	if st.Color.A > 0 {
		root = root.TextColor(st.Color)
	}
	th := c.Theme()
	linkColor := markdown.DefaultTheme(th).Link
	rs := []rune(text)
	at := 0
	root.Children(func() {
		for _, mk := range marks {
			if mk.Start < at || mk.Start >= len(rs) {
				continue
			}
			if mk.Start > at {
				ui.RichText(c, ui.Span{Text: string(rs[at:mk.Start])})
			}
			end := min(mk.End, len(rs))
			sp := ui.Span{Text: string(rs[mk.Start:end])}
			var tip string
			if mk.Name != "" {
				sp.Color = th.Accent
				sp.Background = th.Accent.Mix(th.Background, 0.88)
				sp.Radius = 4
				sp.PaddingX, sp.PaddingY = 3, 1
				tip = "查看 @" + mk.Name + " 的资料"
			} else {
				sp.Color = linkColor
				sp.Underline = true
				tip = "查看图片"
			}
			chip := ui.RichText(c, sp).Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)
			if mk.Name != "" {
				name := mk.Name
				chip.OnClick(func() { a.showUserCard(name) })
			} else {
				url := mk.URL
				chip.OnClick(func() { a.openImageGroup([]articlePicture{{URL: url}}, url) })
			}
			at = end
		}
		if at < len(rs) {
			ui.RichText(c, ui.Span{Text: string(rs[at:])})
		}
	})
	return root
}
