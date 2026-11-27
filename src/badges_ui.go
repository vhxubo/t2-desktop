package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// authorBadges 在通用的用户标识里画佩戴中的社区徽章图标。徽章小图由服务端
// 给出，固有尺寸约 22×22，用在用户名旁的 ≤32px 槽位；佩戴「忽冷忽热」时服务端
// 还会带天气，网页用天气图标 + 温度代替徽章图标，这里保持一致。佩戴「我来过」
// 时服务端可能带一句状态（如「在搬砖」），画在徽章右侧。
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
		// 「我来过」的状态紧挨徽章右侧，和站内的位置一致。状态只来自当前
		// 这次响应，不在这里缓存或推导。
		if b.Status != nil {
			tip := "「我来过」状态：" + b.Status.Text
			ui.Text(c, b.Status.Text).FontSize(11).TextColor(t.TextMuted).
				SingleLine().Tooltip(tip)
		}
	}
}

/* ------------------------------------------------------------------
   资料头·徽章行
---------------------------------------------------------------- */

// 资料头徽章行里小图的边长。徽章小图固有比例约 22×15，按方槽等比缩放；
// 这一行只画图标，名字与说明交给 tooltip，和列表里的头像、行内表情一样走
// 全局长效图片缓存，下载完成后由缓存 publish 重绘。
const headerBadgeIconSize float32 = 16

// headerBadgeRow 在两行身份信息下面画徽章行：先是当前佩戴的徽章（来自用户
// 对象的 equipped_badges），接着一条竖分割线，再是徽章墙里的全部徽章。
// 分割线只是这个可换行行里的一个元素，不把内容切成左右两栏 —— 徽章多时整行
// 一起往下排，不会出现一侧折成多行、另一侧只有一枚而显得不对称。
//
// 两段各自独立：有徽章就画小图标，读完且确实没有时画一枚淡色虚线的空状态
// tag（「暂未佩戴」/「暂无徽章」）；还没读完就留空，不先把「无」说出去。
// 两侧都有内容时才画分割线，免得留下一条孤立的竖线。
// wornReady / allReady 由调用方按资料与徽章墙的加载结果给出。onWornClick 非空
// 时左侧整段可点开徽章切换弹窗；别人的资料卡传 nil，只展示、不挂事件。
func (a *app) headerBadgeRow(c *ui.Context, equipped []badgeInfo, all []badgeWallEntry,
	wornReady, allReady bool, onWornClick func()) {
	wornContent := len(equipped) > 0 || wornReady
	allContent := len(all) > 0 || allReady
	if !wornContent && !allContent {
		return
	}

	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		if wornContent {
			worn := ui.Row(c).Gap(6).AlignItems(ui.Center)
			if onWornClick != nil {
				tk := lobetheme.TokensOf(c)
				worn.Padding(2, 4).Radius(tk.BorderRadiusSM).Cursor(ui.CursorPointer)
				if worn.Hovered() {
					worn.Background(tk.FillSecondary)
				}
			}
			worn.Children(func() {
				if len(equipped) == 0 {
					headerBadgeEmptyTag(c, "暂未佩戴")
				}
				for _, b := range equipped {
					a.headerBadgeIcon(c, b.IconURL, b.Name,
						headerBadgeDescription(b.Name, b.Code, all))
				}
			})
			if onWornClick != nil {
				worn.Label("切换佩戴徽章")
				if worn.Clicked() {
					onWornClick()
				}
			}
		}
		if wornContent && allContent {
			layout.Divider(c, layout.DividerProps{Vertical: true}).Height(headerBadgeIconSize)
		}
		if allContent {
			if len(all) == 0 {
				headerBadgeEmptyTag(c, "暂无徽章")
			}
			for _, b := range all {
				url := b.IconURL
				if url == "" {
					url = b.IconURLLarge
				}
				a.headerBadgeIcon(c, url, profileBadgeDisplayName(b), b.Description)
			}
		}
	})
}

