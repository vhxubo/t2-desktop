package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 「我的」窗口：点击标题栏上登录账号的头像打开。它和通知、设置一样是独立
// 窗口，而不是主窗口里的一层遮罩——四块内容（概览、我的内容、回复、权限）
// 都要滚动和分页，塞进主窗口的弹窗里只会互相挤。
//
// 数据、状态与请求在 profile.go；这里只负责窗口的开关与界面。

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showProfile 是「我的」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showProfile() {
	a.publish(a.showProfileWindow)
}

// showProfileWindow 在主线程上真正打开窗口。打开时先按需拉一次资料与余额，
// 首帧里看到的就是当前账号，而不是空壳再闪一下。
func (a *app) showProfileWindow() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看「我的」。", a.showProfile)
		return
	}
	if a.profile == nil {
		a.profile = newProfileState()
	}
	a.ensureProfileLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 profileWindowView。
	if a.win == nil {
		return
	}
	if win := a.profileWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setProfileWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("我的"),
		Width:             profileWindowWidth,
		Height:            profileWindowHeight,
		MinWidth:          profileWinMinWidth,
		MinHeight:         profileWinMinHeight,
		StateKey:          "profile",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.profileWindowView),
	})
	a.profileWin = win
	win.OnShow(func() { a.setProfileWindowShown(true) })
	win.OnHide(func() { a.setProfileWindowShown(false) })
	win.OnClosed(func() {
		a.profileWin = nil
		a.setProfileWindowShown(false)
		a.authGateWindowClosed(toastProfile)
	})
	a.setProfileWindowShown(true)
}

// profileWindowVisible 报告窗口此刻是否在屏幕上：标题栏的头像据此高亮。
func (a *app) profileWindowVisible() bool { return a.profileShown }

// setProfileWindowShown 记下显示状态并让主窗口重画一次。只在主线程调用。
func (a *app) setProfileWindowShown(shown bool) {
	if a.profileShown == shown {
		return
	}
	a.profileShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closeProfileWindow 关掉窗口：退出登录或主窗口关闭时调用，别让上一个账号
// 的资料留在屏幕上。只在主线程调用。
func (a *app) closeProfileWindow() {
	win := a.profileWin
	if win == nil {
		return
	}
	a.profileWin = nil
	a.setProfileWindowShown(false)
	win.Close()
}

// redrawProfileWindow 让窗口按最新状态重建。只在主线程调用。
func (a *app) redrawProfileWindow() {
	if win := a.profileWin; win != nil {
		win.Invalidate()
	}
}

// profileTag 是标题栏右侧的账号标识：头像 + 昵称，整体可点，点开「我的」。
// 只显示昵称，不再写「已登录」：头像和昵称本身就说明身份，那几个字只会挤占
// 标题栏宽度。昵称还没读到时只画头像，仍然是「我的」的入口。
func (a *app) profileTag(c *ui.Context, name string) {
	t := c.Theme()
	tag := ui.Box(c).Padding(2, 5).Radius(6).Background(ui.Transparent)
	if a.profileWindowVisible() || tag.Hovered() {
		tag.Background(t.SurfaceHover)
	}
	tag.Children(func() {
		if name == "" {
			// 昵称还没读到（身份接口慢或失败）：仍然给一个可点的头像位，
			// 「我的」窗口自己会拉资料，不至于没有入口。
			avatarView(c, "", a.avatarBitmap(a.auth.avatarURL()), userAvatarSize(12))
			return
		}
		a.userTag(c, userTagSpec{
			Name:   name,
			Avatar: a.auth.avatarURL(),
			Size:   12,
			Color:  t.TextMuted,
			// 这是自己的账号区，点它打开「我的」；默认的资料卡留给别处的
			// 用户标识。
			Click: a.showProfile,
		})
	})
	tag.Label("我的").Tooltip("打开「我的」窗口").OnClick(a.showProfile)
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// profileWindowView 是「我的」窗口的 view。窗口自带标题栏与窗口按钮，因此
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) profileWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 帖子行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastProfile))
	// Esc 收起窗口：随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeProfileWindow)

	ps := a.profile
	if ps == nil {
		return // 没有状态就不画：newApp 与 onAuthChanged 都会建好它
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, profileIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "我的").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if name := a.profileTitleName(ps); name != "" {
					ui.Text(c, name).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.profileWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeProfileWindow)
		})
		ui.Divider(c)

		a.profileHeader(c, ps)
		ui.Divider(c)

		// 标签页：内容各占满剩余高度，自己滚动。
		ui.Row(c).FillWidth().Padding(6, 12).Children(func() {
			ui.Tabs(c, &ps.tab, profileTabLabels...)
		})
		ui.Divider(c)

		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16).Children(func() {
			switch ps.tab {
			case profileTabContent:
				a.profileContentBody(c, ps)
			case profileTabReplies:
				a.profileRepliesBody(c, ps)
			case profileTabBadges:
				a.profileBadgesBody(c, ps)
			case profileTabAccount:
				a.profileAccountBody(c, ps)
			default:
				a.profileOverviewBody(c, ps)
			}
		})
	})
	a.ensureProfileLoaded()
	// 缺权限时也摆出同一个全局授权弹窗：从本窗口发起的请求失败后，用户就
	// 在这个窗口里看到原因与「前往授权」。
	a.authDialogFor(c, toastProfile)
	// 合集改名 / 删除的对话框：它们盖在整扇窗口上，所以画在最后。
	a.profileCollectionDialogs(c)
}

