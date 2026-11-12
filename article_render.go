package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 详情页正文里的图片与内置表情都走这里。
//
// Markdown 自带的视图只把「整段只有图片」的段落画成图片；一段文字中间
// 夹着的图片只会显示成 [说明]，而行内 :code: 表情同样只剩文字。服务器
// 的正文恰好两者都有：表情会被换成行内图片语法，配图也常和文字写在同一
// 段里。这里用 ui.RichText 的行内对象（Span.ObjectWidth + DrawObjects）
// 把表情画在文字中间，配图则在该段落内部独占一行显示。
//
// 解码结果与磁盘字节都由全局图片缓存（image_cache.go）管理：表情进长期
// 缓存，帖子配图进临时缓存。

// articleBlockAt 返回 Markdown 的 Options.Block：接管含表情或配图的段落 /
// 标题，其余块（代码、列表等）继续交给 Markdown 默认渲染。size 是这类块的
// 基准字号，0 表示跟随 Markdown 主题；评论区比正文小一号，用自己的一套。
func (a *app) articleBlockAt(size float32) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		return a.articleBlock(c, d, i, size)
	}
}

// articleBlock 接管含表情或配图的段落 / 标题，size 见 articleBlockAt。
func (a *app) articleBlock(c *ui.Context, d *parser.Document, i int, size float32) bool {
	b := d.Block(i)
	if b.Kind != parser.Paragraph && b.Kind != parser.Heading {
		return false
	}
	in := d.Inlines(i)
	emoji := a.hasEmojiImage(in)
	picture := a.hasTopLevelPicture(in)
	if !emoji && !picture {
		return false
	}
	if b.Kind == parser.Heading || !picture {
		// 标题或没有配图的段落：表情按行内图片画，链接、强调原样保留。
		a.articleParagraph(c, d, i, b, size)
		return true
	}
	// 有配图的段落：把段落按图片切成几段文字，图片在段落内部独占一行。
	a.articleMixedParagraph(c, d, i, size)
	return true
}

// hasEmojiImage 递归查找表情图片，包括强调、链接里的表情。Next 是整段
// 内联数组里的绝对下标，所以递归时传完整数组和区间，不能先切片再按
// Next 走。
func (a *app) hasEmojiImage(in []parser.Inline) bool {
	return a.hasEmojiImageIn(in, 0, len(in))
}

func (a *app) hasEmojiImageIn(in []parser.Inline, from, to int) bool {
	for k := from; k < to; k = in[k].Next {
		n := &in[k]
		if n.Kind == parser.Image && a.isEmojiImage(n.Dest) {
			return true
		}
		switch n.Kind {
		case parser.Emphasis, parser.Strong, parser.Strikethrough, parser.Link:
			if a.hasEmojiImageIn(in, k+1, n.Next) {
				return true
			}
		}
	}
	return false
}

// hasTopLevelPicture 只找段落顶层的配图；只有这种图片能拆成独立的行，
// 链接或强调内部的图片仍按原来的说明文字显示。
func (a *app) hasTopLevelPicture(in []parser.Inline) bool {
	for k := 0; k < len(in); k = in[k].Next {
		if in[k].Kind == parser.Image && !a.isEmojiImage(in[k].Dest) {
			return true
		}
	}
	return false
}

func (a *app) isEmojiImage(dest string) bool {
	if dest == "" {
		return false
	}
	// 帖子自己的 emojis 表里可能有全局表没有的社区图标，它们的地址都在
	// /emojis/ 下；按路径识别，才能把这类图标也画成行内表情而不是配图。
	if strings.Contains(dest, "/emojis/") {
		return true
	}
	for _, e := range a.emojis {
		if e.ImgURL == dest {
			return true
		}
	}
	return false
}

// articleImage 取回正文 / 评论 / 徽章里的一张图，cat 决定缓存类别：表情与
// 站内图标长期保留，帖子里的外部配图按 TTL 定期清理。没有开启自动加载
// （无头测试）时只读内存缓存，不碰网络；未命中先返回 nil，下载完成后重绘。
func (a *app) articleImage(raw string, cat imageCategory) ui.ImageSource {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if e := a.images.get(raw); e != nil {
		return e.source()
	}
	a.prefetchImage(raw, cat)
	return nil
}

// articleStyle 是行内文字在解析树里逐层叠加的样式。
type articleStyle struct {
	size         float32
	weight       int
	italic       bool
	strike       bool
	color        ui.Color
	font         string
	code, inLink bool
}

// articleRenderer 把一个文字片段收成 RichText，并在字符坐标上记录其中
// 每个表情该画什么。
type articleRenderer struct {
	a       *app
	th      *markdown.Theme
	pending []ui.Span
	runes   int
	objects map[int]ui.ImageSource
}

