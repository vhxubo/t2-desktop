package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 「用户资料」窗口：点任何一处统一用户标识（头像 + 昵称）打开，是一个独立
// 的 mygo 窗口，而不是主窗口里的一层遮罩 —— 资料、统计与两个列表都要滚动，
// 塞进主窗口的弹窗里只会互相挤。
//
// 版式：
//
//	┌────────────────────────────────────────────┐
//	│  (人) 用户资料                  – 关闭      │ 32 DIP 标题栏
//	│  ────────────────────────────────────────  │
//	│  (头像)  昵称 [Lv.13] [PRO] [徽章]  [关注他] │
//	│          @username · 注册 2024-05-01 · 512 天│
//	│  ────────────────────────────────────────  │
//	│  个人简介：…                                │
//	│  ────────────────────────────────────────  │
//	│  主题数 | 回复数 | 粉丝 | 关注               │
//	│  ────────────────────────────────────────  │
//	│  [他发布的主题] [他的最新回复]                │
//	│  · 标题 ……………                      2 天前  │
//	└────────────────────────────────────────────┘
//
// 顶部不再是一条只有关闭按钮的渐变横幅：资料卡的头部信息本来就少，整条
// 横幅只剩大片空白。现在用和其他窗口相同的 32 DIP 自绘标题栏，头像、昵称
// 与关注按钮并成一行，资料与列表因此整体上移。
//
// 数据、状态与请求在 user_card.go；这里只负责窗口的开关与界面。

/* ------------------------------------------------------------------
   入口、显示与关闭
--------------------------------------------------------------- */

// showUserCard 是资料卡的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；已经开着同一个人的资料卡就只带到前台，不会开出第二个。
func (a *app) showUserCard(username string) {
	username = strings.TrimSpace(username)
	if username == "" || username == "匿名" {
		return // 匿名帖没有可展示的用户
	}
	a.publish(func() { a.showUserCardWindow(username) })
}

// showUserCardWindow 在主线程上真正打开窗口。换人时复用同一个窗口、整份
// 状态重建，避免每点一个人就多一个窗口。
func (a *app) showUserCardWindow(username string) {
	if username == "" || username == "匿名" {
		return
	}
	// 同一份状态里读的人没变、数据也在：只把窗口带到前台。
	sameUser := a.userCard != nil && a.userCard.username == username
	if !sameUser {
		a.userCard = newUserCardState(username)
	}
	a.ensureUserCardLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 userCardWindowView。
	if a.win == nil {
		return
	}
	if win := a.userCardWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setUserCardWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "用户资料",
		Width:             userCardWindowWidth,
		Height:            userCardWindowHeight,
		MinWidth:          userCardMinWidth,
		MinHeight:         userCardMinHeight,
		StateKey:          "user-card",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知、我的一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.userCardWindowView),
	})
	a.userCardWin = win
	win.OnShow(func() { a.setUserCardWindowShown(true) })
	win.OnHide(func() { a.setUserCardWindowShown(false) })
	win.OnClosed(func() {
		a.userCardWin = nil
		a.userCard = nil // 关掉就丢掉这份资料，下次打开重新拉
		a.setUserCardWindowShown(false)
	})
	a.setUserCardWindowShown(true)
}

// userCardWindowVisible 报告资料卡窗口此刻是否在屏幕上。
func (a *app) userCardWindowVisible() bool { return a.userCardShown }

