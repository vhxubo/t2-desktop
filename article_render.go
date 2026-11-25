package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
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

// articlePicture 是从源文里认出的一项媒体：地址、说明文字，以及它是一张图
// 还是一段站外视频。多图预览窗口（见 image_viewer.go）按这些翻页、缩放与
// 另存为，视频则在预览里给出封面卡片、点开在浏览器里播放。
type articlePicture struct {
	URL string
	Alt string
	// Kind 是这一项的类型，零值是图片。视频条目由正文里「独占一段的视频
	// 链接」认出来（见 media.go 的 standaloneMediaLink）。
	Kind mediaKind
}

// ref 返回这一项的媒体信息：视频按地址重新认一遍来源与编号，图片只带地址。
func (p articlePicture) ref() mediaRef {
	if p.Kind == mediaVideo {
		if ref, ok := parseVideoLink(p.URL); ok {
			return ref
		}
		return mediaRef{URL: p.URL, Kind: mediaVideo, WatchURL: p.URL}
	}
	return mediaRef{URL: p.URL, Kind: mediaImage}
}

// articleBlockAt 返回 Markdown 的 Options.Block：接管含表情或配图的段落 /
// 标题，其余块（代码、列表等）继续交给 Markdown 默认渲染。size 是这类块的
// 基准字号，0 表示跟随 Markdown 主题；评论区比正文小一号，用自己的一套。
// imageMaxH 是这份视图的图片高度上限，0 表示不限制。
//
// pictures 按解析好的文档给出这份源文的整组配图（见 newMarkdownOptions 的
// groupPictures）：Markdown 视图在重新解析之后才逐块调用 Block，这时按文档
// 收图才对得上号，所以这里拿的是函数而不是一张事先算好的表。
func (a *app) articleBlockAt(size, imageMaxH float32, pictures func(d *parser.Document) []articlePicture, mentions bool) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		return a.articleBlock(c, d, i, size, imageMaxH, pictures(d), mentions)
	}
}

// articleBlock 接管含表情、配图或提及的段落 / 标题 / 表格，size 见
// articleBlockAt，pictures 是 d 所在那份 Markdown 的整组配图。imageMaxH 是这
// 份视图的图片高度上限（0 表示不限）；mentions 为真时，正文里的 @名字 画成
// 可点击的提及组件（只有评论区打开，见 detail_ui.go）。
func (a *app) articleBlock(c *ui.Context, d *parser.Document, i int, size, imageMaxH float32, pictures []articlePicture, mentions bool) bool {
	b := d.Block(i)
	if b.Kind == parser.Table {
		// 表格单元格不会经过 Options.Block：Markdown 画表格时直接排每一格。
		// 所以含图片的表格要整张接管，否则单元格里的图片只剩 [说明]。
		if !a.tableHasImage(d, i) {
			return false
		}
		a.articleTable(c, d, i, size, imageMaxH, pictures, mentions)
		return true
	}
	if b.Kind != parser.Paragraph && b.Kind != parser.Heading {
		return false
	}
	in := d.Inlines(i)
	// 独占一段的媒体链接：站外视频画成封面卡片，裸图片地址按图片嵌入。
	// 这两种写法在 2Libra 的正文里很常见，Markdown 默认只会留下一条链接。
	if b.Kind == parser.Paragraph {
		if ref, alt, ok := standaloneMediaLink(in); ok {
			if ref.Kind == mediaVideo {
				if alt == "" {
					alt = ref.title()
				}
				a.articleVideoEmbed(c, ref, alt, pictures, imageMaxH)
			} else {
				if alt == "" {
					alt = ref.title()
				}
				a.articlePicture(c, ref.URL, alt, pictures, imageMaxH)
			}
			return true
		}
	}
	emoji := a.hasEmojiImage(in)
	picture := a.hasTopLevelPicture(in)
	mention := mentions && hasMention(in)
	if !emoji && !picture && !mention {
		return false
	}
	if b.Kind == parser.Heading || !picture {
		// 标题或没有配图的段落：表情按行内图片画，链接、强调原样保留。
		a.articleParagraph(c, d, i, b, size, mentions)
		return true
	}
	// 有配图的段落：把段落按图片切成几段文字，图片在段落内部独占一行。
	a.articleMixedParagraph(c, d, i, size, imageMaxH, pictures, mentions)
	return true
}

