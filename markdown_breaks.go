package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 一个回车就换行。
//
// CommonMark 把段落里的单个换行当成软换行，排版时并成一行里的空格。
// 2Libra 的帖子与评论都是用户手写的，回车就是他们要的分行。这里在交给
// markdown.View 之前，把每个软换行改写成 CommonMark 的硬换行（行尾两个
// 空格），正文、附言和评论因此都能一个回车换一行。
//
// 改写只发生在软换行上：代码块、行内代码、链接目标这些地方解析器不会
// 给出软换行节点，原文保持原样；已经写成硬换行（两个空格或反斜杠）的
// 行也不会重复处理。

// hardBreakMarkdown 返回把软换行改成硬换行后的 Markdown 源文。解析用的
// 是 Markdown 视图默认的扩展集（见 newMarkdownOptions），保证两边对段落
// 与代码块的划分一致。
func hardBreakMarkdown(src string) string {
	if !strings.ContainsAny(src, "\r\n") {
		return src
	}
	d := parser.Parse(src)
	var breaks []int
	for i := 0; i < d.Len(); i++ {
		in := d.Inlines(i)
		for k := 0; k < len(in); k = in[k].Next {
			if in[k].Kind != parser.SoftBreak {
				continue
			}
			// Start 指向源文里的换行符：\n，或 \r\n 的 \r。两个空格要补在
			// 它前面才构成硬换行；位置对不上就跳过，绝不往正文里乱插字符。
			if at := in[k].Start; at >= 0 && at < len(src) && (src[at] == '\n' || src[at] == '\r') {
				breaks = append(breaks, at)
			}
		}
	}
	if len(breaks) == 0 {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + 2*len(breaks))
	last := 0
	for _, at := range breaks {
		if at < last { // 块按文档顺序遍历，正常不会倒序；防御重复位置
			continue
		}
		b.WriteString(src[last:at])
		b.WriteString("  ")
		last = at
	}
	b.WriteString(src[last:])
	return b.String()
}

// markdownView 是正文、附言与评论共用的 Markdown 入口：先按「一个回车
// 换行」改写源文，再交给 markdown.View 排版并撑满栏宽。
func (a *app) markdownView(c *ui.Context, src string, o *markdown.Options) ui.Element {
	return markdown.View(c, a.hardBreakSource(src), o).FillWidth()
}

// hardBreakSource 缓存改写结果。正文与评论每次重绘都会走到这里，帖子正文
// 动辄几千字，重复解析没有意义。缓存只在 UI 线程读写；超过上限就整体
// 丢弃，避免评论无限滚动时一直占着内存。
func (a *app) hardBreakSource(src string) string {
	if !strings.ContainsAny(src, "\r\n") {
		return src
	}
	if s, ok := a.mdBreaks[src]; ok {
		return s
	}
	out := hardBreakMarkdown(src)
	if a.mdBreaks == nil || len(a.mdBreaks) >= 512 {
		a.mdBreaks = make(map[string]string, 512)
	}
	a.mdBreaks[src] = out
	return out
}