// profileTitleName 是窗口标题栏上的账号名：优先昵称，其次用户名。
func (a *app) profileTitleName(ps *profileState) string {
	if ps.user.DisplayName != "" {
		return ps.user.DisplayName
	}
	if ps.user.Name != "" {
		return ps.user.Name
	}
	return a.auth.username()
}

// profileHeader 是资料头：大头像、昵称、用户 ID 与注册时间，右侧是刷新。
func (a *app) profileHeader(c *ui.Context, ps *profileState) {
	t := c.Theme()
	u := ps.user
	name := a.profileTitleName(ps)
	avatar := u.Avatar
	if avatar == "" {
		avatar = a.auth.avatarURL()
	}
	title := u.DisplayName
	if title == "" {
		title = name
	}
	if title == "" {
		title = "已登录"
	}
	ui.Row(c).FillWidth().Padding(14, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		avatarView(c, name, a.avatarBitmap(avatar), 48)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(16).Bold().SingleLine()
				if u.IsPro {
					ui.Text(c, "PRO").FontSize(10).TextColor(t.Accent)
				}
				// 本人资料头也展示佩戴中的徽章；数据来自已经拉到的
				// GET /v1/users/:name，切换徽章后由 syncProfileEquippedBadges
				// 就地更新，不在这里额外请求。
				a.authorBadges(c, u.Badges)
			})
			line := profileIdentityLine(u)
			if ps.infoErr != "" {
				line = "资料暂时不可用：" + ps.infoErr
			} else if ps.infoLoading && !ps.infoLoaded {
				line = "正在读取账号资料…"
			}
			ui.Text(c, line).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		a.profileAvatarEntry(c, ps)
		a.smallAction(c, "刷新", ps.infoLoading, func() { a.reloadProfile() })
	})
}

/* ------------------------------------------------------------------
   Tab 1：概览
---------------------------------------------------------------- */

func (a *app) profileOverviewBody(c *ui.Context, ps *profileState) {
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			a.profileStatsSection(c, ps)
			a.profileAttributesSection(c, ps)
			a.profileAuthEntry(c, ps)
		})
	})
}

// profileStatsSection 是「最近统计」：粉丝、关注、主页访问与等级来自公开
// 资料；帖子数与回复数只有对应的标签页拉到过数据时才显示，不为了一个数字
// 在打开窗口时多发两个请求。粉丝与关注两块可以点开名单（见 follows.go）。
func (a *app) profileStatsSection(c *ui.Context, ps *profileState) {
	u := ps.user
	settingsSection(c, "最近统计", func() {
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			profileStatChip(c, "粉丝", profileCountText(ps.infoLoaded, u.Followers), func() {
				a.showFollows(followsFollowers)
			})
			profileStatChip(c, "关注", profileCountText(ps.infoLoaded, u.Following), func() {
				a.showFollows(followsFollowing)
			})
			profileStatChip(c, "主页访问", profileCountText(ps.infoLoaded, u.HomeView), nil)
			profileStatChip(c, "等级", profileCountText(ps.infoLoaded && u.Level > 0, u.Level), nil)
			if topics := &ps.content[profileContentTopics]; topics.loaded {
				profileStatChip(c, "帖子", fmt.Sprintf("%d", len(topics.items)), nil)
			}
			if ps.replyLoaded {
				profileStatChip(c, "回复", fmt.Sprintf("%d", ps.replyTotal), nil)
			}
			if ps.badgeLoaded {
				profileStatChip(c, "徽章", fmt.Sprintf("%d", len(ps.badges)), nil)
			}
		})
	})
}

