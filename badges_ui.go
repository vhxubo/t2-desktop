package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// authorBadges 在昵称后画佩戴中的社区徽章图标。徽章小图由服务端给出，
// 固有尺寸 22×15，用在用户名旁的 ≤32px 槽位；佩戴「忽冷忽热」时服务端
// 还会带天气，网页用天气图标 + 温度代替徽章图标，这里保持一致。
//
// 图片走详情页已有的 articleImages 缓存异步加载，加载完成后 publish 重绘。
// 最多展示两个，避免评论区头部被撑宽。
func (a *app) authorBadges(c *ui.Context, badges []badgeInfo) {
	if len(badges) == 0 {
		return
	}
	t := c.Theme()
	for i, b := range badges {
		if i >= 2 {
			break
		}
		iconURL := b.IconURL
		temperature := ""
		if b.Weather != nil {
			iconURL = weatherIconURL(b.Weather.SymbolCode)
			temperature = fmt.Sprintf("%.0f°", b.Weather.Temperature)
		}
		if iconURL == "" {
			continue
		}
		src := a.articleImage(iconURL)
		if src == nil {
			continue
		}
		img := ui.Image(c, src).Height(15).AspectRatio(22.0 / 15.0)
		if b.Name != "" {
			img.Label(b.Name).Tooltip(b.Name)
		}
		if temperature != "" {
			ui.Text(c, temperature).FontSize(11).TextColor(t.TextMuted)
		}
	}
}

// commentText 渲染评论正文：纯文本，但把 :code: 内置表情 / 社区图标画成
// 与正文同高的行内图片。没有可映射的短码时仍然返回普通 Text，保持评论
// 原有的简洁与可选性。
func (a *app) commentText(c *ui.Context, text string, emojis map[string]emojiInfo) ui.Element {
	const size = float32(13)
	if len(emojis) == 0 || !shortCode.MatchString(text) {
		return ui.Text(c, text).FontSize(size)
	}
	th := markdown.DefaultTheme(c.Theme())
	base := articleStyle{size: size, color: th.Text}
	r := &articleRenderer{a: a, th: th, objects: map[int]ui.ImageSource{}}
	matches := shortCode.FindAllStringIndex(text, -1)
	last := 0
	found := false
	for _, m := range matches {
		code := text[m[0]:m[1]]
		e, ok := emojis[code]
		if !ok {
			continue
		}
		if m[0] > last {
			r.addText(text[last:m[0]], base)
		}
		name := e.Name
		if name == "" {
			name = strings.Trim(code, ":")
		}
		r.addImage(c, e.ImgURL, name, base)
		last = m[1]
		found = true
	}
	if !found {
		return ui.Text(c, text).FontSize(size)
	}
	if last < len(text) {
		r.addText(text[last:], base)
	}
	root := ui.RichText(c).FontSize(size).FillWidth()
	root.Children(func() { r.flush(c) })
	if len(r.objects) > 0 {
		root.DrawObjects(func(p *ui.Painter, at int, rect ui.Rect) {
			if src, ok := r.objects[at]; ok {
				p.Image(src, rect, ui.Contain)
			}
		})
	}
	return root
}