// hasMention 递归查找一段内联里的提及，口径与 displayMentions 一致（见
// mentions.go）。@ 在链接、行内代码里不算：那些位置的文字不该变成组件。
func hasMention(in []parser.Inline) bool {
	return hasMentionIn(in, 0, len(in))
}

func hasMentionIn(in []parser.Inline, from, to int) bool {
	for k := from; k < to; k = in[k].Next {
		n := &in[k]
		switch n.Kind {
		case parser.Text, parser.Mention:
			if len(displayMentions(n.Text)) > 0 {
				return true
			}
		case parser.Emphasis, parser.Strong, parser.Strikethrough:
			if hasMentionIn(in, k+1, n.Next) {
				return true
			}
		}
	}
	return false
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
	// mentions 为真时把正文里的 @名字 画成可点击的组件；只有评论区打开。
	mentions bool
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
func (a *app) articleParagraph(c *ui.Context, d *parser.Document, i int, b *parser.Block, size float32, mentions bool) {
	base := articleBaseStyle(c, d, i, b, size)
	th := markdown.DefaultTheme(c.Theme())
	root := ui.RichText(c).FontSize(base.size).TextColor(base.color).FillWidth()
	if base.weight != 0 {
		root.FontWeight(base.weight)
	}
	if b.Kind == parser.Heading {
		root.Role(ui.RoleHeading).Level(b.Level)
	}
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}, mentions: mentions}
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
func (a *app) articleMixedParagraph(c *ui.Context, d *parser.Document, i int, size, imageMaxH float32, pictures []articlePicture, mentions bool) {
	base := articleBaseStyle(c, d, i, d.Block(i), size)
	a.articleMixedInlines(c, d.Inlines(i), base, ui.Start, pictures, imageMaxH, mentions)
}

// articleMixedInlines 是 articleMixedParagraph 的实现，也用于表格单元格：
// 一段内联按顶层配图切成若干段文字，配图在段落内部独占一行。
//
// pictures 是这张表所在那份 Markdown 的全部配图（见 articlePictures）：每张
// 图点开时打开它所在的那一组并停在自己身上。收图不在这里做——一段内联只
// 知道自己的图，不知道同一份正文里还有几张，组要按整份源文认。
func (a *app) articleMixedInlines(c *ui.Context, in []parser.Inline, base articleStyle, align ui.Align, pictures []articlePicture, imageMaxH float32, mentions bool) {
	col := ui.Column(c).Gap(4)
	col.Children(func() {
		from := 0
		for k := 0; k < len(in); k = in[k].Next {
			n := &in[k]
			if n.Kind != parser.Image || a.isEmojiImage(n.Dest) {
				continue
			}
			a.articleTextSegment(c, in, from, k, base, align, mentions)
			a.articlePicture(c, n.Dest, parser.PlainText(in[k+1:n.Next]), pictures, imageMaxH)
			from = n.Next
		}
		a.articleTextSegment(c, in, from, len(in), base, align, mentions)
	})
}

// articleTextSegment 渲染段落里的一段连续文字；片段里可能还有表情。
func (a *app) articleTextSegment(c *ui.Context, in []parser.Inline, from, to int, base articleStyle, align ui.Align, mentions bool) {
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
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}, mentions: mentions}
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

// 配图占位里的说明文案：加载中、失败与「默认不加载图片」各一句，测试也据此
// 判断进度有没有退场。尺寸还不知道的第一张图给一条固定高度的空框，让转圈
// 有个位置。
const (
	imageLoadingText                     = "图片加载中…"
	imageFailedText                      = "图片加载失败"
	imageRetryText                       = "点击重试"
	imageLazyText                        = "点一下加载图片"
	imageUnknownSizePlaceholderH float32 = 72
	// imageVideoCardMaxH 是正文里视频封面卡片的高度上限：再宽的一栏也不该让
	// 一张卡片占满整屏。评论区用更小的 commentImageMaxH，见下。
	imageVideoCardMaxH float32 = 380
	// commentImageMaxH 是评论里配图与视频卡片的高度上限：再高的竖图也要
	// 收窄到这个高度，不能让一条评论占满整个评论区。正文不设上限。
	commentImageMaxH float32 = 240
)