// setUserCardWindowShown 记下显示状态并让主窗口重画一次。只在主线程调用。
func (a *app) setUserCardWindowShown(shown bool) {
	if a.userCardShown == shown {
		return
	}
	a.userCardShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closeUserCardWindow 关掉窗口并丢掉状态。只在主线程调用。
func (a *app) closeUserCardWindow() {
	win := a.userCardWin
	a.userCard = nil
	a.setUserCardWindowShown(false)
	if win != nil {
		a.userCardWin = nil
		win.Close()
	}
}

// redrawUserCardWindow 让窗口按最新状态重建。只在主线程调用。
func (a *app) redrawUserCardWindow() {
	if win := a.userCardWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   窗口界面
--------------------------------------------------------------- */

// userCardWindowView 是资料卡窗口的 view。窗口没有系统标题栏：顶部是自绘
// 标题栏（拖动窗口、最小化与关闭），下面直接是资料主体。
func (a *app) userCardWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 列表行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 关注 / 取关的结果以 toast 显示在这个窗口里，不退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastUserCard))
	// Esc 收起窗口：随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeUserCardWindow)

	cs := a.userCard
	if cs == nil {
		// 窗口正在关闭（状态已丢弃）：画一帧空壳就好，不要在渲染里把状态
		// 重新建出来 —— 那会让「关掉就丢掉这份资料」失效。
		ui.Column(c).Fill().Children(func() { a.userCardTitleBar(c) })
		return
	}

	ui.Column(c).Fill().Children(func() {
		a.userCardTitleBar(c)
		ui.Divider(c)
		a.userCardHeader(c, cs)
		ui.Divider(c)
		a.userCardBio(c, cs)
		ui.Divider(c)
		a.userCardStats(c, cs)
		ui.Divider(c)
		ui.Row(c).FillWidth().Padding(6, 16).Children(func() {
			ui.Tabs(c, &cs.tab, userCardTabLabels...)
		})
		ui.Divider(c)
		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16).Children(func() {
			switch cs.tab {
			case userCardTabReplies:
				a.userCardListBody(c, cs, userCardReplies)
			case userCardTabAttrs:
				a.userCardAttrsBody(c, cs)
			default:
				a.userCardListBody(c, cs, userCardTopics)
			}
		})
	})
	a.ensureUserCardLoaded()
}

// userCardTitleBar 是自绘标题栏：左边是人形图标与「用户资料」，右边是
// 最小化与关闭，画法与主窗口、通知、我的三个窗口一致。它同时是拖动窗口的
// 区域。用户名只在下面的资料头里写一次，标题栏不再重复。
func (a *app) userCardTitleBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
		Background(t.Surface).DragWindow().Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
			AlignItems(ui.Center).Children(func() {
			ui.Icon(c, profileIcon).Size(16, 16).TextColor(t.Accent)
			ui.Text(c, "用户资料").FontSize(13).Bold().SingleLine()
		})
		a.titleButton(c, titleMinimize, "最小化", func() {
			if win := a.userCardWin; win != nil {
				win.Minimize()
			}
		})
		a.titleButton(c, titleClose, "关闭", a.closeUserCardWindow)
	})
}

// userCardHeader 是资料头：头像、昵称、等级 / PRO / 徽章、身份信息与关注
// 按钮。关注按钮与身份信息同一行，不再单独占一行；资料头是整张卡里唯一
// 需要突出的地方，其余区块都贴着它往下排。
func (a *app) userCardHeader(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	u := cs.user
	ui.Row(c).FillWidth().Padding(12, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		avatarView(c, cs.username, a.avatarBitmap(u.Avatar), userCardAvatar)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, cs.title()).FontSize(16).Bold().SingleLine()
				if u.Level > 0 {
					userCardChip(c, fmt.Sprintf("Lv.%d", u.Level), t.TextMuted)
				}
				if u.IsPro {
					userCardChip(c, "PRO", t.Accent)
				}
			})
			// 第二行：@用户名 + 注册时间 + 佩戴中的徽章。徽章名字来自接口，
			// 站点有什么就显示什么（「守护者」这类徽章会原样出现）。行内
			// 放不下时自动折行，不会把关注按钮挤出窗口。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				if meta := userCardHeaderMeta(cs); meta != "" {
					ui.Text(c, meta).FontSize(12).
						TextColor(t.TextMuted).MaxLines(2)
				}
				for _, b := range u.Badges {
					if b.Name == "" {
						continue
					}
					userCardChip(c, b.Name, t.Warning)
				}
			})
		})
		a.userCardFollowButton(c, cs)
	})
}