// headerBadgeEmptyTag 画徽章行里的空状态标签：沿用 Lobe Tag 的灰色 Token、
// 圆角与描边，只把实线换成虚线，保持和资料头其它 Lobe 组件同一套风格。
func headerBadgeEmptyTag(c *ui.Context, label string) {
	tag := display.Tag(c, label, display.TagProps{
		Size: lobetheme.Small, Color: display.TagGray,
		Variant: display.TagOutlined, Round: true,
	})
	tag.BorderStyle(ui.BorderDashed)
}

// headerBadgeDescription 在徽章墙里按名字或机器码找说明。用户对象里的
// 佩戴徽章不带 description，佩戴那一侧借墙里的说明做 tooltip。
func headerBadgeDescription(name, code string, all []badgeWallEntry) string {
	for _, b := range all {
		if (name != "" && b.Name == name) || (code != "" && b.Code == code) {
			return strings.TrimSpace(b.Description)
		}
	}
	return ""
}

// headerBadgeIcon 画一枚徽章小图。无障碍名是徽章名，tooltip 是「名字：说明」，
// 说明缺失时只留名字：悬浮就能知道这枚徽章是什么、做什么用。图片还没下载时
// 不画占位方块，缓存完成后会重绘这一行。
func (a *app) headerBadgeIcon(c *ui.Context, url, name, desc string) {
	src := a.articleImage(url, imageCacheStable)
	if src == nil {
		return
	}
	img := ui.Image(c, src).Size(headerBadgeIconSize, headerBadgeIconSize).Fit(ui.Contain)
	label := strings.TrimSpace(name)
	if label == "" {
		label = "徽章"
	}
	tip := label
	if d := strings.TrimSpace(desc); d != "" {
		tip += "：" + d
	}
	img.Label(label).Tooltip(tip)
}

// badgePickerDialog 是切换佩戴徽章的弹窗：列出徽章墙里已有的徽章，逐枚佩戴
// 或取消佩戴。入口只在个人中心与自己的资料卡的资料头里，弹窗也只画在打开它
// 的那扇窗口；提交、幂等与状态同步都复用 toggleProfileBadge 那套。
func (a *app) badgePickerDialog(c *ui.Context) {
	ps := a.profile
	if ps == nil || !ps.badgePickerOpen {
		return
	}
	open := ps.badgePickerOpen
	feedback.Modal(c, &open, feedback.ModalProps{
		Title: "切换佩戴的徽章", Width: 460, NoFooter: true,
	}, func() {
		tk := lobetheme.TokensOf(c)
		if len(ps.badges) == 0 {
			display.Text(c, "还没有获得任何徽章。", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			return
		}
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			for _, b := range ps.badges {
				a.badgePickerRow(c, ps, b)
			}
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Spacer(c).Grow(1)
			done := action.Button(c, "完成", action.ButtonProps{Size: lobetheme.Small})
			done.TextColor(tk.Primary)
			if done.Clicked() {
				open = false
			}
		})
	})
	ps.badgePickerOpen = open
}

// badgePickerRow 是弹窗里的一枚徽章：小图、名字、说明与佩戴 / 取消佩戴按钮。
// 按钮的无障碍名带上徽章名，测试与读屏都能点到确定的那一枚；提交期间禁用。
func (a *app) badgePickerRow(c *ui.Context, ps *profileState, b badgeWallEntry) {
	name := profileBadgeDisplayName(b)
	row := popupRow(c, false).FillWidth().Padding(8, 10)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			a.profileBadgeIcon(c, b, 28)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, name).FontSize(12).Bold().SingleLine()
				if desc := strings.TrimSpace(b.Description); desc != "" {
					ui.Text(c, desc).FontSize(10).TextColor(lobetheme.TokensOf(c).TextSecondary).MaxLines(2)
				}
			})
			verb := "佩戴"
			if b.Equipped {
				verb = "取消佩戴"
			}
			btn := action.Button(c, verb, action.ButtonProps{Size: lobetheme.Small})
			btn.Label(verb + "「" + name + "」").Shrink(0)
			btn.Disabled(ps.badgeBusyID != "" || b.BadgeID == "")
			if btn.Clicked() {
				a.toggleProfileBadge(b.BadgeID, name, !b.Equipped, "")
			}
		})
	})
}

