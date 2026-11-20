package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
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
// 整套界面走 Lobe UI：标题栏图标、资料头的标签、关注按钮、分段、统计卡片、
// 加载 / 空 / 失败状态与列表行分别由 lucide、display.Tag、action.Button、
// nav.Tabs、layout.Block 与 feedback 的组件提供。文案与动作保持不变，只换
// 观感；配色统一取 lobetheme.TokensOf(c)。数据、状态与请求在 user_card.go。

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
		Title:             a.subWindowTitle("用户资料"),
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
		a.authGateWindowClosed(toastUserCard)
		a.accountWindowClosed(win)
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
// 标题栏（拖动窗口、最小化与关闭），下面直接是资料主体。布局、文字与状态
// 提示全部走 Lobe：文字 display.Text、分段 nav.Tabs、分隔线 layout.Divider，
// 区域背景取 tk.BgLayout。
func (a *app) userCardWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	// 列表行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
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
		layout.Divider(c, layout.DividerProps{})
		a.userCardHeader(c, cs)
		layout.Divider(c, layout.DividerProps{})
		a.userCardBio(c, cs)
		layout.Divider(c, layout.DividerProps{})
		a.userCardStats(c, cs)
		layout.Divider(c, layout.DividerProps{})
		// 标签页用 Lobe 的 nav.Tabs：选中态与滑动指示块由组件负责。切换时
		// 只换正文，各页的滚动与分页状态都留在 userCardState。
		ui.Row(c).FillWidth().Padding(6, 16).Children(func() {
			c.Key("user-card-tabs")
			items := make([]nav.TabItem, 0, len(userCardTabLabels))
			for _, label := range userCardTabLabels {
				items = append(items, nav.TabItem{Label: label})
			}
			sel := cs.tab
			nav.Tabs(c, &sel, nav.TabsProps{
				Items:   items,
				Variant: nav.TabsSquare,
				Size:    lobetheme.Small,
			}, nil)
			if sel != cs.tab && sel >= 0 && sel < len(userCardTabLabels) {
				cs.tab = sel
			}
		})
		layout.Divider(c, layout.DividerProps{})
		// 每页给一个独立的 key，避免不同页复用彼此的状态。
		c.Key(fmt.Sprintf("user-card-page-%d", cs.tab))
		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16).Children(func() {
			switch cs.tab {
			case userCardTabReplies:
				a.userCardListBody(c, cs, userCardReplies)
			case userCardTabAttrs:
				a.userCardAttrsBody(c, cs)
			case userCardTabBadges:
				a.userCardBadgesBody(c, cs)
			case userCardTabCollections:
				a.userCardCollectionsBody(c, cs)
			default:
				a.userCardListBody(c, cs, userCardTopics)
			}
		})
	})
	a.userCardAboutEditor(c)
	a.ensureUserCardLoaded()
	// 资料里的个人介绍也是 Markdown：配图点开后预览是一扇独立窗口，这里只在
	// 图片到位后补开它（见 viewerTick）。
	a.viewerTick()
	// 缺权限时就在这个窗口里弹授权：关注 / 保存介绍等请求是从这里发起的。
	a.authDialogFor(c, toastUserCard)
}

// userCardTitleBar 是自绘标题栏：左边是 Lucide 人形图标与「用户资料」，右边
// 是最小化与关闭，画法与主窗口、通知、我的三个窗口一致。它同时是拖动窗口
// 的区域。资料头里写的是这张卡片的用户；标题栏最右那枚标识写的是「这扇窗口
// 属于哪个账号」，多账号时不至于把别人的资料当成自己账号的窗口。
func (a *app) userCardTitleBar(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
		Background(tk.BgLayout).DragWindow().Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
			AlignItems(ui.Center).Children(func() {
			ui.Icon(c, lucide.User()).Size(16, 16).TextColor(tk.Primary)
			display.Text(c, "用户资料", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
			a.accountWindowTag(c)
		})
		titleBarButton(c, titleMinimize, "最小化", func() {
			if win := a.userCardWin; win != nil {
				win.Minimize()
			}
		})
		titleBarButton(c, titleClose, "关闭", a.closeUserCardWindow)
	})
}