// profileCountText 在数据还没到或接口没给值时显示占位，而不是一个假的 0。
func profileCountText(known bool, n int) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

// profileStatChip 是一块统计。onClick 非空时整块可点（粉丝 / 关注打开
// 名单），悬停给浅底与强调色描边，免得它看起来和旁边的只读数字一样。
func profileStatChip(c *ui.Context, label, value string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Padding(6, 10).Radius(8).Background(t.Surface).Border(1, t.Border)
	if onClick != nil {
		if b.Hovered() {
			b.Background(t.SurfaceHover)
			b.Border(1, t.Accent)
		}
		b.Label(label).Tooltip("查看" + label + "名单")
		b.OnClick(onClick)
	}
	b.Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, value).FontSize(14).Bold().SingleLine()
			ui.Text(c, label).FontSize(10).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// profileAuthEntry 是概览页底部的授权管理快速入口：一段说明 + 一个出口。
func (a *app) profileAuthEntry(c *ui.Context, ps *profileState) {
	t := c.Theme()
	settingsSection(c, "授权管理", func() {
		ui.Text(c, "本应用通过 2Libra OAuth 读取你的账号，只能用你授予的权限范围；"+
			"随时可以在站点后台撤销，撤销后本应用的接口调用会立即失效。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		a.smallAction(c, "查看当前权限", false, func() { ps.tab = profileTabAccount })
	})
}

/* ------------------------------------------------------------------
   Tab 2：我的内容（帖子 / 收藏 / 书签 / 合集）
---------------------------------------------------------------- */

// profileContentUnits 是「共 N <单位>」里的单位，与四个分段一一对应。
var profileContentUnits = []string{"篇", "条", "条", "个"}

// profileContentEmpty 是各分段没有内容时的两行说明。
var profileContentEmpty = [][2]string{
	{"还没有发布过帖子。", "需要发帖请到站点完成。"},
	{"还没有收藏过帖子。", "在站点收藏的帖子会出现在这里。"},
	{"还没有加过书签。", "在站点给评论加的书签会出现在这里。"},
	{"还没有创建合集。", "在帖子详情页的「合集」里新建；这里可以改名、删除与查看合集内容。"},
}

func (a *app) profileContentBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	kind := ps.contentKind
	if kind < 0 || kind >= profileContentCount {
		kind, ps.contentKind = profileContentTopics, profileContentTopics
	}
	st := &ps.content[kind]
	spec := profileContentSpecs[kind]

	// 点开一个合集时整块换成它里面的帖子，「返回合集列表」再回到这一页的分段。
	if kind == profileContentCollections && ps.collBrowse != nil {
		a.collectionTopicsBody(c, ps.collBrowse, a.openProfileItem, func() {
			ps.collBrowse = nil
		})
		return
	}

	// 分段切换：帖子 / 收藏 / 书签 / 合集。每段各有一份列表状态，已经拉过的
	// 保留着，来回切换不会重新请求。
	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Tabs(c, &ps.contentKind, profileContentLabels...)
		ui.Spacer(c).Grow(1)
		if st.loaded {
			ui.Textf(c, "共 %d %s", profileContentTotal(st), profileContentUnits[kind]).
				FontSize(11).TextColor(t.TextMuted)
		}
		a.smallAction(c, "刷新", st.loading, func() { a.reloadProfileContent(kind) })
	})

	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载"+profileContentLabels[kind]+"…").
				FontSize(12).TextColor(t.TextMuted)
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, st.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadProfileContent(kind, 1)
			}
		})
		return
	case len(st.items) == 0:
		empty := profileContentEmpty[kind]
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, empty[0]).FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, empty[1]).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	items := st.items
	st.list.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &st.list, len(items), func(i int) {
		a.profileContentRow(c, kind, items[i])
	}).Grow(1).MinHeight(0).Gap(8)
	if spec.Paging == pagingPages {
		a.profileMoreContent(c, kind, st)
	}
}

