package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// authorBadges 在通用的用户标识里画佩戴中的社区徽章图标。徽章小图由服务端
// 给出，固有尺寸约 22×22，用在用户名旁的 ≤32px 槽位；佩戴「忽冷忽热」时服务端
// 还会带天气，网页用天气图标 + 温度代替徽章图标，这里保持一致。
//
// 数据只来自已经收到的用户对象（author.equipped_badges 等）：没有就什么都不
// 画，绝不为了补徽章单独请求用户资料或徽章墙。图片和表情一样是站点固定给出
// 的小图，走全局图片缓存的长效那一类；2Libra 的徽章小图是「SVG 包一张位图」，
// 图片缓存会先把内嵌位图抽出来再解码，加载完成后 publish 重绘。高度固定 15，
// 宽度按原图比例，正方形徽章不会被拉成 22×15。最多展示两个，避免评论区头部
// 被撑宽。
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
		src := a.articleImage(iconURL, imageCacheStable)
		if src == nil {
			continue
		}
		img := ui.Image(c, src).Size(15, 15).Fit(ui.Contain)
		if b.Name != "" {
			img.Label(b.Name).Tooltip(b.Name)
		}
		if temperature != "" {
			ui.Text(c, temperature).FontSize(11).TextColor(t.TextMuted)
		}
	}
}

// commentText 渲染评论正文：与帖子正文同一套 Markdown 渲染（标题、列表、
// 引用、代码、链接与配图），字号小一号；:code: 内置表情 / 社区图标与正文
// 一样换成行内表情图片，而不是 [说明] 文字。
func (a *app) commentText(c *ui.Context, text string, emojis map[string]emojiInfo) ui.Element {
	return a.markdownView(c, replaceEmoji(text, emojis, true), a.commentMd)
}