// imageBoxWidth 返回图片为不越过 imageMaxH 而需要的最大宽度。
// 尺寸未知或不设上限时返回 0，调用方就不加 MaxWidth。
func imageBoxWidth(w, h, maxH float32) float32 {
	if w <= 0 || h <= 0 || maxH <= 0 {
		return 0
	}
	return maxH * w / h
}

// articlePicture 显示一张配图：仍在下载时转圈并说明，失败后给一句说明，
// 加载完成后按原比例缩到栏宽，imageMaxH 大于 0 时再按原比例收窄到不超过
// 这个高度（评论区用，见 commentImageMaxH）。图比栏窄时在栏内左右居中。
// 配图是帖子里的外部图片，进
// 临时缓存，过期后自动清理。点一下打开多图预览窗口（见 image_viewer.go）。
//
// 占位按图片的真实宽高来：图片已经在磁盘缓存里时（读文件头，见
// imageCache.cachedSize），先画一个同样大小的空框，图片解好后原地替换，
// 下方的文字不会被顶下去。没有登记过加载的图（无头测试不联网、地址不合法）
// 沿用原来的静态占位，不假装有一张正在下载的图。
//
// pictures 是这份正文的全部配图（见 articlePictures），点开预览时按它在整
// 组里翻页。还在加载或加载失败的占位也能点：加载中点一下先记下，图片到位后
// 直接开预览窗口；失败后点一下重新下载这张图（见 retryImage）。
func (a *app) articlePicture(c *ui.Context, dest, alt string, pictures []articlePicture, imageMaxH float32) {
	th := markdown.DefaultTheme(c.Theme())
	if alt == "" {
		alt = "图片"
	}
	dest = strings.TrimSpace(dest)
	if a.imageHidden(dest) {
		a.articlePictureLazy(c, dest, alt, imageMaxH)
		return
	}
	src := a.articleImage(dest, imageCacheTemporary)
	w, h := ui.ImageSize(src)
	if w <= 0 || h <= 0 {
		w, h, _ = a.images.cachedSize(dest, imageCacheTemporary)
	}
	if src != nil {
		img := ui.Image(c, src).MaxWidthPercent(100).Radius(4).Label(alt).
			AlignSelf(ui.Center)
		if w > 0 && h > 0 {
			img.AspectRatio(w / h)
			if maxW := imageBoxWidth(w, h, imageMaxH); maxW > 0 {
				img.MaxWidth(maxW)
			}
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
			Radius(4).Background(th.CodeBackground).Center().Clip().
			AlignSelf(ui.Center)
		if maxW := imageBoxWidth(w, h, imageMaxH); maxW > 0 {
			ph.MaxWidth(maxW)
		}
		ph.Role(ui.RoleImage).Label(alt)
		ph.Children(func() {
			ui.Text(c, alt).FontSize(12).TextColor(th.Muted).MaxLines(2)
		})
		ph.OnClick(func() { a.openImageGroup(pictures, dest) })
		return
	}
	a.articlePicturePending(c, dest, alt, w, h, pictures, imageMaxH)
}

// articlePicturePending 画「加载中」或「加载失败」的配图占位。尺寸已知时仍
// 按真实宽高留白，图片到达后下面的文字不会跳；评论区的高度上限也按同一套
// 宽度换算，保证占位与成图边界一致。尺寸未知时用固定高度的空框。
// 加载态不重发请求，转圈说明进度；失败态不转圈，点一下重新下载并回到加载态。
//
// 占位框只设 Label、不设 RoleImage：mygo 会把显式 RoleImage 盒的子树在访问树
// 里压平，导致里面的转圈（RoleProgress）无法被读取；加载完成后真正的
// ui.Image 自带图片角色，这里不必抢。
func (a *app) articlePicturePending(c *ui.Context, dest, alt string, w, h float32, pictures []articlePicture, imageMaxH float32) {
	th := markdown.DefaultTheme(c.Theme())
	box := ui.Box(c).Radius(4).Background(th.CodeBackground).Center().Clip().
		AlignSelf(ui.Center)
	if w > 0 && h > 0 {
		box.Width(w).MaxWidthPercent(100).AspectRatio(w / h)
		if maxW := imageBoxWidth(w, h, imageMaxH); maxW > 0 {
			box.MaxWidth(maxW)
		}
	} else {
		box.FillWidth().Height(imageUnknownSizePlaceholderH)
	}
	box.Label(alt)
	loading := a.images.state(dest) == imageLoading
	if loading {
		// 加载中点一下先记下，图片到位后自动弹出预览（见 wantImage）。
		box.OnClick(func() { a.wantImageGroup(pictures, dest) })
		box.Busy(true)
	} else {
		// 失败的图会一直停在占位里，点整块占位重新下载（见 retryImage）。
		box.Cursor(ui.CursorPointer)
		box.OnClick(func() { a.retryImage(dest, imageCacheTemporary) })
	}
	box.Children(func() {
		ui.Column(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if loading {
				lobeSpin(c, feedback.SpinProps{CustomSize: 16}).Label("图片加载中")
				ui.Text(c, imageLoadingText).FontSize(12).TextColor(th.Muted).SingleLine()
			} else {
				ui.Text(c, imageFailedText).FontSize(12).TextColor(th.Muted).SingleLine()
				ui.Text(c, alt).FontSize(11).TextColor(th.Muted).MaxLines(2)
				ui.Text(c, imageRetryText).FontSize(11).TextColor(th.Muted).SingleLine()
			}
		})
	})
}

