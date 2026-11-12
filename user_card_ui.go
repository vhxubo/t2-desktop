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
//	│  横幅（渐变，可拖动窗口）            [x] 关闭 │
//	│                                            │
//	│  (头像)  昵称                                │
//	│          @username  [PRO] [徽章]            │
//	│                                            │
//	│  [关注他] [打赏金币] [网页打开]               │
//	│  ────────────────────────────────────────  │
//	│  个人简介：…                                │
//	│  ────────────────────────────────────────  │
//	│  主题数 | 回复数 | 粉丝 | 关注               │
//	│  ────────────────────────────────────────  │
//	│  [他发布的主题] [他的最新回复]                │
//	│  · 标题 ……………                      2 天前  │
//	└────────────────────────────────────────────┘
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

// userCardWindowView 是资料卡窗口的 view。窗口没有系统标题栏：顶部横幅既是
// 装饰，也是拖动窗口的区域，关闭按钮画在横幅右上角。
func (a *app) userCardWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 这个窗口里的按钮要把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// Esc 收起窗口：随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeUserCardWindow)

	cs := a.userCard
	if cs == nil {
		// 窗口正在关闭（状态已丢弃）：画一帧空壳就好，不要在渲染里把状态
		// 重新建出来 —— 那会让「关掉就丢掉这份资料」失效。
		ui.Column(c).Fill().Children(func() { a.userCardBanner(c) })
		return
	}

	ui.Column(c).Fill().Children(func() {
		a.userCardBanner(c)
		a.userCardHeader(c, cs)
		a.userCardActions(c, cs)
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
			if cs.tab == userCardTabReplies {
				a.userCardListBody(c, cs, userCardReplies)
			} else {
				a.userCardListBody(c, cs, userCardTopics)
			}
		})
	})
	a.ensureUserCardLoaded()
}

// userCardBanner 是顶部横幅：从主题强调色渐变的装饰带，同时是拖动窗口的
// 区域；右上角是关闭按钮（对应版式里的「[x] 关闭」）。
func (a *app) userCardBanner(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).FillWidth().Height(userCardBannerHeight).DragWindow().
		Gradient(t.Accent, t.AccentHover, 90).Children(func() {
		ui.Row(c).Fill().Padding(8, 10).AlignItems(ui.Start).Children(func() {
			ui.Spacer(c).Grow(1)
			a.userCardCloseButton(c)
		})
	})
}

// userCardCloseButton 是横幅右上角的关闭按钮：图标 + 「关闭」，压在渐变色上
// 所以固定用白字与半透明白底，深浅主题下都看得清。
func (a *app) userCardCloseButton(c *ui.Context) {
	white := ui.RGB(255, 255, 255)
	b := ui.ButtonBase(c).Shrink(0).Padding(3, 8).Radius(6).Gap(4).
		Background(ui.Transparent)
	if b.Hovered() {
		b.Background(ui.RGBA(255, 255, 255, 0.22))
	}
	b.Children(func() {
		ui.Icon(c, closeIcon).Size(12, 12).TextColor(white)
		ui.Text(c, "关闭").FontSize(11).TextColor(white).SingleLine()
	})
	b.Label("关闭").Tooltip("关闭资料卡").OnClick(a.closeUserCardWindow)
}

// userCardHeader 是资料头：大头像、昵称、@用户名、PRO 与佩戴中的徽章。
func (a *app) userCardHeader(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	u := cs.user
	avatar := u.Avatar
	ui.Row(c).FillWidth().Padding(14, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		avatarView(c, cs.username, a.avatarBitmap(avatar), userCardAvatar)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, cs.title()).FontSize(17).Bold().SingleLine()
				if u.IsPro {
					userCardChip(c, "PRO", t.Accent)
				}
			})
			// 第二行：@用户名 + 徽章。徽章名字来自接口，站点有什么就显示
			// 什么（「守护者」这类徽章会原样出现）。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				if cs.username != "" {
					ui.Text(c, "@"+cs.username).FontSize(12).
						TextColor(t.TextMuted).SingleLine()
				}
				for _, b := range u.Badges {
					if b.Name == "" {
						continue
					}
					userCardChip(c, b.Name, t.Warning)
				}
			})
		})
	})
}