// commentBodyView 是评论正文的统一渲染入口：详情页、最新评论与通知都走这里，
// 保证同一段 Markdown 在三处的排版、表情短码、@提及、链接与配图完全一致。
// src 是原始 Markdown（或调用方先把裸图片地址补成配图的结果）；emojis 由调用
// 方给：详情页合并了评论自带的表情，列表与通知用全局表情表。inList 为真时
// 用不占可选取层的选项，让点正文落到整行而不是被 Markdown 视图吃掉。
func (a *app) commentBodyView(c *ui.Context, src string, emojis map[string]emojiInfo, inList bool) ui.Element {
	o := a.commentMdOptions(c)
	if inList {
		o = a.commentListMdOptions(c)
	}
	return a.markdownView(c, replaceEmoji(src, emojis, true), o)
}

// commentText 渲染评论正文：与帖子正文同一套 Markdown 渲染（标题、列表、
// 引用、代码、链接与配图），字号小一号；:code: 内置表情 / 社区图标与正文
// 一样换成行内表情图片，而不是 [说明] 文字。
//
// 正文里若含有编辑器插入的 Base64 编码串，就按编码串把正文切开：普通段照旧
// 交给 Markdown，片段画成可点「原文」解码的独立组件（见 base64.go）。绝大多数
// 评论不带片段，这时仍走原来那一条路径，不多包一层 Column。
func (a *app) commentText(c *ui.Context, text, commentID string, emojis map[string]emojiInfo) ui.Element {
	segs := splitBase64Segments(text)
	// 整条评论只有一段编码串时 segs 也只有一个元素，不能只看长度；那一段的
	// token 非空就说明它是编码块，要走下面的组件路径。
	if len(segs) == 1 && segs[0].token == "" {
		return a.commentBodyView(c, segs[0].text, emojis, false)
	}
	return ui.Column(c).FillWidth().Children(func() {
		b64Index := 0
		for _, seg := range segs {
			if seg.token != "" {
				key := base64RevealKey(commentID, b64Index, seg.token)
				a.base64CommentBlock(c, a.detail, seg.token, key)
				b64Index++
				continue
			}
			if strings.TrimSpace(seg.text) == "" {
				continue
			}
			a.commentBodyView(c, seg.text, emojis, false)
		}
	})
}

/* ------------------------------------------------------------------
   「我的」窗口·徽章标签页
---------------------------------------------------------------- */

// 徽章墙的展示尺寸：卡片里的图用 48px 槽位，图标跟着槽位等比缩放；徽章名
// 旁的小图仍然走 authorBadges 的 15px。卡片与说明区定高，说明有无、文案长短
// 都不改变行高，两列网格里的每一枚看起来才是一样大。
const (
	badgeWallIconSize     float32 = 48
	badgeWallIconFallback float32 = 0.72
	badgeWallCardHeight   float32 = 148
	badgeWallDescHeight   float32 = 28
)