// userCardHeaderMeta 是昵称下面那行身份信息：@用户名与注册时间。注册的
// 说法沿用「我的」窗口（注册日期 + 天数）；资料还没到时不编一个日期。
func userCardHeaderMeta(cs *userCardState) string {
	parts := make([]string, 0, 2)
	if cs.username != "" {
		parts = append(parts, "@"+cs.username)
	}
	if reg := profileRegisteredText(cs.user.CreatedAt); reg != "" {
		parts = append(parts, reg)
	}
	return strings.Join(parts, " · ")
}

// userCardChip 是昵称旁的短标签：PRO、徽章名与统计数字都用它，靠颜色区分。
func userCardChip(c *ui.Context, label string, tint ui.Color) {
	t := c.Theme()
	ui.Box(c).Shrink(0).Padding(1, 6).Radius(4).Background(t.Surface).
		Border(1, t.Border).Children(func() {
		ui.Text(c, label).FontSize(10).TextColor(tint).SingleLine()
	})
}

// userCardFollowButton 画关注按钮：未关注是主按钮「关注他」，已关注是低
// 强调的描边按钮「已关注」，点一下切换。自己的资料卡不显示。它画在资料头
// 的右侧，与昵称同一行。
func (a *app) userCardFollowButton(c *ui.Context, cs *userCardState) {
	if !a.userCardCanFollow() {
		return
	}
	t := c.Theme()
	following := a.userCardFollowing()
	label := "关注他"
	if following {
		label = "已关注"
	}
	var btn ui.Element
	if following {
		btn = ui.Button(c, label).
			Background(ui.Transparent).
			Border(1, t.Border).
			TextColor(t.TextMuted)
	} else {
		btn = ui.PrimaryButton(c, label)
	}
	btn.FontSize(12).Padding(4, 12).Radius(6).Disabled(cs.follow.busy)
	if following {
		btn.Tooltip("点击取消关注")
	} else {
		btn.Tooltip("关注这位用户")
	}
	if btn.Clicked() {
		a.toggleUserCardFollow(!following)
	}
}

// userCardBio 是「个人简介」一块：正文来自 GET /v1/users/:userName/about，
// 按 Markdown 渲染（链接、列表与表情都走正文同一套渲染），{{KEY}} 统计
// 占位符换成该用户的实际数字。介绍可能很长，超过一块的高度就在块内滚动，
// 下面的统计、标签页与列表不会被挤出窗口。
func (a *app) userCardBio(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	// MinHeight(0)：窗口缩到最小时先压这一块（里面的介绍框会跟着缩短），
	// 否则内容会把统计、标签页与列表顶出窗口。
	ui.Column(c).FillWidth().MinHeight(0).Padding(10, 16).Gap(4).Children(func() {
		ui.Text(c, "个人简介").FontSize(11).TextColor(t.TextMuted).SingleLine()
		switch {
		case cs.aboutErr != "":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "介绍暂时不可用："+cs.aboutErr).FontSize(userCardBioFontSize).
					TextColor(t.Danger).MaxLines(2).Grow(1)
				a.smallAction(c, "重试", cs.aboutLoading, a.loadUserCardAbout)
			})
		case !cs.aboutLoaded || cs.aboutLoading:
			ui.Text(c, "正在读取个人介绍…").FontSize(userCardBioFontSize).TextColor(t.Text)
		case strings.TrimSpace(cs.about.Content) == "":
			// 没填写和「仅自己可见」都是空正文，但原因不同，不能混为一谈。
			text := "这位用户还没有填写个人介绍。"
			if cs.about.PublicKnown && !cs.about.IsPublic {
				text = "这位用户的个人介绍仅自己可见。"
			}
			ui.Text(c, text).FontSize(userCardBioFontSize).TextColor(t.Text)
		default:
			ui.Scroll(c).FillWidth().MaxHeight(userCardBioContentHeight(c)).Children(func() {
				// 介绍自己的 emojiMap 可能带社区图标；并到全局表情表后一起
				// 渲染，规则与帖子正文相同。
				emojis := mergeEmojiMaps(a.emojis, cs.about.Emojis)
				src := replaceEmoji(expandAboutStats(cs.about.Content, cs.about.Stats), emojis, true)
				a.markdownView(c, src, a.bioMd)
			})
			if meta := userCardAboutMeta(cs.about); meta != "" {
				ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		}
	})
}