// profileContentTotal 是「共 N」的数字：优先用服务端给的总数，没给时退回
// 已经拉到的条数，不编一个 0。
func profileContentTotal(st *tabState) int {
	if st.total > 0 {
		return st.total
	}
	return len(st.items)
}

// profileContentRow 和主窗口的列表行用同一套画法：收藏是帖子、书签是评论，
// 书签因此会走评论的富行；点开后由 openProfileItem 在主窗口打开正文。
// 「书签」分段的行右侧多一枚移除按钮：书签本来就是这个窗口里能删的东西，
// 让用户为了删一条书签再进帖子是把断头路接了一半。「合集」分段同理：整行
// 点开合集里的帖子，右侧是改名与删除（合集是我自己的，见 collections.go）。
func (a *app) profileContentRow(c *ui.Context, kind int, it Item) {
	switch {
	case kind == profileContentCollections:
		a.profileCollectionRow(c, it)
	case kind == profileContentBookmarks && it.CommentID != "":
		a.profileBookmarkRow(c, it)
	default:
		a.rowWith(c, it, a.openProfileItem)
	}
}

// profileBookmarkRow 是「我的内容 - 书签」的一行：整行打开评论，右侧一枚
// 移除按钮。
func (a *app) profileBookmarkRow(c *ui.Context, it Item) {
	t := c.Theme()
	busy := a.profile != nil && a.profile.bookmarkBusy == it.CommentID
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			a.rowWith(c, it, a.openProfileItem)
		})
		b := ui.Box(c).Size(28, 28).Radius(8).Center().Background(ui.Transparent)
		hovered := b.Hovered() && !busy
		if hovered {
			b.Background(t.SurfaceHover)
		}
		ink := t.TextMuted
		if hovered {
			ink = t.Danger
		}
		b.Children(func() {
			if busy {
				ui.Spinner(c).Size(12, 12)
				return
			}
			ui.Icon(c, bookmarkOnIcon).Size(15, 15).TextColor(ink)
		})
		b.Label("移除书签").Tooltip("从书签里移除这条评论")
		if !busy {
			b.OnClick(func() { a.removeProfileBookmark(it) })
		}
	})
}

// profileCollectionRow 是「我的内容 - 合集」的一行：整行点开合集里的帖子，
// 右侧两枚图标按钮分别改名与删除。合集在站点上只有创建者能改，而这一页
// 本来就只有「我的」，所以不需要再判断归属。
func (a *app) profileCollectionRow(c *ui.Context, it Item) {
	ps := a.profile
	if ps == nil {
		return
	}
	busy := ps.collBusy != ""
	title := it.Title
	if title == "" {
		title = "未命名合集"
	}
	// 合集行没有 short_id，靠 Clickable 说明「这一行可以点」：点开的是合集里
	// 的帖子，而不是站外链接。
	row := it
	row.Clickable = !busy
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			a.rowWith(c, row, func(Item) { a.openProfileCollection(it) })
		})

		rename := iconActionButton(c, editIcon, "重命名合集 "+title, "给这个合集改名")
		rename.Disabled(busy)
		if rename.Clicked() {
			a.askRenameCollection(it)
		}
		del := iconActionButton(c, trashIcon, "删除合集 "+title, "删除这个合集，帖子本身不受影响")
		del.Disabled(busy)
		if del.Clicked() {
			a.askDeleteCollection(it)
		}
	})
}