// imageHidden 报告一张配图此刻该不该先画占位：只在「默认不加载图片」打开、
// 这张图还没被点开过、也没有已经在缓存里时成立。点过一次的地址记在
// revealedImages 里，之后滚回来照常显示图片。
func (a *app) imageHidden(dest string) bool {
	if !a.imagePrefs.lazyLoad() || a.revealedImages[dest] {
		return false
	}
	return a.images.source(dest) == nil
}

// revealImage 是「默认不加载图片」模式下点占位的结果：记下这张图已经点开，
// 开始下载并让界面重建。图片到位后占位自己换成图片（见 articlePicture）。
func (a *app) revealImage(dest string) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return
	}
	if a.revealedImages == nil {
		a.revealedImages = make(map[string]bool, 1)
	}
	if a.revealedImages[dest] {
		return
	}
	a.revealedImages[dest] = true
	a.prefetchImage(dest, imageCacheTemporary)
	a.publish(func() {})
}

// retryImage 是失败配图占位的点击动作：失败项被自动重试的冷却期与次数上限
// 挡着，显式点击直接复位并重新入队；重复点在缓存项已回到加载态时不会重复
// 下载。重建一次界面，让占位立刻换成加载中的转圈。
func (a *app) retryImage(raw string, cat imageCategory) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !a.images.retry(raw, cat, a.imageFetcher()) {
		return
	}
	a.publish(func() {})
}

// articlePictureLazy 画「默认不加载图片」时的占位：只留位置和一句说明，点
// 一下才开始下载。尺寸已知时（磁盘缓存里有这张图）按真实宽高留白，图片到位
// 后下面的文字不会跳；评论区的高度上限同样按宽度换算，占位与成图边界一致。
// 尺寸未知时用固定高度的空框。
func (a *app) articlePictureLazy(c *ui.Context, dest, alt string, imageMaxH float32) {
	th := markdown.DefaultTheme(c.Theme())
	w, h, _ := a.images.cachedSize(dest, imageCacheTemporary)
	box := ui.Box(c).Radius(4).Background(th.CodeBackground).Center().Clip().
		Cursor(ui.CursorPointer).AlignSelf(ui.Center)
	if w > 0 && h > 0 {
		box.Width(w).MaxWidthPercent(100).AspectRatio(w / h)
		if maxW := imageBoxWidth(w, h, imageMaxH); maxW > 0 {
			box.MaxWidth(maxW)
		}
	} else {
		box.FillWidth().Height(imageUnknownSizePlaceholderH)
	}
	box.Label(alt).Tooltip("点一下加载这张图")
	box.OnClick(func() { a.revealImage(dest) })
	box.Children(func() {
		ui.Column(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, imagePlaceholderIcon).Size(16, 16).TextColor(th.Muted)
			ui.Text(c, imageLazyText).FontSize(12).TextColor(th.Muted).SingleLine()
		})
	})
}

