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

// articlePicture 是从源文里认出的一张配图：地址与说明文字。多图预览弹窗
// （见 image_viewer.go）按这些翻页与另存为。
type articlePicture struct {
	URL string
	Alt string
}

// articleBlockAt 返回 Markdown 的 Options.Block：接管含表情或配图的段落 /
// 标题，其余块（代码、列表等）继续交给 Markdown 默认渲染。size 是这类块的
// 基准字号，0 表示跟随 Markdown 主题；评论区比正文小一号，用自己的一套。
//
// pictures 按解析好的文档给出这份源文的整组配图（见 newMarkdownOptions 的
// groupPictures）：Markdown 视图在重新解析之后才逐块调用 Block，这时按文档
// 收图才对得上号，所以这里拿的是函数而不是一张事先算好的表。
func (a *app) articleBlockAt(size float32, pictures func(d *parser.Document) []articlePicture) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		return a.articleBlock(c, d, i, size, pictures(d))
	}
}

// articleBlock 接管含表情或配图的段落 / 标题 / 表格，size 见 articleBlockAt，
// pictures 是 d 所在那份 Markdown 的整组配图。
func (a *app) articleBlock(c *ui.Context, d *parser.Document, i int, size float32, pictures []articlePicture) bool {
	b := d.Block(i)
	if b.Kind == parser.Table {
		// 表格单元格不会经过 Options.Block：Markdown 画表格时直接排每一格。
		// 所以含图片的表格要整张接管，否则单元格里的图片只剩 [说明]。
		if !a.tableHasImage(d, i) {
			return false
		}
		a.articleTable(c, d, i, size, pictures)
		return true
	}
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
	a.articleMixedParagraph(c, d, i, size, pictures)
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
// 每个表情该画什么。measure 为真时只收集 span、不创建元素：表格用它量列宽。
type articleRenderer struct {
	a       *app
	th      *markdown.Theme
	pending []ui.Span
	runes   int
	objects map[int]ui.ImageSource
	measure bool
	// measured 是测量模式下收到的全部 span，按出现顺序，供调用方整体度量。
	measured []ui.Span
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
func (a *app) articleMixedParagraph(c *ui.Context, d *parser.Document, i int, size float32, pictures []articlePicture) {
	base := articleBaseStyle(c, d, i, d.Block(i), size)
	a.articleMixedInlines(c, d.Inlines(i), base, ui.Start, pictures)
}

// articleMixedInlines 是 articleMixedParagraph 的实现，也用于表格单元格：
// 一段内联按顶层配图切成若干段文字，配图在段落内部独占一行。
//
// pictures 是这张表所在那份 Markdown 的全部配图（见 articlePictures）：每张
// 图点开时打开它所在的那一组并停在自己身上。收图不在这里做——一段内联只
// 知道自己的图，不知道同一份正文里还有几张，组要按整份源文认。
func (a *app) articleMixedInlines(c *ui.Context, in []parser.Inline, base articleStyle, align ui.Align, pictures []articlePicture) {
	col := ui.Column(c).Gap(4)
	col.Children(func() {
		from := 0
		for k := 0; k < len(in); k = in[k].Next {
			n := &in[k]
			if n.Kind != parser.Image || a.isEmojiImage(n.Dest) {
				continue
			}
			a.articleTextSegment(c, in, from, k, base, align)
			a.articlePicture(c, n.Dest, parser.PlainText(in[k+1:n.Next]), pictures)
			from = n.Next
		}
		a.articleTextSegment(c, in, from, len(in), base, align)
	})
}

// articleTextSegment 渲染段落里的一段连续文字；片段里可能还有表情。
func (a *app) articleTextSegment(c *ui.Context, in []parser.Inline, from, to int, base articleStyle, align ui.Align) {
	if from >= to || articleSegmentEmpty(in, from, to) {
		return
	}
	th := markdown.DefaultTheme(c.Theme())
	root := ui.RichText(c).FontSize(base.size).TextColor(base.color).FillWidth()
	if base.weight != 0 {
		root.FontWeight(base.weight)
	}
	if align != ui.Start {
		root.TextAlign(align)
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

// 配图占位里的说明文案：加载中与失败各一句，测试也据此判断进度有没有退场。
// 尺寸还不知道的第一张图给一条固定高度的空框，让转圈有个位置。
const (
	imageLoadingText                     = "图片加载中…"
	imageFailedText                      = "图片加载失败"
	imageUnknownSizePlaceholderH float32 = 72
)

// articlePicture 显示一张正文配图：仍在下载时转圈并说明，失败后给一句说明，
// 加载完成后按原比例缩到栏宽。配图是帖子里的外部图片，进临时缓存，过期后
// 自动清理。点一下打开多图预览弹窗（见 image_viewer.go）。
//
// 占位按图片的真实宽高来：图片已经在磁盘缓存里时（读文件头，见
// imageCache.cachedSize），先画一个同样大小的空框，图片解好后原地替换，
// 下方的文字不会被顶下去。没有登记过加载的图（无头测试不联网、地址不合法）
// 沿用原来的静态占位，不假装有一张正在下载的图。
//
// pictures 是这份正文的全部配图（见 articlePictures），点开预览时按它在整
// 组里翻页。还在加载或加载失败的占位也能点：加载中点一下先记下，图片到位后
// 直接弹窗；失败的只留说明，点了什么也不会发生。
func (a *app) articlePicture(c *ui.Context, dest, alt string, pictures []articlePicture) {
	th := markdown.DefaultTheme(c.Theme())
	if alt == "" {
		alt = "图片"
	}
	dest = strings.TrimSpace(dest)
	src := a.articleImage(dest, imageCacheTemporary)
	w, h := ui.ImageSize(src)
	if w <= 0 || h <= 0 {
		w, h, _ = a.images.cachedSize(dest, imageCacheTemporary)
	}
	if src != nil {
		img := ui.Image(c, src).MaxWidthPercent(100).Radius(4).Label(alt)
		if w > 0 && h > 0 {
			img.AspectRatio(w / h)
		}
		img.Cursor(ui.CursorPointer).Tooltip("点开预览大图")
		img.OnClick(func() { a.openImageGroup(pictures, dest) })
		return
	}
	state := a.images.state(dest)
	if state == imageIdle {
		if w <= 0 || h <= 0 {
			ui.Text(c, "["+alt+"]").FontSize(12).TextColor(th.Muted)
			return
		}
		ph := ui.Box(c).Width(w).MaxWidthPercent(100).AspectRatio(w / h).
			Radius(4).Background(th.CodeBackground).Center().Clip()
		ph.Role(ui.RoleImage).Label(alt)
		ph.Children(func() {
			ui.Text(c, alt).FontSize(12).TextColor(th.Muted).MaxLines(2)
		})
		ph.OnClick(func() { a.openImageGroup(pictures, dest) })
		return
	}
	a.articlePicturePending(c, dest, alt, w, h, pictures)
}

// articlePicturePending 画「加载中」或「加载失败」的配图占位。尺寸已知时仍
// 按真实宽高留白，图片到达后下面的文字不会跳；尺寸未知时用固定高度的空框。
// 失败态不转圈也不重发请求，只说明是哪张图没加载出来。
//
// 占位框只设 Label、不设 RoleImage：mygo 会把显式 RoleImage 盒的子树在访问树
// 里压平，导致里面的转圈（RoleProgress）无法被读取；加载完成后真正的
// ui.Image 自带图片角色，这里不必抢。
func (a *app) articlePicturePending(c *ui.Context, dest, alt string, w, h float32, pictures []articlePicture) {
	th := markdown.DefaultTheme(c.Theme())
	box := ui.Box(c).Radius(4).Background(th.CodeBackground).Center().Clip()
	if w > 0 && h > 0 {
		box.Width(w).MaxWidthPercent(100).AspectRatio(w / h)
	} else {
		box.FillWidth().Height(imageUnknownSizePlaceholderH)
	}
	box.Label(alt)
	loading := a.images.state(dest) == imageLoading
	// 加载中与加载失败的占位都按 articlePicture 的同一套点击处理：加载中点
	// 一下先记下，图片到位后自动弹出预览（见 wantImage）。
	box.OnClick(func() { a.wantImageGroup(pictures, dest) })
	if loading {
		box.Busy(true)
	}
	box.Children(func() {
		ui.Column(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if loading {
				ui.Spinner(c).Size(16, 16).Label("图片加载中")
			}
			text := imageFailedText
			if loading {
				text = imageLoadingText
			}
			ui.Text(c, text).FontSize(12).TextColor(th.Muted).SingleLine()
			if !loading {
				ui.Text(c, alt).FontSize(11).TextColor(th.Muted).MaxLines(2)
			}
		})
	})
}

// tableHasImage 报告表格里是否有图片：单元格里的配图与行内 :code: 表情
// 都算。只有这样的表格才需要接管，其余仍交给 Markdown 自己排。
func (a *app) tableHasImage(d *parser.Document, i int) bool {
	end := d.Block(i).Next
	for r := i + 1; r < end; r = d.Block(r).Next {
		cend := d.Block(r).Next
		for cell := r + 1; cell < cend; cell = d.Block(cell).Next {
			in := d.Inlines(cell)
			if a.articleHasImage(in, 0, len(in)) {
				return true
			}
		}
	}
	return false
}

// articleHasImage 递归查找任意图片，包括强调、链接里的图片。与 hasEmojiImageIn
// 同一套下标规则：Next 是整段内联数组里的绝对下标，递归时传区间。
func (a *app) articleHasImage(in []parser.Inline, from, to int) bool {
	for k := from; k < to; k = in[k].Next {
		n := &in[k]
		if n.Kind == parser.Image {
			return true
		}
		switch n.Kind {
		case parser.Emphasis, parser.Strong, parser.Strikethrough, parser.Link:
			if a.articleHasImage(in, k+1, n.Next) {
				return true
			}
		}
	}
	return false
}

// articleTable 排一张含图片的表格。版式与 Markdown 自带的表格一致：列宽按
// 内容测量（上限 TableMaxColumn），单元格内边距、表头底色、隔行底色、列之间
// 的竖线都由主题决定，整张表比栏宽更宽时横向滚动。
//
// 与默认实现的唯一区别是单元格内容由 article_render.go 自己画，因此配图和
// :code: 表情在表格里也能显示成图片。
func (a *app) articleTable(c *ui.Context, d *parser.Document, i int, size float32, pictures []articlePicture) {
	th := markdown.DefaultTheme(c.Theme())
	b := d.Block(i)
	cols := len(b.Align)
	padX, padY := th.TableCellPadding[0]*th.FontSize, th.TableCellPadding[1]*th.FontSize
	maxW := th.TableMaxColumn * th.FontSize
	// 列宽取这一列所有单元格里最宽的一个，表格每次重排都重新量。
	widths := make([]float32, cols)
	end := d.Block(i).Next
	for r := i + 1; r < end; r = d.Block(r).Next {
		row := d.Block(r)
		k := 0
		for cell := r + 1; cell < d.Block(r).Next; cell = d.Block(cell).Next {
			if k >= cols {
				break
			}
			s := articleBaseStyle(c, d, cell, d.Block(cell), size)
			if row.Header {
				s.weight = th.StrongWeight
			}
			w := a.articleCellWidth(c, d.Inlines(cell), s, maxW)
			widths[k] = max(widths[k], min(w, maxW)+2*padX+1)
			k++
		}
	}
	scroll := ui.ScrollHorizontal(c).AlignSelf(ui.Stretch)
	scroll.Children(func() {
		grid := ui.Column(c).Border(1, th.TableBorder).Radius(0.3 * th.FontSize).
			Clip().Shrink(0).Role(ui.RoleTable)
		grid.Children(func() {
			n := 0
			for r := i + 1; r < end; r = d.Block(r).Next {
				row := d.Block(r)
				rowEl := ui.Row(c).Role(ui.RoleRow).AlignItems(ui.Stretch)
				if row.Header {
					rowEl.Background(th.TableHeader)
				} else if n%2 == 1 && th.TableStripe.A > 0 {
					rowEl.Background(th.TableStripe)
				}
				if n > 0 {
					rowEl.BorderWidth(1, 0, 0, 0).BorderColor(th.TableBorder)
				}
				header := row.Header
				rowEl.Children(func() {
					k := 0
					for cell := r + 1; cell < d.Block(r).Next; cell = d.Block(cell).Next {
						if k >= cols {
							break
						}
						s := articleBaseStyle(c, d, cell, d.Block(cell), size)
						role := ui.RoleCell
						if header {
							s.weight = th.StrongWeight
							role = ui.RoleColumnHeader
						}
						align := ui.Start
						switch b.Align[k] {
						case parser.AlignCenter:
							align = ui.Center
						case parser.AlignRight:
							align = ui.End
						}
						box := ui.Column(c).Width(widths[k]).Padding(padY, padX).Role(role)
						if k > 0 {
							box.BorderWidth(0, 0, 0, 1).BorderColor(th.TableBorder)
						}
						box.Children(func() { a.articleCell(c, d, cell, s, align, pictures) })
						k++
					}
				})
				n++
			}
		})
	})
}

// articleCell 画一格内容：有顶层配图时按「文字 + 图片 + 文字」排，否则就是
// 一段可换行的文字（其中的表情仍是行内图片）。pictures 见 articlePicture。
func (a *app) articleCell(c *ui.Context, d *parser.Document, cell int, base articleStyle, align ui.Align, pictures []articlePicture) {
	in := d.Inlines(cell)
	if a.hasTopLevelPicture(in) {
		a.articleMixedInlines(c, in, base, align, pictures)
		return
	}
	a.articleTextSegment(c, in, 0, len(in), base, align)
}

// articleCellWidth 估算一格内容需要的宽度：文字按硬换行拆行后取最宽的一行，
// 配图按已知的自然宽度（还不知道时按列宽上限占位）。
func (a *app) articleCellWidth(c *ui.Context, in []parser.Inline, base articleStyle, maxW float32) float32 {
	w := float32(0)
	for k := 0; k < len(in); k = in[k].Next {
		if n := &in[k]; n.Kind == parser.Image && !a.isEmojiImage(n.Dest) {
			// 顶层配图独占一行，它比文字宽时整格按它算。
			w = max(w, a.articlePictureWidth(n.Dest, maxW))
		}
	}
	th := markdown.DefaultTheme(c.Theme())
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}, measure: true}
	r.inlines(c, in, 0, len(in), base)
	r.flush(c)
	return max(w, articleLinesWidth(c, r.measured))
}

// articlePictureWidth 返回配图在表格里需要的宽度：尺寸已知就用它，还没下载
// 或解出来时按列宽上限占位（图片到位后表格重排一次，宽度随之收紧）。
func (a *app) articlePictureWidth(dest string, maxW float32) float32 {
	if src := a.images.source(dest); src != nil {
		if w, _ := ui.ImageSize(src); w > 0 {
			return min(w, maxW)
		}
	}
	if w, _, ok := a.images.cachedSize(dest, imageCacheTemporary); ok && w > 0 {
		return min(w, maxW)
	}
	return maxW
}

// articleLinesWidth 返回这一串 span 按硬换行拆开后最宽的一行。测量用的是
// 与绘制同一个 RichText 的度量，所以强调、行内代码与表情对象的宽度都算在内。
func articleLinesWidth(c *ui.Context, spans []ui.Span) float32 {
	widest := float32(0)
	var line []ui.Span
	flush := func() {
		if len(line) > 0 {
			w, _ := c.MeasureText(0, line...)
			widest = max(widest, w)
		}
		line = line[:0]
	}
	for _, sp := range spans {
		if !strings.Contains(sp.Text, "\n") {
			line = append(line, sp)
			continue
		}
		for i, part := range strings.Split(sp.Text, "\n") {
			if i > 0 {
				flush()
			}
			if part == "" {
				continue
			}
			p := sp
			p.Text = part
			line = append(line, p)
		}
	}
	flush()
	return widest
}

// articlePictures 按顺序认出整份文档里的全部配图，供 Options.Group 设置
// 分组（见 newMarkdownOptions）：段落顶层的配图、表格单元格里的配图，以及
// 强调 / 链接里的配图都算；行内 :code: 表情与社区图标不算（见 isEmojiImage）。
// 标题里的图片同样成行显示，也算在内。规则与 articleBlock 同源，所以数出来
// 的张数与画面里显示的一致。
func (a *app) articlePictures(d *parser.Document) []articlePicture {
	var out []articlePicture
	for i := 0; i < d.Len(); i++ {
		b := d.Block(i)
		switch b.Kind {
		case parser.Table:
			// 表格的行与单元格也是块：这里只收顶层块，表格自己展开每一格。
			if !a.tableHasImage(d, i) {
				continue
			}
			for r := i + 1; r < b.Next; r = d.Block(r).Next {
				for cell := r + 1; cell < d.Block(r).Next; cell = d.Block(cell).Next {
					in := d.Inlines(cell)
					collectArticlePictures(&out, a, in, 0, len(in))
				}
			}
			i = b.Next - 1 // 表格的行与单元格不必再当独立块扫一遍
		case parser.Paragraph, parser.Heading:
			in := d.Inlines(i)
			collectArticlePictures(&out, a, in, 0, len(in))
		}
	}
	return out
}

// collectArticlePictures 递归收集 in[from:to] 里的配图，规则见 articlePictures。
func collectArticlePictures(out *[]articlePicture, a *app, in []parser.Inline, from, to int) {
	for k := from; k < to; k = in[k].Next {
		n := &in[k]
		switch n.Kind {
		case parser.Image:
			if a.isEmojiImage(n.Dest) || strings.TrimSpace(n.Dest) == "" {
				continue
			}
			alt := strings.TrimSpace(parser.PlainText(in[k+1 : n.Next]))
			if alt == "" {
				alt = "图片"
			}
			*out = append(*out, articlePicture{URL: strings.TrimSpace(n.Dest), Alt: alt})
		case parser.Emphasis, parser.Strong, parser.Strikethrough, parser.Link:
			collectArticlePictures(out, a, in, k+1, n.Next)
		}
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
			if r.measure {
				// 量宽度时不建设链接元素，只把里面的文字接着收进 span。
				ls := st
				ls.inLink = true
				r.inlines(c, in, k+1, n.Next, ls)
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
	size := st.size
	if size <= 0 {
		size = r.th.FontSize
	}
	w, h := ui.ImageSize(src)
	if src != nil && (w <= 0 || h <= 0) {
		if alt == "" {
			alt = "图片"
		}
		r.addText("["+alt+"]", st)
		return
	}
	if src == nil {
		// 图片还没到也先按 1em 方形占位：表情图都是方的，图到之后原地
		// 变成图片，这一行不会因为宽度变化而重排；失败时也只是留一个小空位。
		w, h = 1, 1
	}
	// 表情按一个汉字高（1em）占位：0.8em 在基线以上、0.2em 在基线
	// 以下，行高不变，表情与文字对齐。
	ascent, descent := size*0.8, size*0.2
	width := (ascent + descent) * w / h
	if src != nil {
		r.objects[r.runes] = src
	}
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
	if r.measure {
		r.measured = append(r.measured, pending...)
		return
	}
	ui.RichText(c, pending...)
}