// profileMoreContent 是分页列表底部的续拉行：还有下一页时给按钮，续拉失败
// 时给出原因并可重试，已有内容保持不动。
func (a *app) profileMoreContent(c *ui.Context, kind int, st *tabState) {
	t := c.Theme()
	switch {
	case st.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+st.moreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadProfileContent(kind, st.page) })
		})
	case st.loading && len(st.items) > 0:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	// 服务端给了总数且已经全部拿到时不再给按钮：留着它只会让人点一下拿到
	// 空页，白等一次请求。
	case !st.noMore && (st.totalPages == 0 || st.page < st.totalPages) &&
		!(st.total > 0 && len(st.items) >= st.total):
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "加载更多").Clicked() {
				a.loadProfileContent(kind, st.page+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

/* ------------------------------------------------------------------
   Tab 3：我的回复
---------------------------------------------------------------- */

func (a *app) profileRepliesBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	name := a.auth.username()
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "我的回复").FontSize(13).Bold()
		if ps.replyLoaded {
			ui.Textf(c, "共 %d 条", ps.replyTotal).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", ps.replyLoading, func() {
			if name != "" {
				a.loadProfileReplies(name, 1)
			}
		})
	})

	if name == "" {
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还不知道当前账号的用户名").FontSize(13).Bold()
			ui.Text(c, "点右上角「刷新」重新读取账号资料后再试。").FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	switch {
	case ps.replyLoading && len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载我的回复…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case ps.replyErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, ps.replyErr).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadProfileReplies(name, 1)
			}
		})
		return
	case len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "还没有发表过回复。").FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, "在帖子详情页底部的评论框里发表评论。").FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	items := ps.replies
	ps.replyList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &ps.replyList, len(items), func(i int) {
		a.profileReplyRow(c, items[i])
	}).Grow(1).MinHeight(0).Gap(8)
	a.profileMoreReplies(c, ps, name)
}

// profileMoreReplies 是回复列表底部的续拉行：还有下一页时给按钮，续拉失败
// 时给出原因并可重试，已有内容保持不动。
func (a *app) profileMoreReplies(c *ui.Context, ps *profileState, name string) {
	t := c.Theme()
	switch {
	case ps.replyMoreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+ps.replyMoreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadProfileReplies(name, ps.replyPage) })
		})
	case ps.replyLoading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	case !ps.replyNoMore:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "加载更多").Clicked() {
				a.loadProfileReplies(name, ps.replyPage+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

func (a *app) profileReplyRow(c *ui.Context, it Item) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, it.Title).FontSize(12).MaxLines(2)
			parts := make([]string, 0, 3)
			if x := it.Extra; x != nil {
				if x.PostTitle != "" {
					parts = append(parts, "《"+x.PostTitle+"》")
				}
				if x.Floor > 0 {
					parts = append(parts, fmt.Sprintf("%d 楼", x.Floor))
				}
				if x.When != "" {
					parts = append(parts, x.When)
				}
			}
			if len(parts) > 0 {
				ui.Text(c, strings.Join(parts, " · ")).FontSize(11).
					TextColor(t.TextMuted).SingleLine()
			}
		})
	})
	if it.ShortID != "" {
		row.Tooltip("打开原帖并定位这条回复")
		row.OnClick(func() { a.openProfileDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.openExternal(it.URL) })
	}
}

/* ------------------------------------------------------------------
   Tab 4：权限与账号
---------------------------------------------------------------- */

func (a *app) profileAccountBody(c *ui.Context, ps *profileState) {
	t := c.Theme()
	u := ps.user
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			settingsSection(c, "账号", func() {
				a.profileAccountRow(c, "用户名", profileOrDash(u.Name, a.auth.username()))
				a.profileAccountRow(c, "用户 ID", profileUserNumber(u.UserNumber))
				a.profileAccountRow(c, "注册时间", profileRegisteredOrDash(u.CreatedAt))
				a.profileAccountRow(c, "等级", profilePositiveOrDash(ps.infoLoaded, u.Level))
				a.profileAccountRow(c, "粉丝 / 关注",
					profileFollowText(ps.infoLoaded, u.Followers, u.Following))
			})
			ui.Divider(c)

			settingsSection(c, "授权范围", func() {
				scopes := a.auth.scopes()
				if len(scopes) == 0 {
					ui.Text(c, "令牌里没有 scope 信息：可能是旧版本授权的会话，"+
						"重新登录一次即可拿到完整范围。").
						FontSize(11).TextColor(t.TextMuted).MaxLines(3)
				} else {
					ui.Text(c, "当前令牌携带 "+fmt.Sprintf("%d", len(scopes))+" 项权限：").
						FontSize(11).TextColor(t.TextMuted)
					for _, s := range scopes {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, s).FontSize(11).Width(110).SingleLine()
							ui.Text(c, profileScopeLabel(s)).FontSize(11).
								TextColor(t.TextMuted).SingleLine().Grow(1)
						})
					}
				}
			})
			ui.Divider(c)

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "退出登录").Clicked() {
					a.logoutAccount()
				}
				ui.Spacer(c).Grow(1)
			})
			ui.Text(c, "撤销授权页在 2Libra 站点的「授权应用」里；撤销后需要重新登录本应用。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
	})
}