// userCardHeader 是资料头：头像、昵称、等级 / PRO / 徽章、身份信息与关注
// 按钮。关注按钮与身份信息同一行，不再单独占一行；资料头是整张卡里唯一
// 需要突出的地方，其余区块都贴着它往下排。
func (a *app) userCardHeader(c *ui.Context, cs *userCardState) {
	u := cs.user
	ui.Row(c).FillWidth().Padding(12, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		avatarView(c, cs.username, a.avatarBitmap(u.Avatar), userCardAvatar)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				display.Text(c, cs.title(), display.TextProps{Strong: true, FontSize: 16, Rows: 1})
				if u.Level > 0 {
					userCardTag(c, fmt.Sprintf("Lv.%d", u.Level), display.TagGray)
				}
				if u.IsPro {
					userCardTag(c, "PRO", display.TagBlue)
				}
			})
			// 第二行：@用户名 + 注册时间 + 佩戴中的徽章。徽章名字来自接口，
			// 站点有什么就显示什么（「守护者」这类徽章会原样出现）。行内
			// 放不下时自动折行，不会把关注按钮挤出窗口。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				if meta := userCardHeaderMeta(cs); meta != "" {
					display.Text(c, meta, display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 2,
					})
				}
				for _, b := range u.Badges {
					if b.Name == "" {
						continue
					}
					userCardTag(c, b.Name, display.TagGold)
					// 「我来过」的状态显示在徽章右侧，与站内一致；没有就是没有。
					if b.Status != nil {
						userCardTag(c, b.Status.Text, display.TagGray)
					}
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

// userCardTag 是昵称旁的短标签：等级、PRO、徽章名与徽章状态共用 Lobe 的
// Tag，靠颜色区分。原先手涂的描边盒子换成组件后，字号与圆角跟其它窗口一致。
func userCardTag(c *ui.Context, label string, color display.TagColor) {
	display.Tag(c, label, display.TagProps{
		Size: lobetheme.Small, Color: color, Variant: display.TagFilled, Round: true,
	})
}

// userCardFollowButton 画关注按钮：未关注是主按钮「关注他」，已关注是低
// 强调的描边按钮「已关注」，点一下切换。自己的资料卡不显示。它画在资料头
// 的右侧，与昵称同一行。按钮走 Lobe 的 action.Button，文案与 tooltip 不变。
func (a *app) userCardFollowButton(c *ui.Context, cs *userCardState) {
	if !a.userCardCanFollow() {
		return
	}
	following := a.userCardFollowing()
	label := "关注他"
	variant := action.Primary
	if following {
		label = "已关注"
		variant = action.Default
	}
	b := action.Button(c, label, action.ButtonProps{Size: lobetheme.Small, Variant: variant})
	b.Shrink(0).Disabled(cs.follow.busy)
	if following {
		b.Tooltip("点击取消关注")
	} else {
		b.Tooltip("关注这位用户")
	}
	if b.Clicked() {
		a.toggleUserCardFollow(!following)
	}
}

// userCardBio 是「个人简介」一块：正文来自 GET /v1/users/:userName/about，
// 按 Markdown 渲染（链接、列表与表情都走正文同一套渲染），{{KEY}} 统计
// 占位符换成该用户的实际数字。介绍可能很长，超过一块的高度就在块内滚动，
// 下面的统计、标签页与列表不会被挤出窗口。
func (a *app) userCardBio(c *ui.Context, cs *userCardState) {
	// MinHeight(0)：窗口缩到最小时先压这一块（里面的介绍框会跟着缩短），
	// 否则内容会把统计、标签页与列表顶出窗口。
	ui.Column(c).FillWidth().MinHeight(0).Padding(10, 16).Gap(4).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			display.Text(c, "个人简介", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			ui.Spacer(c).Grow(1)
			// 只有本人、且介绍已经读到，才给编辑入口：没读到正文就编辑，
			// 容易用空草稿覆盖掉原来的内容。
			if a.userCardSelf() && cs.aboutLoaded && cs.aboutErr == "" {
				a.smallAction(c, "编辑介绍", cs.aboutBusy, a.openUserCardAboutEditor)
			}
		})
		switch {
		case cs.aboutErr != "":
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "介绍暂时不可用："+cs.aboutErr, display.TextProps{
					Type: display.TextDanger, FontSize: userCardBioFontSize, Rows: 2,
				}).Grow(1).MinWidth(0)
				a.smallAction(c, "重试", cs.aboutLoading, a.loadUserCardAbout)
			})
		case !cs.aboutLoaded || cs.aboutLoading:
			display.Text(c, "正在读取个人介绍…", display.TextProps{
				FontSize: userCardBioFontSize, Rows: 1,
			})
		case strings.TrimSpace(cs.about.Content) == "":
			// 没填写和「仅社区成员可见」都是空正文，但原因不同，不能混为一谈。
			text := "这位用户还没有填写个人介绍。"
			if cs.about.PublicKnown && !cs.about.IsPublic {
				text = "这位用户的个人介绍仅在社区内可见。"
			}
			display.Text(c, text, display.TextProps{FontSize: userCardBioFontSize, Rows: 2})
		default:
			// Lobe 的细滚动条与边缘渐隐，替换 mygo 原生 Scroll；高度仍按窗口
			// 剩余空间计算，窗口被缩小时介绍先让位。
			scroll := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
				// 介绍自己的 emojiMap 可能带社区图标；并到全局表情表后一起
				// 渲染，规则与帖子正文相同。
				emojis := mergeEmojiMaps(a.emojis, cs.about.Emojis)
				src := replaceEmoji(expandAboutStats(cs.about.Content, cs.about.Stats), emojis, true)
				a.markdownView(c, src, a.bioMd)
			})
			scroll.FillWidth().MaxHeight(userCardBioContentHeight(c))
			if meta := userCardAboutMeta(cs.about); meta != "" {
				display.Text(c, meta, display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
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
			parts = append(parts, "介绍仅社区成员可见")
		}
	}
	if t, ok := profileTime(about.UpdatedAt); ok {
		parts = append(parts, "更新于 "+t.Local().Format("2006-01-02 15:04"))
	}
	return strings.Join(parts, " · ")
}

