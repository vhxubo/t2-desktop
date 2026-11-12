package main

import (
	"fmt"
	"strings"

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

/* ------------------------------------------------------------------
   「我的」窗口·徽章标签页
---------------------------------------------------------------- */

// 徽章墙的展示尺寸：卡片里的图用 48px 槽位，图标跟着槽位等比缩放；徽章名
// 旁的小图仍然走 authorBadges 的 15px。
const (
	badgeWallIconSize     float32 = 48
	badgeWallIconFallback float32 = 0.72
)

// profileBadgesBody 是「徽章」标签页：顶部是数量与刷新，下面是两列徽章卡片。
// 每张卡片有名字、说明、等级 / 持有比例与获得日期，佩戴中再标一枚胶囊；点
// 「佩戴」/「取消佩戴」提交切换。加载、失败与空状态各自说明页面为什么是空的。
func (a *app) profileBadgesBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	total, equipped := profileBadgeCounts(ps.badges)

	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Text(c, "我的徽章").FontSize(13).Bold()
		if ps.badgeLoaded {
			ui.Textf(c, "共 %d 枚 · 佩戴中 %d 枚", total, equipped).
				FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", ps.badgeLoading || ps.badgeBusyID != "",
			a.reloadProfileBadges)
	})
	ui.Text(c, "点「佩戴」切换徽章；首次佩戴免费，之后每次佩戴可能扣除金币。").
		FontSize(10).TextColor(t.TextMuted).MaxLines(2)

	switch {
	case ps.badgeLoading && len(ps.badges) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在读取我的徽章…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case ps.badgeErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, ps.badgeErr).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadProfileBadges()
			}
		})
		return
	case !ps.badgeLoaded:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在读取我的徽章…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case len(ps.badges) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还没有获得任何徽章。").FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, "参与社区活动并达成条件后，徽章会出现在这里。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
		return
	}

	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Grid(c).FillWidth().Columns(2).Gap(10).Children(func() {
			for i := range ps.badges {
				a.profileBadgeCard(c, ps, ps.badges[i])
			}
		})
	})
}

// profileBadgeCounts 数徽章总数与佩戴中的数量。
func profileBadgeCounts(badges []badgeWallEntry) (total, equipped int) {
	for _, b := range badges {
		if b.Equipped {
			equipped++
		}
	}
	return len(badges), equipped
}

// profileBadgeCard 是一枚徽章的卡片。佩戴中的卡片用强调色描边并标「佩戴中」；
// 提交期间按钮显示「正在提交…」并禁用，避免连点重复扣币。
func (a *app) profileBadgeCard(c *ui.Context, ps *profileState, b badgeWallEntry) {
	t := c.Theme()
	name := profileBadgeDisplayName(b)
	busy := ps.badgeBusyID != "" && ps.badgeBusyID == b.BadgeID
	border := t.Border
	if b.Equipped {
		border = t.Accent
	}

	ui.Column(c).FillWidth().Padding(10, 12).Gap(6).Radius(10).
		Background(t.Surface).Border(1, border).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			a.profileBadgeIcon(c, b, badgeWallIconSize)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, name).FontSize(12).Bold().SingleLine().
						Grow(1).MinWidth(0).Tooltip(name)
					if b.Equipped {
						ui.Text(c, "佩戴中").FontSize(9).TextColor(t.Accent).Shrink(0)
					}
				})
				if meta := profileBadgeMeta(b); meta != "" {
					ui.Text(c, meta).FontSize(9).TextColor(t.TextMuted).SingleLine()
				}
			})
		})
		if desc := strings.TrimSpace(b.Description); desc != "" {
			ui.Text(c, desc).FontSize(10).TextColor(t.TextMuted).MaxLines(2).Tooltip(desc)
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			action := "佩戴"
			if b.Equipped {
				action = "取消佩戴"
			}
			label := action
			if busy {
				label = "正在提交…"
			}
			button := ui.Button(c, label).FontSize(10)
			// 每张卡片的按钮文字都一样，用无障碍名把徽章名字带上：读屏与
			// 测试都能点到确定的那一枚。
			button.Label(action + "「" + name + "」")
			button.Disabled(ps.badgeBusyID != "" || b.BadgeID == "")
			if b.BadgeID == "" {
				button.Tooltip("这枚徽章缺少佩戴所需的标识，暂时无法切换")
			} else if b.Equipped {
				button.Tooltip("取消佩戴「" + name + "」")
			} else {
				button.Tooltip("佩戴「" + name + "」；首次免费，之后可能扣除金币")
			}
			if button.Clicked() {
				a.toggleProfileBadge(b.BadgeID, name, !b.Equipped, "")
			}
			ui.Spacer(c).Grow(1)
			if achieved := profileBadgeAchievedText(b.AchievedAt); achieved != "" {
				ui.Text(c, achieved).FontSize(9).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
}

// profileBadgeIcon 画徽章墙里的大图标。大图缺失时用小图，但缩小到槽位的
// 七成，避免把 22×15 的小图放大到 48px 糊成一团。图片还没到就先画名字首字，
// 加载完成后由图片缓存重绘。
func (a *app) profileBadgeIcon(c *ui.Context, b badgeWallEntry, size float32) {
	t := c.Theme()
	url, iconSize := b.IconURLLarge, size
	if url == "" {
		url, iconSize = b.IconURL, size*badgeWallIconFallback
	}
	box := ui.Box(c).Size(size, size).Radius(8).Shrink(0).Center().
		Background(t.SurfaceHover)
	box.Children(func() {
		if src := a.articleImage(url, imageCacheStable); src != nil {
			ui.Image(c, src).Size(iconSize, iconSize).Fit(ui.Contain)
			return
		}
		ui.Text(c, avatarInitials(profileBadgeDisplayName(b))).
			FontSize(size * 0.38).Bold().TextColor(t.TextMuted).SingleLine()
	})
}
