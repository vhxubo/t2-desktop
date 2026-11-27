package main

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// excerptMark 是纯文本摘要里可点或可画的一段。Name 非空表示 @提及，URL 非空
// 表示图片链接，Code 非空表示表情短码；Start/End 是 rune 下标 [Start, End)，
// 与 displayMentions 同口径。
type excerptMark struct {
	Start, End int
	Name, URL  string
	Code       string
}

// excerptMarks 扫出摘要里的 @提及、图片链接与表情短码，按起点排序并去掉重叠。
// 三者都按原文顺序拼回同一个 RichText：提及开资料卡，图片开预览，表情画成
// 行内图片（见 excerptView）。同一位置优先保留提及 / 链接，短码不抢它们的位子。
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
	for _, idx := range shortCode.FindAllStringIndex(text, -1) {
		start := utf8.RuneCountInString(text[:idx[0]])
		marks = append(marks, excerptMark{
			Start: start,
			End:   start + utf8.RuneCountInString(text[idx[0]:idx[1]]),
			Code:  text[idx[0]:idx[1]],
		})
	}
	sort.Slice(marks, func(i, j int) bool {
		if marks[i].Start != marks[j].Start {
			return marks[i].Start < marks[j].Start
		}
		return marks[i].Code == "" && marks[j].Code != ""
	})
	out := marks[:0]
	for _, mk := range marks {
		if len(out) > 0 && mk.Start < out[len(out)-1].End {
			continue
		}
		out = append(out, mk)
	}
	return out
}

// markdownImageSource 把正文里裸露的图片地址写成 Markdown 图片，让通知窗口
// 的 Markdown 视图把它们画成真正的配图（点开进预览）。已经是图片 / 链接目标
// 或行内代码里的地址保持原样，免得把 ![图](url) 套成 ![图](![](url))。
// 摘要里的可点片段与这里的转换共用 excerptMarks，口径一致。
func markdownImageSource(src string) string {
	marks := excerptMarks(src)
	if len(marks) == 0 {
		return src
	}
	rs := []rune(src)
	var b strings.Builder
	last := 0
	for _, mk := range marks {
		if mk.Name != "" || mk.URL == "" || mk.Start < last {
			continue
		}
		pre := string(rs[:mk.Start])
		if strings.HasSuffix(pre, "](") || strings.Count(pre, "`")%2 == 1 {
			continue
		}
		b.WriteString(string(rs[last:mk.Start]))
		b.WriteString("![](")
		b.WriteString(mk.URL)
		b.WriteString(")")
		last = mk.End
	}
	if last == 0 {
		return src
	}
	b.WriteString(string(rs[last:]))
	return b.String()
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
// 点开预览窗口，表情短码画成行内图片；没有这些片段时退回普通文本，排版与
// 原先完全一致。所有片段都拼进同一个 RichText，换行与省略号按整段计算。
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
	size := st.Size
	if size <= 0 {
		size = markdown.DefaultTheme(th).FontSize
	}
	rs := []rune(text)
	at, runes := 0, 0
	objects := map[int]ui.ImageSource{}
	addSpan := func(sp ui.Span) {
		ui.RichText(c, sp)
		runes += utf8.RuneCountInString(sp.Text)
	}
	root.Children(func() {
		for _, mk := range marks {
			if mk.Start < at || mk.Start >= len(rs) {
				continue
			}
			if mk.Start > at {
				addSpan(ui.Span{Text: string(rs[at:mk.Start])})
			}
			end := min(mk.End, len(rs))
			switch {
			case mk.Name != "":
				sp := ui.Span{Text: string(rs[mk.Start:end])}
				sp.Color = th.Accent
				sp.Background = th.Accent.Mix(th.Background, 0.88)
				sp.Radius = 4
				sp.PaddingX, sp.PaddingY = 3, 1
				tip := "查看 @" + mk.Name + " 的资料"
				chip := ui.RichText(c, sp).Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)
				name := mk.Name
				chip.OnClick(func() { a.showUserCard(name) })
				runes += end - mk.Start
			case mk.URL != "":
				sp := ui.Span{Text: string(rs[mk.Start:end])}
				sp.Color = linkColor
				sp.Underline = true
				tip := "查看图片"
				chip := ui.RichText(c, sp).Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)
				url := mk.URL
				chip.OnClick(func() { a.openImageGroup([]articlePicture{{URL: url}}, url) })
				runes += end - mk.Start
			case mk.Code != "":
				if e, ok := a.emojis[mk.Code]; ok {
					src := a.articleImage(e.ImgURL, imageCacheStable)
					w, h := ui.ImageSize(src)
					if src == nil {
						w, h = 1, 1 // 图还没到也按 1em 占位，到了原地替换
					}
					if w > 0 && h > 0 {
						ascent, descent := size*0.8, size*0.2
						if src != nil {
							objects[runes] = src
						}
						addSpan(ui.Span{
							Text:          "\uFFFC",
							Size:          size,
							ObjectWidth:   (ascent + descent) * w / h,
							ObjectHeight:  ascent,
							ObjectDescent: descent,
						})
					} else {
						addSpan(ui.Span{Text: string(rs[mk.Start:end])})
					}
				} else {
					addSpan(ui.Span{Text: string(rs[mk.Start:end])})
				}
			}
			at = end
		}
		if at < len(rs) {
			addSpan(ui.Span{Text: string(rs[at:])})
		}
	})
	if len(objects) > 0 {
		root.DrawObjects(func(p *ui.Painter, at int, rect ui.Rect) {
			if src, ok := objects[at]; ok {
				p.Image(src, rect, ui.Contain)
			}
		})
	}
	return root
}