// userCardStats 是统计条：主题数、回复数、粉丝、关注。数字都来自接口，
// 列表还没拉到时显示占位而不是 0。四张统计卡走 Lobe 的 layout.Block。
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
	b := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
		Grow(1).MinWidth(0).Padding(6, 8)
	b.Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			display.Text(c, value, display.TextProps{Strong: true, FontSize: 14, Rows: 1})
			display.Text(c, label, display.TextProps{
				Type: display.TextSecondary, FontSize: 10, Rows: 1,
			})
		})
	})
}

// userCardCollectionsBody 是「他的合集」分段：平时是公开合集列表，点开一个
// 合集就地换成它里面的帖子，左上角的「返回」回到列表。
func (a *app) userCardCollectionsBody(c *ui.Context, cs *userCardState) {
	if cs.collBrowse != nil {
		a.collectionTopicsBody(c, cs.collBrowse, a.openUserCardDetail, func() {
			cs.collBrowse = nil
		})
		return
	}
	a.userCardListBody(c, cs, userCardCollections)
}

// userCardListBody 画一个标签页的列表：加载中、失败、空与正常四种状态，
// 正常时每行可以点开正文。四种状态分别用 Lobe 的 Spin、Result、Empty 与
// Block，文案与入口保持原样。
func (a *app) userCardListBody(c *ui.Context, cs *userCardState, kind userCardListKind) {
	l := cs.list(kind)
	switch {
	case l.loading && len(l.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Large})
			display.Text(c, "正在加载…", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
		})
		return
	case l.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			feedback.Result(c, feedback.ResultProps{
				Status:   feedback.ResultError,
				Title:    "加载失败",
				SubTitle: l.err,
				Extra: func() {
					if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.loadUserCardList(kind, 1)
					}
				},
			})
		})
		return
	case l.loaded && len(l.items) == 0:
		empty := display.EmptyProps{}
		switch kind {
		case userCardReplies:
			empty.Title = "这位用户还没有发表过回复。"
			empty.Icon = lucide.MessageCircle()
		case userCardCollections:
			empty.Title = "这位用户还没有公开合集。"
			empty.Icon = lucide.Folder()
		default:
			empty.Title = "这位用户还没有发布过主题。"
			empty.Icon = lucide.FileText()
		}
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			display.Empty(c, empty)
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
	// 公开合集整页返回，没有下一页，别给一颗点了只会拿到空页的按钮。
	if userCardSpec(cs.username, kind, 1).Paging == pagingPages {
		a.userCardMore(c, cs, kind)
	}
}