// badgeWallOrdered 把佩戴中的徽章排到最前面：两列网格下它们占第一行的位置，
// 想取消佩戴时不用翻遍整面墙。其余徽章按原来的相对顺序照常展示在下面。
func badgeWallOrdered(badges []badgeWallEntry) []badgeWallEntry {
	out := make([]badgeWallEntry, 0, len(badges))
	for _, b := range badges {
		if b.Equipped {
			out = append(out, b)
		}
	}
	for _, b := range badges {
		if !b.Equipped {
			out = append(out, b)
		}
	}
	return out
}

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
		a.smallRefreshButton(c, ps.badgeLoading || ps.badgeBusyID != "", a.reloadProfileBadges)
	})

	switch {
	case ps.badgeLoading && len(ps.badges) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
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
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
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
			for _, b := range badgeWallOrdered(ps.badges) {
				a.profileBadgeCard(c, ps, b)
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

// profileBadgeCard 是一枚徽章的卡片。Lobe 的扁平表面：不画圆角与描边，佩戴中
// 用主色浅底与「佩戴中」标出，不再靠一圈强调色边框。说明区与卡片定高，所有
// 卡片一般大小。提交期间按钮显示「正在提交…」并禁用，避免连点重复提交。
func (a *app) profileBadgeCard(c *ui.Context, ps *profileState, b badgeWallEntry) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	name := profileBadgeDisplayName(b)
	busy := ps.badgeBusyID != "" && ps.badgeBusyID == b.BadgeID

	// Lobe 的 Filled 表面只在 Clickable 时自己处理悬停；这里卡片不可点（动作
	// 在里面的按钮上），所以自己按 Hovered 换个稍深的底色，避免把整张卡片
	// 变成按钮。佩戴中的卡片用主色浅底与对应的悬停色。
	card := layout.Block(c.Key("badge-card-"+b.BadgeID), layout.BlockProps{Variant: layout.BlockFilled}).Radius(0)
	rest, hover := tk.FillTertiary, tk.FillSecondary
	if b.Equipped {
		rest, hover = tk.PrimaryBg, tk.PrimaryBgHover
	}
	if card.Hovered() {
		card.Background(hover)
	} else {
		card.Background(rest)
	}
	card.FillWidth().Height(badgeWallCardHeight).Padding(12, 12).Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			a.profileBadgeIcon(c, b, badgeWallIconSize)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, name).FontSize(12).Bold().SingleLine().
						Grow(1).MinWidth(0)
					if b.Equipped {
						ui.Text(c, "佩戴中").FontSize(9).TextColor(tk.Primary).Shrink(0)
					}
				})
				if meta := profileBadgeMeta(b); meta != "" {
					ui.Text(c, meta).FontSize(9).TextColor(t.TextMuted).SingleLine()
				}
			})
		})
		// 说明区留两行的位置：有说明与没说明的卡片内部布局一致。
		ui.Box(c).FillWidth().Height(badgeWallDescHeight).Children(func() {
			if desc := strings.TrimSpace(b.Description); desc != "" {
				ui.Text(c, desc).FontSize(10).TextColor(t.TextMuted).MaxLines(2)
			}
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			verb := "佩戴"
			if b.Equipped {
				verb = "取消佩戴"
			}
			label := verb
			if busy {
				label = "正在提交…"
			}
			button := action.Button(c, label, action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Filled,
			})
			// 每张卡片的按钮文字都一样，用无障碍名把徽章名字带上：读屏与
			// 测试都能点到确定的那一枚。
			button.Label(verb + "「" + name + "」")
			button.Disabled(ps.badgeBusyID != "" || b.BadgeID == "")
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
// 七成，避免把 22×15 的小图放大到 48px 糊成一团。图片还在下载时先转圈，
// 没登记过加载或加载失败时仍画名字首字，加载完成后由图片缓存重绘。
func (a *app) profileBadgeIcon(c *ui.Context, b badgeWallEntry, size float32) {
	t := c.Theme()
	url, iconSize := b.IconURLLarge, size
	if url == "" {
		url, iconSize = b.IconURL, size*badgeWallIconFallback
	}
	box := ui.Box(c).Size(size, size).Radius(0).Shrink(0).Center().
		Background(lobetheme.TokensOf(c).FillSecondary)
	box.Children(func() {
		if src := a.articleImage(url, imageCacheStable); src != nil {
			ui.Image(c, src).Size(iconSize, iconSize).Fit(ui.Contain)
			return
		}
		if a.images.state(url) == imageLoading {
			lobeSpin(c, feedback.SpinProps{CustomSize: size * 0.4}).Label("徽章图标加载中")
			return
		}
		ui.Text(c, avatarInitials(profileBadgeDisplayName(b))).
			FontSize(size * 0.38).Bold().TextColor(t.TextMuted).SingleLine()
	})
}

/* ------------------------------------------------------------------
   资料卡·ta的徽章
--------------------------------------------------------------- */

// userCardBadgesBody 是资料卡的「ta的徽章」标签页：顶部是数量与刷新，下面是
// 两列只读徽章卡片。这里不能佩戴别人的徽章，所以卡片上没有按钮；佩戴状态由
// /badges/equipped 标出来，加载失败时仍展示徽章墙本身。
func (a *app) userCardBadgesBody(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	st := &cs.badges
	total, equipped := profileBadgeCounts(st.entries)

	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Text(c, "ta的徽章").FontSize(13).Bold()
		if st.loaded {
			ui.Textf(c, "共 %d 枚 · 佩戴中 %d 枚", total, equipped).
				FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		a.smallRefreshButton(c, st.loading, a.reloadUserCardBadges)
	})

	switch {
	case st.loading && len(st.entries) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			ui.Text(c, "正在读取ta的徽章…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, st.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadUserCardBadges()
			}
		})
		return
	case !st.loaded:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			ui.Text(c, "正在读取ta的徽章…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case len(st.entries) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "这位用户还没有获得任何徽章。").FontSize(13).TextColor(t.TextMuted)
		})
		return
	}

	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Grid(c).FillWidth().Columns(2).Gap(10).Children(func() {
			for _, b := range badgeWallOrdered(st.entries) {
				a.userCardBadgeCard(c, b)
			}
		})
	})
}