// articleVideoEmbed 把一段站外视频画成封面卡片：封面、一个播放按钮与来源
// 编号。title 是卡片上写的名字（链接文字优先，没有就用来源 · 编号）。点一下
// 打开预览窗口停在这一项，播放交给浏览器——正文里不直接放一个能跳走的外链，
// 免得读帖时误触。
func (a *app) articleVideoEmbed(c *ui.Context, ref mediaRef, title string, pictures []articlePicture, imageMaxH float32) {
	limit := imageVideoCardMaxH
	if imageMaxH > 0 {
		limit = min(limit, imageMaxH)
	}
	card := ui.Box(c).FillWidth().AspectRatio(16.0 / 9).MaxHeight(limit).
		Radius(8).Background(videoCardBG).Clip().Cursor(ui.CursorPointer)
	card.Label("打开视频：" + title).Tooltip("点开预览")
	card.OnClick(func() { a.openImageGroup(pictures, ref.URL) })
	// 「默认不加载图片」时正文里的封面也不下载：卡片只用来源底色，点开预览
	// 之后才取封面（预览是明确的动作，不再省这一张）。
	fetchCover := !a.imagePrefs.lazyLoad() || a.revealedImages[ref.ThumbURL]
	card.Children(func() { a.videoCover(c, ref, title, fetchCover) })
}

// videoCardBG 是视频卡片没有封面时的底色，与预览窗口共用 Lobe 深色
// BgContainer，舞台与卡片不会因为写死近黑而各自偏色。
var videoCardBG = lobeStageBackground