// userCardMore 是列表底部的续拉：还有下一页时给按钮，续拉失败时给出原因
// 并可重试，已有内容保持不动。
func (a *app) userCardMore(c *ui.Context, cs *userCardState, kind userCardListKind) {
	tk := lobetheme.TokensOf(c)
	l := cs.list(kind)
	switch {
	case l.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "加载更多失败："+l.moreErr, display.TextProps{
				Type: display.TextDanger, FontSize: 11, Rows: 2,
			}).Grow(1).MinWidth(0)
			a.smallAction(c, "重试", false, func() { a.loadUserCardList(kind, l.page) })
		})
	case l.loading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
			display.Text(c, "正在加载更多…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		})
	case !l.noMore:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			b := action.Button(c, "加载更多", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Default,
			})
			b.TextColor(tk.Primary)
			if b.Clicked() {
				a.loadUserCardList(kind, l.page+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

// userCardRow 是一行内容：主题行显示标题与元信息，回复行显示正文摘要与
// 它所属的帖子；右侧是时间。点一下回到主窗口的正文页。整行是一张可点的
// Lobe Block，悬停时底色提亮，文案与提示保持原样。
func (a *app) userCardRow(c *ui.Context, it Item, kind userCardListKind) {
	tk := lobetheme.TokensOf(c)
	// 主题行跟着本地已读状态把标题置灰；回复行表示的是评论内容，不跟帖子
	// 的已读走（与 rows.go 的富行同一规则）。
	titleColor := tk.Text
	if kind == userCardTopics && a.isPostRead(it.ShortID) {
		titleColor = tk.TextSecondary
	}
	row := layout.Block(c, layout.BlockProps{
		Variant: layout.BlockOutlined, Clickable: true,
	}).FillWidth().Padding(8, 10)
	if row.Hovered() {
		row.Background(tk.FillSecondary)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				t := display.Text(c, "· "+it.Title, display.TextProps{FontSize: 12, Rows: 2})
				t.TextColor(titleColor)
				if meta := userCardRowMeta(it, kind); meta != "" {
					display.Text(c, meta, display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
			})
			if when := userCardWhen(it); when != "" {
				display.Text(c, when, display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				}).Shrink(0)
			}
		})
	})
	// 合集行没有 short_id：点开的是这个合集里面的帖子（见 collection_browse.go）。
	if kind == userCardCollections {
		row.Tooltip("查看这个合集里的帖子")
		if it.Key != "" {
			row.OnClick(func() { a.openUserCardCollection(it) })
		}
		return
	}
	if it.ShortID != "" {
		row.Tooltip("打开正文")
		row.OnClick(func() { a.openUserCardDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.openExternal(it.URL) })
	}
}

// openUserCardCollection 在资料卡的「他的合集」里点开一个合集：就地换成它里面
// 的帖子，返回时回到合集列表。这里记下主人昵称，浏览视图里会写「by @昵称」。
func (a *app) openUserCardCollection(it Item) {
	cs := a.userCard
	if cs == nil || it.Key == "" || cs.username == "" {
		return
	}
	name := it.Title
	if name == "" {
		name = "未命名合集"
	}
	cs.collBrowse = newCollectionBrowse(collectionItem{ID: it.Key, Name: name}, cs.username, toastUserCard)
	a.loadCollectionTopics(cs.collBrowse, 1)
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
	a.showMainWindowAfterDetail()
}