// articleBaseStyle 返回段落的基准字号、字重与颜色，标题按标题样式，
// 引用内的正文用引用文字色。size 是基准字号（0 跟随 Markdown 主题），
// 标题的 em 倍数按它换算。
func articleBaseStyle(c *ui.Context, d *parser.Document, i int, b *parser.Block, size float32) articleStyle {
	th := markdown.DefaultTheme(c.Theme())
	baseSize := th.FontSize
	if size > 0 {
		baseSize = size
	}
	base := articleStyle{size: baseSize, color: th.Text}
	if b.Kind == parser.Heading {
		level := min(max(b.Level, 1), len(th.Headings))
		h := th.Headings[level-1]
		base.size = baseSize * h.Size
		base.weight = h.Weight
		base.color = h.Color
	} else if articleInQuote(d, i) {
		base.color = th.QuoteText
	}
	return base
}

// articleParagraph 把整段（含表情）收成一个可换行的 RichText。
func (a *app) articleParagraph(c *ui.Context, d *parser.Document, i int, b *parser.Block, size float32) {
	base := articleBaseStyle(c, d, i, b, size)
	th := markdown.DefaultTheme(c.Theme())
	root := ui.RichText(c).FontSize(base.size).TextColor(base.color).FillWidth()
	if base.weight != 0 {
		root.FontWeight(base.weight)
	}
	if b.Kind == parser.Heading {
		root.Role(ui.RoleHeading).Level(b.Level)
	}
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}}
	in := d.Inlines(i)
	root.Children(func() {
		r.inlines(c, in, 0, len(in), base)
		r.flush(c)
	})
	if len(r.objects) > 0 {
		root.DrawObjects(func(p *ui.Painter, at int, rect ui.Rect) {
			if src, ok := r.objects[at]; ok {
				p.Image(src, rect, ui.Contain)
			}
		})
	}
}

// articleMixedParagraph 渲染「文字 + 配图 + 文字」的段落：每个文字
// 片段仍是可换行的 RichText，配图作为片段之间的独立图片。
func (a *app) articleMixedParagraph(c *ui.Context, d *parser.Document, i int, size float32) {
	base := articleBaseStyle(c, d, i, d.Block(i), size)
	in := d.Inlines(i)
	col := ui.Column(c).Gap(4)
	col.Children(func() {
		from := 0
		for k := 0; k < len(in); k = in[k].Next {
			n := &in[k]
			if n.Kind != parser.Image || a.isEmojiImage(n.Dest) {
				continue
			}
			a.articleTextSegment(c, in, from, k, base)
			a.articlePicture(c, n.Dest, parser.PlainText(in[k+1:n.Next]))
			from = n.Next
		}
		a.articleTextSegment(c, in, from, len(in), base)
	})
}

// articleTextSegment 渲染段落里的一段连续文字；片段里可能还有表情。
func (a *app) articleTextSegment(c *ui.Context, in []parser.Inline, from, to int, base articleStyle) {
	if from >= to || articleSegmentEmpty(in, from, to) {
		return
	}
	th := markdown.DefaultTheme(c.Theme())
	root := ui.RichText(c).FontSize(base.size).TextColor(base.color).FillWidth()
	if base.weight != 0 {
		root.FontWeight(base.weight)
	}
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}}
	root.Children(func() {
		r.inlines(c, in, from, to, base)
		r.flush(c)
	})
	if len(r.objects) > 0 {
		root.DrawObjects(func(p *ui.Painter, at int, rect ui.Rect) {
			if src, ok := r.objects[at]; ok {
				p.Image(src, rect, ui.Contain)
			}
		})
	}
}

// articleSegmentEmpty 判断片段是否只有空白，只有空白就不必占一行。
func articleSegmentEmpty(in []parser.Inline, from, to int) bool {
	for k := from; k < to; k = in[k].Next {
		switch in[k].Kind {
		case parser.Text:
			if strings.TrimSpace(in[k].Text) != "" {
				return false
			}
		case parser.SoftBreak, parser.LineBreak:
		default:
			return false
		}
	}
	return true
}

// articlePicture 显示一张正文配图：加载中先占位，加载后按原比例缩到栏宽。
// 配图是帖子里的外部图片，进临时缓存，过期后自动清理。
func (a *app) articlePicture(c *ui.Context, dest, alt string) {
	th := markdown.DefaultTheme(c.Theme())
	if alt == "" {
		alt = "图片"
	}
	src := a.articleImage(dest, imageCacheTemporary)
	if src == nil {
		ui.Text(c, "["+alt+"]").FontSize(12).TextColor(th.Muted)
		return
	}
	img := ui.Image(c, src).MaxWidthPercent(100).Radius(4).Label(alt)
	if w, h := ui.ImageSize(src); w > 0 && h > 0 {
		img.AspectRatio(w / h)
	}
}

func articleInQuote(d *parser.Document, i int) bool {
	for p := d.Block(i).Parent; p > 0; p = d.Block(p).Parent {
		if d.Block(p).Kind == parser.Quote {
			return true
		}
	}
	return false
}