// videoCover 画视频卡片的封面与中央的播放按钮。YouTube 的缩略图能直接由
// 编号拼出来，走图片缓存下载；哔哩哔哩的封面要另调接口，这里只用来源底色
// 加编号。title 是卡片上写的名字，fetchCover 为假时连封面也不下载
// （「默认不加载图片」）。
func (a *app) videoCover(c *ui.Context, ref mediaRef, title string, fetchCover bool) {
	tk := lobetheme.TokensOf(c)
	if fetchCover && ref.ThumbURL != "" {
		if src := a.articleImage(ref.ThumbURL, imageCacheTemporary); src != nil {
			ui.Image(c, src).Fill().Fit(ui.Cover).Label(ref.title())
		}
	}
	ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
		Background(tk.BgMask).Center().Children(func() {
		// 这一列撑满卡片再居中排，标题按卡片宽度决定能否单行放下；不撑满时
		// 列的宽度会被最窄的按钮收窄，稍长的视频编号就被迫截断。
		ui.Column(c).FillWidth().Padding(0, 24).Gap(8).AlignItems(ui.Center).Children(func() {
			play := ui.Box(c).Size(52, 52).Radius(26).
				Background(tk.BgMask.Alpha(0.55)).Center()
			play.Children(func() { ui.Icon(c, playIcon).Size(22, 22).TextColor(tk.White) })
			ui.Text(c, title).FontSize(12).TextColor(tk.White).
				SingleLine().MaxWidthPercent(100)
			ui.Text(c, "点开预览 · 在浏览器中播放").FontSize(11).
				TextColor(tk.White.Alpha(0.72)).SingleLine()
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
func (a *app) articleTable(c *ui.Context, d *parser.Document, i int, size, imageMaxH float32, pictures []articlePicture, mentions bool) {
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
			w := a.articleCellWidth(c, d.Inlines(cell), s, maxW, imageMaxH)
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
						box.Children(func() { a.articleCell(c, d, cell, s, align, pictures, imageMaxH, mentions) })
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
func (a *app) articleCell(c *ui.Context, d *parser.Document, cell int, base articleStyle, align ui.Align, pictures []articlePicture, imageMaxH float32, mentions bool) {
	in := d.Inlines(cell)
	if a.hasTopLevelPicture(in) {
		a.articleMixedInlines(c, in, base, align, pictures, imageMaxH, mentions)
		return
	}
	a.articleTextSegment(c, in, 0, len(in), base, align, mentions)
}

// articleCellWidth 估算一格内容需要的宽度：文字按硬换行拆行后取最宽的一行，
// 配图按已知的自然宽度（还不知道时按列宽上限占位）。
func (a *app) articleCellWidth(c *ui.Context, in []parser.Inline, base articleStyle, maxW, imageMaxH float32) float32 {
	w := float32(0)
	for k := 0; k < len(in); k = in[k].Next {
		if n := &in[k]; n.Kind == parser.Image && !a.isEmojiImage(n.Dest) {
			// 顶层配图独占一行，它比文字宽时整格按它算。
			w = max(w, a.articlePictureWidth(n.Dest, maxW, imageMaxH))
		}
	}
	th := markdown.DefaultTheme(c.Theme())
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}, measure: true}
	r.inlines(c, in, 0, len(in), base)
	r.flush(c)
	return max(w, articleLinesWidth(c, r.measured))
}

// articlePictureWidth 返回配图在表格里需要的宽度：尺寸已知就用它，还没下载
// 或解出来时按列宽上限占位（图片到位后表格重排一次，宽度随之收紧）。评论里
// 再按 imageMaxH 收窄，避免列宽按原图预留、实际图片却窄一截。
func (a *app) articlePictureWidth(dest string, maxW, imageMaxH float32) float32 {
	if src := a.images.source(dest); src != nil {
		if w, h := ui.ImageSize(src); w > 0 {
			if max := imageBoxWidth(w, h, imageMaxH); max > 0 {
				w = min(w, max)
			}
			return min(w, maxW)
		}
	}
	if w, h, ok := a.images.cachedSize(dest, imageCacheTemporary); ok && w > 0 {
		if max := imageBoxWidth(w, h, imageMaxH); max > 0 {
			w = min(w, max)
		}
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
			// 独占一段的媒体链接在正文里是一张卡片或一张图（见 articleBlock），
			// 收组时按同一规则收，张数才与画面里显示的一致。
			if b.Kind == parser.Paragraph {
				if ref, alt, ok := standaloneMediaLink(in); ok {
					if alt == "" {
						alt = ref.title()
					}
					out = append(out, articlePicture{URL: ref.URL, Alt: alt, Kind: ref.Kind})
					continue
				}
			}
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
			r.addTextWithMentions(c, n.Text, st)
		case parser.Mention:
			// Markdown 默认把 @名字 当纯文字；显示层把它画成一块可点开的组件。
			r.addTextWithMentions(c, n.Text, st)
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

// addTextWithMentions 把一段文字收进 RichText：@名字 单独成一块可点击组件，
// 前后文字仍是普通 span。测量宽度时不建元素，链接里的 @ 也不该抢走链接的
// 点击，这两种情况都退回普通文字。
func (r *articleRenderer) addTextWithMentions(c *ui.Context, text string, st articleStyle) {
	if !r.mentions || r.measure || st.inLink || text == "" {
		r.addText(text, st)
		return
	}
	ms := displayMentions(text)
	if len(ms) == 0 {
		r.addText(text, st)
		return
	}
	rs := []rune(text)
	at := 0
	for _, m := range ms {
		if m.Start > at {
			r.addText(string(rs[at:m.Start]), st)
		}
		r.flush(c) // 先收掉前面的文字，保证组件落在正确的阅读顺序上
		r.mentionChip(c, string(rs[m.Start:m.End]), m.Name, st)
		at = m.End
	}
	if at < len(rs) {
		r.addText(string(rs[at:]), st)
	}
}

// mentionChip 画一块 @名字：字号字形跟着正文，额外给浅底、圆角和左右内边距，
// 像编辑器里写完的提及那样成为独立的一块；点一下打开这个用户的资料卡。
func (r *articleRenderer) mentionChip(c *ui.Context, text, name string, st articleStyle) {
	if name == "" {
		r.addText(text, st)
		return
	}
	t := c.Theme()
	sp := r.span(text, st)
	sp.Color = t.Accent
	sp.Background = t.Accent.Mix(t.Background, 0.88)
	sp.Radius = 4
	sp.PaddingX = 3
	sp.PaddingY = 1
	label := "查看 @" + name + " 的资料"
	chip := ui.RichText(c, sp).Cursor(ui.CursorPointer).Label(label).Tooltip(label)
	chip.OnClick(func() { r.a.showUserCard(name) })
	// 行内表情的对象坐标按整段的 rune 数定位，组件里的文字也要算进去。
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