func (a *app) profileAccountRow(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		ui.Text(c, label).FontSize(12).Width(90).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(12).SingleLine()
	})
}

func profileOrDash(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "—"
}

func profileUserNumber(n int) string {
	if n <= 0 {
		return "—"
	}
	return "#" + fmt.Sprintf("%d", n)
}

func profileRegisteredOrDash(raw string) string {
	if text := profileRegisteredText(raw); text != "" {
		return text
	}
	return "—"
}

func profilePositiveOrDash(known bool, n int) string {
	if !known || n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func profileFollowText(known bool, followers, following int) string {
	if !known {
		return "—"
	}
	return fmt.Sprintf("%d / %d", followers, following)
}

/* ------------------------------------------------------------------
   打开正文与切换主窗口
---------------------------------------------------------------- */

// openProfileItem 是「我的」窗口里列表行的打开动作：站内帖子 / 评论回主窗口
// 正文（本窗口留着，读完还能接着看），其余交给系统浏览器。
func (a *app) openProfileItem(it Item) {
	switch {
	case it.ShortID != "":
		a.openProfileDetail(it)
	case it.URL != "":
		a.openExternal(it.URL)
	}
}

// openProfileDetail 从「我的」窗口打开一篇帖子或一条回复：把主窗口带到前台并
// 换到详情页，「我的」窗口本身留着不关 —— 读完后还能回到原来的标签页。
// 评论行会带上 CommentID，正文里据此定位楼层。
func (a *app) openProfileDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(it)
	a.showMainWindow()
}

/* ------------------------------------------------------------------
   合集：打开内容、改名、删除
--------------------------------------------------------------- */

// openProfileCollection 在「我的内容 - 合集」里点开一个合集：整块换成它里面
// 的帖子（collection_browse.go），返回时再回到合集列表。
func (a *app) openProfileCollection(it Item) {
	ps := a.profile
	if ps == nil || it.Key == "" || ps.collBusy != "" {
		return
	}
	name := it.Title
	if name == "" {
		name = "未命名合集"
	}
	ps.collBrowse = newCollectionBrowse(collectionItem{ID: it.Key, Name: name}, "", toastProfile)
	a.loadCollectionTopics(ps.collBrowse, 1)
}

// askRenameCollection 打开改名对话框，输入框先填上现在的名字。
func (a *app) askRenameCollection(it Item) {
	ps := a.profile
	if ps == nil || it.Key == "" || ps.collBusy != "" {
		return
	}
	ps.collEdit = it.Key
	ps.collDraft = it.Title
}

// submitRenameCollection 提交一次改名（PATCH，id 不变）。结果用本窗口的 toast
// 报告；成功后重读合集列表，让新名字立刻出现在这里。
func (a *app) submitRenameCollection() {
	ps := a.profile
	if ps == nil || ps.collEdit == "" || ps.collBusy != "" {
		return
	}
	name := strings.TrimSpace(ps.collDraft)
	if name == "" {
		a.toast(toastProfile, "请先填写合集名称")
		return
	}
	if utf8.RuneCountInString(name) > collectionNameMax {
		a.toast(toastProfile, fmt.Sprintf("合集名称最多 %d 个字", collectionNameMax))
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能修改合集。", a.submitRenameCollection)
		return
	}
	id := ps.collEdit
	ps.collBusy = id
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		item, err := a.client.renameCollection(ctx, id, name)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			ps.collBusy = ""
			// 请求期间换了账号 / 退出登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastProfile, err, a.submitRenameCollection) {
					return
				}
				a.toast(toastProfile, friendlyError(err))
				return
			}
			ps.collEdit = ""
			ps.collDraft = ""
			if ps.collBrowse != nil && ps.collBrowse.id == id {
				ps.collBrowse.name = item.Name
			}
			a.toast(toastProfile, "已改名为「"+item.Name+"」")
			a.invalidateDetailCollectionPicker()
			a.reloadProfileContent(profileContentCollections)
		})
	}()
}