// userCardBioContentHeight 是介绍内容区当前能给的高度：窗口总高减去其余
// 区块（标题栏、资料头、统计、标签页、列表首行与分隔线）大约要占的高度，
// 再限制在 [userCardBioMinHeight, userCardBioMaxHeight] 之间。窗口被缩小
// 时介绍先让位；常量只是估计，bio 块的 MinHeight(0) 保证估错时也不会把
// 列表顶出窗口。
func userCardBioContentHeight(c *ui.Context) float32 {
	_, h := c.Size()
	height := h - userCardBioReserved
	if height > userCardBioMaxHeight {
		return userCardBioMaxHeight
	}
	if height < userCardBioMinHeight {
		return userCardBioMinHeight
	}
	return height
}

// userCardAboutMeta 是介绍底下的状态行：是否公开，以及最后更新时间（站点
// 按本地时间显示）。响应没给的信息不编。
func userCardAboutMeta(about userAbout) string {
	parts := make([]string, 0, 2)
	if about.PublicKnown {
		if about.IsPublic {
			parts = append(parts, "介绍公开")
		} else {
			parts = append(parts, "介绍仅自己可见")
		}
	}
	if t, ok := profileTime(about.UpdatedAt); ok {
		parts = append(parts, "更新于 "+t.Local().Format("2006-01-02 15:04"))
	}
	return strings.Join(parts, " · ")
}

// userCardStats 是统计条：主题数、回复数、粉丝、关注。数字都来自接口，
// 列表还没拉到时显示占位而不是 0。
func (a *app) userCardStats(c *ui.Context, cs *userCardState) {
	known := cs.profileLoaded
	ui.Row(c).FillWidth().Padding(10, 16).Gap(8).Wrap().Children(func() {
		userCardStat(c, "主题数", userCardCountText(cs.topics.loaded, cs.topics.total))
		userCardStat(c, "回复数", userCardCountText(cs.replies.loaded, cs.replies.total))
		userCardStat(c, "粉丝", userCardCountText(known, cs.user.Followers))
		userCardStat(c, "关注", userCardCountText(known, cs.user.Following))
	})
}

// userCardCountText 在数据还没到或接口没给值时显示占位，而不是一个假的 0。
func userCardCountText(known bool, n int) string {
	if !known {
		return "—"
	}
	return coinThousands(n)
}

func userCardStat(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Box(c).Grow(1).MinWidth(0).Padding(6, 8).Radius(8).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, value).FontSize(14).Bold().SingleLine()
			ui.Text(c, label).FontSize(10).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// userCardListBody 画一个标签页的列表：加载中、失败、空与正常四种状态，
// 正常时每行可以点开正文。
func (a *app) userCardListBody(c *ui.Context, cs *userCardState, kind userCardListKind) {
	t := c.Theme()
	l := cs.list(kind)
	switch {
	case l.loading && len(l.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(20, 20)
			ui.Text(c, "正在加载…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case l.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(13).Bold()
			ui.Text(c, l.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadUserCardList(kind, 1)
			}
		})
		return
	case l.loaded && len(l.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			if kind == userCardReplies {
				ui.Text(c, "这位用户还没有发表过回复。").FontSize(12).TextColor(t.TextMuted)
				return
			}
			ui.Text(c, "这位用户还没有发布过主题。").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}

	items := l.items
	l.list.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &l.list, len(items), func(i int) {
		a.userCardRow(c, items[i], kind)
	}).Grow(1).MinHeight(0).Gap(8)
	a.userCardMore(c, cs, kind)
}