// userCardChip 是昵称旁的短标签：PRO、徽章名与统计数字都用它，靠颜色区分。
func userCardChip(c *ui.Context, label string, tint ui.Color) {
	t := c.Theme()
	ui.Box(c).Shrink(0).Padding(1, 6).Radius(4).Background(t.Surface).
		Border(1, t.Border).Children(func() {
		ui.Text(c, label).FontSize(10).TextColor(tint).SingleLine()
	})
}

// userCardActions 是三个动作：关注 / 取关、打赏金币、网页打开。
func (a *app) userCardActions(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(0, 16, 12, 16).Gap(8).
		AlignItems(ui.Center).Wrap().Children(func() {
		a.userCardFollowButton(c, cs)
		// 打赏：接口没有「按用户打赏」，打赏要落在具体的帖子或评论上，
		// 所以这里把该用户的帖子列表交给站点，金额在站点上确认。
		if ui.Button(c, "打赏金币").FontSize(12).Padding(4, 10).Radius(6).
			Background(ui.Transparent).Border(1, t.Border).
			TextColor(t.Text).Clicked() {
			a.openExternal(userSiteURL(cs.username) + "/topics")
		}
		if ui.Button(c, "网页打开").FontSize(12).Padding(4, 10).Radius(6).
			Background(ui.Transparent).Border(1, t.Border).
			TextColor(t.Text).Clicked() {
			a.openExternal(userSiteURL(cs.username))
		}
		if cs.follow.err != "" {
			ui.Text(c, cs.follow.err).FontSize(11).TextColor(t.Danger).
				MaxLines(2).Grow(1)
		}
	})
}

// userCardFollowButton 画关注按钮：未关注是主按钮「关注他」，已关注是低
// 强调的描边按钮「已关注」，点一下切换。自己的资料卡不显示。
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

// userCardBio 是「个人简介」一块。接口只给 has_readme 这个布尔值，正文要
// 到站点看，所以这里说明状态并给出出口，不假装拿到了正文。
func (a *app) userCardBio(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(10, 16).Gap(4).Children(func() {
		ui.Text(c, "个人简介").FontSize(11).TextColor(t.TextMuted).SingleLine()
		text := "正在读取资料…"
		switch {
		case cs.profileErr != "":
			text = "资料暂时不可用：" + cs.profileErr
		case cs.profileLoaded && cs.user.HasReadme:
			text = "这位用户填写了个人简介。接口只返回「是否填写」，正文请到站点查看。"
		case cs.profileLoaded:
			text = "这位用户还没有填写个人简介。"
		}
		ui.Text(c, text).FontSize(12).TextColor(t.Text).MaxLines(3)
		if cs.profileLoaded && cs.user.HasReadme {
			a.smallAction(c, "到站点看完整简介", false, func() {
				a.openExternal(userSiteURL(cs.username))
			})
		}
	})
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
	row := ui.ButtonBase(c).FillWidth().Padding(8, 10).Radius(8).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "· "+it.Title).FontSize(12).
					MaxLines(2).TextColor(t.Text)
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
	if it.Meta != "" {
		parts = append(parts, it.Meta)
	}
	return strings.Join(parts, " · ")
}

// userCardWhen 是行右侧的时间：Item 把它放在 SubtitleSuffix 里（形如
// 「 · 2 天前」），这里去掉前导分隔符。
func userCardWhen(it Item) string {
	return strings.TrimPrefix(it.SubtitleSuffix, " · ")
}

// openUserCardDetail 从资料卡打开一篇帖子或一条回复：关掉本窗口，把主窗口
// 带到前台并换到详情页。评论行会带上 CommentID，正文里据此定位楼层。
func (a *app) openUserCardDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.closeUserCardWindow()
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(it)
	a.showMainWindow()
}