// userCardBadgeCard 是一枚他人徽章的卡片：图标、名字、等级 / 持有比例、说明
// 与获得日期。和「我的」徽章页用同一套 Lobe 扁平卡片：没有描边，佩戴中用主色
// 浅底与「佩戴中」标出，尺寸也一致；这里只读，没有佩戴按钮。
func (a *app) userCardBadgeCard(c *ui.Context, b badgeWallEntry) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	name := profileBadgeDisplayName(b)

	// Lobe 的 Filled 表面只在 Clickable 时自己处理悬停；这里卡片不可点（动作
	// 在里面的按钮上），所以自己按 Hovered 换个稍深的底色，避免把整张卡片
	// 变成按钮。佩戴中的卡片用主色浅底与对应的悬停色。
	card := layout.Block(c.Key("badge-card-"+b.BadgeID), layout.BlockProps{Variant: layout.BlockFilled}).Radius(0)
	rest, hover := tk.FillTertiary, tk.FillSecondary
	if b.Equipped {
		rest, hover = tk.PrimaryBg, tk.PrimaryBgHover
	}
	if card.Hovered() {
		card.Background(hover)
	} else {
		card.Background(rest)
	}
	card.FillWidth().Height(badgeWallCardHeight).Padding(12, 12).Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			a.profileBadgeIcon(c, b, badgeWallIconSize)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, name).FontSize(12).Bold().SingleLine().
						Grow(1).MinWidth(0).Tooltip(name)
					if b.Equipped {
						ui.Text(c, "佩戴中").FontSize(9).TextColor(tk.Primary).Shrink(0)
					}
				})
				if meta := profileBadgeMeta(b); meta != "" {
					ui.Text(c, meta).FontSize(9).TextColor(t.TextMuted).SingleLine()
				}
			})
		})
		ui.Box(c).FillWidth().Height(badgeWallDescHeight).Children(func() {
			if desc := strings.TrimSpace(b.Description); desc != "" {
				ui.Text(c, desc).FontSize(10).TextColor(t.TextMuted).MaxLines(2).Tooltip(desc)
			}
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Spacer(c).Grow(1)
			if achieved := profileBadgeAchievedText(b.AchievedAt); achieved != "" {
				ui.Text(c, achieved).FontSize(9).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
}