// userCardMore 是列表底部的续拉：还有下一页时给按钮，续拉失败时给出原因
// 并可重试，已有内容保持不动。
func (a *app) userCardMore(c *ui.Context, cs *userCardState, kind userCardListKind) {
	t := c.Theme()
	l := cs.list(kind)
	switch {
	case l.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+l.moreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadUserCardList(kind, l.page) })
		})
	case l.loading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	case !l.noMore:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "加载更多").Clicked() {
				a.loadUserCardList(kind, l.page+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

// userCardRow 是一行内容：主题行显示标题与元信息，回复行显示正文摘要与
// 它所属的帖子；右侧是时间。点一下回到主窗口的正文页。
func (a *app) userCardRow(c *ui.Context, it Item, kind userCardListKind) {
	t := c.Theme()
	// 主题行跟着本地已读状态把标题置灰；回复行表示的是评论内容，不跟帖子
	// 的已读走（与 rows.go 的富行同一规则）。
	titleColor := postTitleColor(t, kind == userCardTopics && a.isPostRead(it.ShortID))
	row := ui.ButtonBase(c).FillWidth().Padding(8, 10).Radius(8).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "· "+it.Title).FontSize(12).
					MaxLines(2).TextColor(titleColor)
				if meta := userCardRowMeta(it, kind); meta != "" {
					ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).MaxLines(1)
				}
			})
			if when := userCardWhen(it); when != "" {
				ui.Text(c, when).FontSize(11).Shrink(0).TextColor(t.TextMuted)
			}
		})
	})
	if it.ShortID != "" {
		row.Tooltip("打开正文")
		row.OnClick(func() { a.openUserCardDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.openExternal(it.URL) })
	}
}

// userCardRowMeta 是行的次行：主题行给节点与回复 / 浏览数，回复行给它所属
// 的帖子与楼层。
func userCardRowMeta(it Item, kind userCardListKind) string {
	if kind == userCardReplies {
		parts := make([]string, 0, 3)
		if x := it.Extra; x != nil {
			if x.PostTitle != "" {
				parts = append(parts, "《"+x.PostTitle+"》")
			}
			if x.Floor > 0 {
				parts = append(parts, fmt.Sprintf("%d 楼", x.Floor))
			}
		}
		return strings.Join(parts, " · ")
	}
	parts := make([]string, 0, 2)
	if prefix := strings.TrimSuffix(it.SubtitlePrefix, " · "); prefix != "" {
		parts = append(parts, prefix)
	}
	// 帖子行同样用新的时效字段：置顶、最高楼层、回复数（只写数字，不写
	// 「N 回复」）。手工构造的 Item 没有 Stats 时退回旧的 Meta 摘要。
	if it.Stats != nil {
		if it.Pinned {
			parts = append(parts, "置顶")
		}
		if it.Stats.MaxFloor > 0 {
			parts = append(parts, fmt.Sprintf("%d 楼", it.Stats.MaxFloor))
		}
		parts = append(parts, fmt.Sprintf("%d", it.Stats.Replies))
	} else if it.Meta != "" {
		parts = append(parts, it.Meta)
	}
	return strings.Join(parts, " · ")
}

// userCardWhen 是行右侧的时间：Item 把它放在 SubtitleSuffix 里（形如
// 「 · 2 天前」），这里去掉前导分隔符。
func userCardWhen(it Item) string {
	return strings.TrimPrefix(it.SubtitleSuffix, " · ")
}

// openUserCardDetail 从资料卡打开一篇帖子或一条回复：把主窗口带到前台并换到
// 详情页，资料卡本身留着不关 —— 读完后还能回到原来的列表。评论行会带上
// CommentID，正文里据此定位楼层。
func (a *app) openUserCardDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(it)
	a.showMainWindow()
}