// askDeleteCollection 打开删除确认框。删除不可恢复（合集里的帖子只是被取消
// 挂载，帖子本身不受影响），所以先问一句再提交。
func (a *app) askDeleteCollection(it Item) {
	ps := a.profile
	if ps == nil || it.Key == "" || ps.collBusy != "" {
		return
	}
	ps.collDelete = it.Key
}

// submitDeleteCollection 提交删除。结果用本窗口的 toast 报告；成功后就地关掉
// 正在浏览的那个合集（如果正是它）并重读列表。
func (a *app) submitDeleteCollection() {
	ps := a.profile
	if ps == nil || ps.collDelete == "" || ps.collBusy != "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能删除合集。", a.submitDeleteCollection)
		return
	}
	id := ps.collDelete
	name := ""
	for _, it := range ps.content[profileContentCollections].items {
		if it.Key == id {
			name = it.Title
			break
		}
	}
	ps.collBusy = id
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		err := a.client.deleteCollection(ctx, id)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			ps.collBusy = ""
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastProfile, err, a.submitDeleteCollection) {
					return
				}
				a.toast(toastProfile, friendlyError(err))
				return
			}
			ps.collDelete = ""
			if ps.collBrowse != nil && ps.collBrowse.id == id {
				ps.collBrowse = nil
			}
			// 当前打开的详情页可能正显示「已在合集」这个已删的合集：清掉归属，
			// 别让按钮指着一个不存在的合集。
			if d := a.detail; d != nil && d.topic != nil &&
				d.topic.CollectionKnown && d.topic.CollectionID == id {
				d.topic.CollectionID = ""
				d.topic.CollectionName = ""
			}
			if name != "" {
				a.toast(toastProfile, "已删除合集「"+name+"」")
			} else {
				a.toast(toastProfile, "已删除合集")
			}
			a.invalidateDetailCollectionPicker()
			a.reloadProfileContent(profileContentCollections)
		})
	}()
}

// profileCollectionDialogs 是改名与删除的对话框。同一时刻只开一个：改名优先，
// 关掉之后才轮到删除确认。
func (a *app) profileCollectionDialogs(c *ui.Context) {
	ps := a.profile
	if ps == nil {
		return
	}
	t := c.Theme()
	if ps.collEdit != "" {
		open := true
		dismissed := ui.Modal(c, &open, func() {
			ui.Column(c).Width(380).Gap(10).Children(func() {
				ui.Text(c, "重命名合集").FontSize(16).Bold()
				ui.TextInput(c, &ps.collDraft).Label("合集名称").
					Placeholder("合集名称").MaxLength(collectionNameMax).
					FillWidth().Disabled(ps.collBusy != "")
				ui.Textf(c, "最多 %d 个字；改名不会改变合集里的帖子。", collectionNameMax).
					FontSize(11).TextColor(t.TextMuted).MaxLines(2)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spacer(c).Grow(1)
					if ui.Button(c, "取消").Clicked() {
						ps.collEdit, ps.collDraft = "", ""
					}
					save := ui.PrimaryButton(c, "保存")
					if ps.collBusy != "" {
						save.Disabled(true)
					}
					if save.Clicked() {
						a.submitRenameCollection()
					}
				})
			})
		})
		if dismissed.Dismissed() {
			ps.collEdit, ps.collDraft = "", ""
		}
		return
	}
	if ps.collDelete == "" {
		return
	}
	name := "这个合集"
	for _, it := range ps.content[profileContentCollections].items {
		if it.Key == ps.collDelete && it.Title != "" {
			name = "「" + it.Title + "」"
			break
		}
	}
	open := true
	dismissed := ui.Modal(c, &open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "删除合集？").FontSize(16).Bold()
			ui.Text(c, "删除"+name+"只删合集本身：合集里的帖子会被取消挂载，"+
				"帖子、评论和浏览数都不受影响。删除不可恢复。").
				TextColor(t.TextMuted).MaxLines(3)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					ps.collDelete = ""
				}
				confirm := ui.PrimaryButton(c, "确认删除")
				if ps.collBusy != "" {
					confirm.Disabled(true)
				}
				if confirm.Clicked() {
					a.submitDeleteCollection()
				}
			})
		})
	})
	if dismissed.Dismissed() {
		ps.collDelete = ""
	}
}