func (r *articleRenderer) inlines(c *ui.Context, in []parser.Inline, from, to int, st articleStyle) {
	for k := from; k < to; k = in[k].Next {
		n := &in[k]
		switch n.Kind {
		case parser.Text:
			r.addText(n.Text, st)
		case parser.SoftBreak:
			// 源文在交给 Markdown 前已经按「一个回车换行」改写成硬换行
			// （见 markdown_breaks.go）；这里兜底同样显示为换行。
			r.addText("\n", st)
		case parser.LineBreak:
			r.addText("\n", st)
		case parser.Code:
			cs := st
			cs.code = true
			r.addText(n.Text, cs)
		case parser.Emphasis:
			ns := st
			ns.italic = true
			r.inlines(c, in, k+1, n.Next, ns)
		case parser.Strong:
			ns := st
			ns.weight = r.th.StrongWeight
			r.inlines(c, in, k+1, n.Next, ns)
		case parser.Strikethrough:
			ns := st
			ns.strike = true
			r.inlines(c, in, k+1, n.Next, ns)
		case parser.Link:
			if st.inLink || !articleLinkSafe(n.Dest) {
				r.inlines(c, in, k+1, n.Next, st)
				continue
			}
			r.flush(c)
			dest := n.Dest
			link := ui.Link(c, "", "").TextColor(r.th.Link)
			if link.Clicked() {
				r.a.handleLink(dest)
			}
			link.Children(func() {
				ls := st
				ls.inLink = true
				r.inlines(c, in, k+1, n.Next, ls)
				r.flush(c)
			})
		case parser.Image:
			alt := parser.PlainText(in[k+1 : n.Next])
			r.addImage(c, n.Dest, alt, st)
		default:
			// HTML、数学、脚注与提及：正文里保留原文字符串，不吞内容。
			r.addText(n.Text, st)
		}
	}
}

// articleLinkSafe 与 Markdown 视图的链接判断一致：只放行 http(s) 等
// ui.SafeToOpen 认识的地址和站内绝对路径，javascript: 等一律不变成链接。
func articleLinkSafe(dest string) bool {
	if dest == "" || strings.ContainsAny(dest, "\\\x00") || strings.HasPrefix(dest, "//") {
		return false
	}
	if i := strings.IndexAny(dest, ":/?#"); i >= 0 && dest[i] == ':' {
		return ui.SafeToOpen(dest)
	}
	return strings.HasPrefix(dest, "/")
}

func (r *articleRenderer) addImage(c *ui.Context, dest, alt string, st articleStyle) {
	// 只有内置表情按行内图片画；普通配图由 articleMixedParagraph 单独
	// 成行，这里遇到（例如链接内部的图片）只保留说明文字。
	if !r.a.isEmojiImage(dest) {
		if alt == "" {
			alt = "图片"
		}
		muted := st
		muted.color = r.th.Muted
		r.addText("["+alt+"]", muted)
		return
	}
	// 表情与社区小图标是服务端固定的一批图，进长期缓存。
	src := r.a.articleImage(dest, imageCacheStable)
	if src == nil {
		if alt == "" {
			alt = "图片"
		}
		muted := st
		muted.color = r.th.Muted
		r.addText("["+alt+"]", muted)
		return
	}
	w, h := ui.ImageSize(src)
	if w <= 0 || h <= 0 {
		r.addText("["+alt+"]", st)
		return
	}
	size := st.size
	if size <= 0 {
		size = r.th.FontSize
	}
	// 表情按一个汉字高（1em）占位：0.8em 在基线以上、0.2em 在基线
	// 以下，行高不变，表情与文字对齐。
	ascent, descent := size*0.8, size*0.2
	width := (ascent + descent) * w / h
	r.objects[r.runes] = src
	r.pending = append(r.pending, ui.Span{
		Text:          "\uFFFC",
		Size:          size,
		ObjectWidth:   width,
		ObjectHeight:  ascent,
		ObjectDescent: descent,
	})
	r.runes++
}

func (r *articleRenderer) addText(text string, st articleStyle) {
	if text == "" {
		return
	}
	r.pending = append(r.pending, r.span(text, st))
	r.runes += utf8.RuneCountInString(text)
}

func (r *articleRenderer) span(text string, st articleStyle) ui.Span {
	size := st.size
	if size == 0 {
		size = r.th.FontSize
	}
	sp := ui.Span{
		Text:          text,
		Font:          r.th.Font,
		Size:          size,
		Weight:        st.weight,
		Italic:        st.italic,
		Strikethrough: st.strike,
		Color:         st.color,
	}
	if st.font != "" {
		sp.Font = st.font
	}
	if st.code {
		sp.Font = r.th.CodeFont
		sp.Size = size * r.th.CodeSize
		sp.Background = r.th.CodeBackground
		sp.Radius = max(3, sp.Size/4)
		if !st.inLink && st.color == r.th.Text {
			sp.Color = r.th.Code
		}
	}
	if st.inLink {
		// 链接里的文字统一用链接色，强调、代码等只保留字形，不改色。
		sp.Color = r.th.Link
		if r.th.LinkWeight > sp.Weight {
			sp.Weight = r.th.LinkWeight
		}
	}
	return sp
}

func (r *articleRenderer) flush(c *ui.Context) {
	if len(r.pending) == 0 {
		return
	}
	pending := r.pending
	r.pending = nil
	ui.RichText(c, pending...)
}
