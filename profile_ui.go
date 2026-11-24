package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「个人中心」窗口：点击标题栏上登录账号的头像打开。它和通知、设置一样是独立
// 窗口，而不是主窗口里的一层遮罩——概览、内容、资产等页面都要滚动和分页，
// 塞进主窗口的弹窗里只会互相挤。
//
// 整套界面走 Lobe UI：标题栏、资料头与一级标签统一用 Token 配色和 Lobe
// 组件（TabsBase、nav.Tabs、display.Avatar、popupSurface / popupRow 等），与
// 账号面板、设置窗口是同一套设计语言。区块与列表行不画圆角与整圈描边。
//
// 数据、状态与请求在 profile.go；这里只负责窗口的开关与界面。

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// profileTabIcons 与 profileTabLabels 一一对应：图标只是标签前的装饰，文案仍
// 是读屏与点击测试命中的无障碍名。
var profileTabIcons = []*ui.SVG{
	lucide.ChartNoAxesColumn(), lucide.FileText(), lucide.Layers3(),
	lucide.Wallet(), lucide.Award(), lucide.Settings2(),
}

// showProfile 是「个人中心」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showProfile() {
	if a.onProfileOpen != nil {
		a.onProfileOpen()
	}
	a.publish(a.showProfileWindow)
}

// showProfileTab 打开「个人中心」窗口并直接落在指定一级标签。徽章墙、合集
// 这些命令走这里；设置页还要指定二级分段时用 showProfileSettings。
func (a *app) showProfileTab(tab int) {
	if a.profile == nil {
		a.profile = newProfileState()
	}
	a.profile.tab = tab
	a.showProfile()
}

// showProfileSettings 打开「设置」页并落到指定的二级分段。信息流空状态里的
// 「管理屏蔽名单」和命令中心的内容屏蔽入口都走这里。
func (a *app) showProfileSettings(kind int) {
	if a.profile == nil {
		a.profile = newProfileState()
	}
	a.profile.tab = profileTabSettings
	a.profile.settingsKind = kind
	a.showProfile()
}

// showProfileWindow 在主线程上真正打开窗口。打开时先按需拉一次资料与余额，
// 首帧里看到的就是当前账号，而不是空壳再闪一下。
func (a *app) showProfileWindow() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看「个人中心」。")
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
	win := a.newAppWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("个人中心"),
		Width:             profileWindowWidth,
		Height:            profileWindowHeight,
		MinWidth:          profileWinMinWidth,
		MinHeight:         profileWinMinHeight,
		StateKey:          "profile",
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.profileWindowView)),
	}, toastProfile)
	a.profileWin = win
	win.OnShow(func() { a.setProfileWindowShown(true) })
	win.OnHide(func() { a.setProfileWindowShown(false) })
	win.OnClosed(func() {
		a.profileWin = nil
		a.setProfileWindowShown(false)
		a.authGateWindowClosed(toastProfile)
		a.accountWindowClosed(win)
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

// profileTag 是标题栏右侧的账号标识：头像 + 昵称。它只作账号菜单的锚点，
// 点一下弹出气泡，签到、金币、潘多拉、个人中心与退出登录都收在气泡里。只显示
// 昵称，不再写「已登录」：头像和昵称本身就说明身份，那几个字只会挤占标题栏宽度。
// 昵称还没读到时只画头像，仍然是账号菜单的入口。
func (a *app) profileTag(c *ui.Context, name string) {
	tk := lobetheme.TokensOf(c)
	tag := ui.Box(c).Padding(2, 5).Radius(tk.BorderRadiusSM)
	if a.profileMenuOpen || a.profileWindowVisible() || tag.Hovered() {
		tag.Background(tk.FillSecondary)
	}
	tag.Children(func() {
		a.profileTagIdentity(c, name)
	})
	tag.Label("个人中心").Tooltip("打开账号菜单：签到、金币、潘多拉、个人中心与退出登录")
	// Lobe 的 Popover 点锚点开合、点窗外与 Esc 关闭；气泡挂在账号标识下方。
	// 锚点本身不再挂 OnClick：点它只开合气泡，进哪个页面在气泡里选。
	// Popover 没有打开回调，但它在构建期就翻转 open：对比构建前后的值即可
	// 认出这次是「打开」。每次展开都重新读一次余额，并趁机同步今日签到状态
	//（签到按自然日缓存，同一天只会真正拉一次）。
	wasOpen := a.profileMenuOpen
	lobeoverlay.Popover(c, tag, &a.profileMenuOpen, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		a.profileMenuPanel(c)
	})
	if !wasOpen && a.profileMenuOpen {
		a.refreshCoinTagBalance()
		a.ensureCoinSignSynced()
	}
}

// profileTagIdentity 画账号标识里的头像与昵称。两段都不挂 OnClick，让这一帧
// 的点击传到外层的账号菜单锚点，由 Popover 开合气泡。昵称还没读到时只画头像。
func (a *app) profileTagIdentity(c *ui.Context, name string) {
	tk := lobetheme.TokensOf(c)
	size := userAvatarSize(12)
	pro, _ := a.auth.proStatus()
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		if name == "" {
			// 昵称还没读到（身份接口慢或失败）：仍然给一个可点的头像位，
			// 账号菜单自己会带进「个人中心」窗口，不至于没有入口。
			a.avatarWithPro(c, "", a.avatarBitmap(a.auth.avatarURL()), size, pro)
			return
		}
		a.avatarWithPro(c, name, a.avatarBitmap(a.auth.avatarURL()), size, pro)
		ui.Text(c, name).FontSize(12).SingleLine().TextColor(tk.TextSecondary)
	})
}

// profileMenuPanel 是账号菜单气泡的内容：签到、金币、潘多拉与个人中心四个
// 入口，末尾用一条分隔线隔开退出登录。金币行带余额，签到行带今日状态，潘多拉
// 行沿用动态名字与「可抽」角标；点任一行都先收起气泡再进对应页面。
func (a *app) profileMenuPanel(c *ui.Context) {
	ui.Column(c).Width(236).Gap(2).Children(func() {
		a.profileMenuSignRow(c)
		a.profileMenuCoinsRow(c)
		a.profileMenuPandoraRow(c)
		a.profileMenuMineRow(c)
		layout.Divider(c, layout.DividerProps{})
		a.profileMenuLogoutRow(c)
	})
}

// profileMenuRow 画账号菜单里的一行：图标 + 文案 + 可选右侧信息。整行可点，
// 点下去先收起气泡，再执行 goAction。图标只是装饰；可见文案是 label，读屏
// 与点击测试命中的无障碍名可用 accessName 另给（空时用 label）。只有潘多拉
// 行传 tip：它要借 tooltip 展示进行中事件的效果，其余行传空，不挂提示。
func (a *app) profileMenuRow(c *ui.Context, icon *ui.SVG, label, accessName, tip string, trailing func(), goAction func()) {
	tk := lobetheme.TokensOf(c)
	row := ui.Row(c).FillWidth().Padding(7, 8).Radius(tk.BorderRadiusSM).
		Gap(8).AlignItems(ui.Center).Background(ui.Transparent)
	if row.Hovered() {
		row.Background(tk.FillSecondary)
	}
	row.Children(func() {
		ui.Icon(c, icon).Size(16, 16).TextColor(tk.TextSecondary)
		ui.Text(c, label).FontSize(12).SingleLine()
		if trailing != nil {
			ui.Spacer(c).Grow(1)
			trailing()
		}
	})
	name := label
	if accessName != "" {
		name = accessName
	}
	row.Label(name)
	if tip != "" {
		row.Tooltip(tip)
	}
	if row.Clicked() {
		a.profileMenuOpen = false
		goAction()
	}
}

// profileMenuSignRow 是账号菜单的签到入口：显示今日签到状态，点一下直接提交
// 签到，结果由 toast 报告。金币窗口里不再放签到按钮，入口统一收在这里。
func (a *app) profileMenuSignRow(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	status := "签到领金币"
	if cs := a.coins; cs != nil {
		switch {
		case cs.signBusy:
			status = "正在签到…"
		case cs.signSyncing:
			status = "正在同步签到状态…"
		case cs.sign.Known && cs.sign.Signed:
			status = "今日已签到"
			if cs.sign.Streak > 0 {
				status += fmt.Sprintf(" · 连续 %d 天", cs.sign.Streak)
			}
		case cs.sign.Known:
			status = "今日未签到"
		}
	}
	a.profileMenuRow(c, lucide.CalendarCheck2(), "签到", "签到", "", func() {
		ui.Text(c, status).FontSize(11).TextColor(tk.TextSecondary).SingleLine()
	}, func() { a.signInNow() })
}

// profileMenuCoinsRow 是账号菜单的金币入口：金币图标 + 千分位余额（还没读到
// 时是破折号），点开独立的金币窗口。
func (a *app) profileMenuCoinsRow(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	balance := "—"
	if a.coins != nil && a.coins.balanceOK {
		balance = coinThousands(a.coins.balance)
	}
	a.profileMenuRow(c, lucide.Coins(), "金币", "金币", "", func() {
		ui.Text(c, balance).FontSize(11).TextColor(tk.TextSecondary).SingleLine()
	}, func() { a.showCoins() })
}

// profileMenuPandoraRow 是账号菜单的潘多拉入口：抽中事件后显示事件名，今天
// 还能抽时右侧带「可抽」角标，点开抽卡弹窗。
func (a *app) profileMenuPandoraRow(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	a.profileMenuRow(c, pandoraIcon, pandoraEntryName(a.pandora), "", pandoraEntryHint(a.pandora), func() {
		if a.pandoraReady() {
			ui.Box(c).Height(14).Padding(0, 4).Radius(999).Center().
				Background(tk.Error).Children(func() {
				ui.Text(c, "可抽").FontSize(9).Bold().SingleLine().TextColor(tk.White)
			})
		}
	}, func() { a.showPandora() })
}

// profileMenuMineRow 是账号菜单的「个人中心」入口：点开「个人中心」窗口。
func (a *app) profileMenuMineRow(c *ui.Context) {
	// 可见文案与标题栏锚点一样是「个人中心」，无障碍名另给一个唯一的，读屏与
	// 点击测试才分得清锚点和气泡里的入口。
	a.profileMenuRow(c, lucide.UserRound(), "个人中心", "打开个人中心窗口", "", nil, func() {
		a.showProfile()
	})
}

// profileMenuLogoutRow 是账号菜单末尾的退出登录入口：原来它占着侧边栏底部
// 一行，现在和其余账号操作一起收在气泡里。退出实际走 a.logoutAccount，多账号
// 时摘掉这个账号并关掉它的窗口，单账号时就是普通退出。
func (a *app) profileMenuLogoutRow(c *ui.Context) {
	a.profileMenuRow(c, logoutIcon, "退出登录", "", "", nil, func() {
		a.logoutAccount()
	})
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// profileWindowView 是「个人中心」窗口的 view。窗口自带标题栏与窗口按钮，因此
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) profileWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	// 帖子行等入口可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	defer a.drainToasts(c, a.claimsToasts(toastProfile))
	// Esc 收起窗口：随时可以再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeProfileWindow)

	ps := a.profile
	if ps == nil {
		return // 没有状态就不画：newApp 与 onAuthChanged 都会建好它
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.UserRound()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "个人中心", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				if name := a.profileTitleName(ps); name != "" {
					display.Text(c, name, display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
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

		a.profileHeader(c, ps)

		// 六个一级标签用可换行的 TabsBase 自己画：点击与键盘选择由基础组件
		// 负责，选中态是主色文字 + 底边线。切换时只换正文，窗口还是这一扇；
		// 各页的滚动与分页状态都留在 profileState。
		ui.Row(c).FillWidth().Padding(4, 12).Children(func() {
			c.Key("profile-tabs")
			sel := ps.tab
			tabs := ui.TabsBase(c, &sel, profileTabCount)
			tabs.List.Row().Wrap().FillWidth().Gap(2).AlignItems(ui.Center).Children(func() {
				for i, label := range profileTabLabels {
					tab := tabs.Tab(i).Gap(4).AlignItems(ui.Center).
						Padding(6, 8).Shrink(0).BorderWidth(0, 0, 2, 0)
					fg := tk.TextSecondary
					if i == sel {
						fg = tk.Primary
						tab.BorderColor(tk.Primary)
					} else {
						tab.BorderColor(ui.Transparent)
					}
					if tab.Hovered() {
						fg = tk.Text
					}
					tab.TextColor(fg).FontSize(11)
					tab.Children(func() {
						ui.Icon(c, profileTabIcons[i]).FontSize(12)
						ui.Text(c, label).SingleLine()
					})
				}
			})
			if sel != ps.tab && sel >= 0 && sel < len(profileTabLabels) {
				ps.tab = sel
			}
		})
		// 切进「屏蔽设置」就强制 GET 一次：网页端可能改过名单，进入时看到
		// 的必须是最新一份，保存时才是整份替换而不是拿旧基线覆盖别人的改动。
		if ps.tabSeen != ps.tab || ps.settingsSeen != ps.settingsKind {
			ps.tabSeen, ps.settingsSeen = ps.tab, ps.settingsKind
			if ps.tab == profileTabSettings && ps.settingsKind == profileSettingsFilter {
				a.ensureContentFilter(true)
			}
		}

		// 每页给一个独立的 key，避免不同页复用彼此的输入框 / 分段控件状态。
		c.Key(fmt.Sprintf("profile-page-%d-%d-%d-%d", ps.tab, ps.contentKind, ps.assetKind, ps.settingsKind))
		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 16).Children(func() {
			switch ps.tab {
			case profileTabAbout:
				a.profileAboutBody(c, ps)
			case profileTabContent:
				a.profileContentBody(c, ps)
			case profileTabAssets:
				a.profileAssetsBody(c, ps)
			case profileTabBadges:
				a.profileBadgesBody(c, ps)
			case profileTabSettings:
				a.profileSettingsBody(c, ps)
			default:
				a.profileOverviewBody(c, ps)
			}
		})
	})
	a.ensureProfileLoaded()
	// 缺权限时也摆出同一个全局授权弹窗：从本窗口发起的请求失败后，用户就
	// 在这个窗口里看到原因与「前往授权」。
	a.authDialogFor(c, toastProfile)
	// 合集改名 / 删除、个人简介编辑的对话框：盖在整扇窗口上，画在最后。
	a.profileCollectionDialogs(c)
	a.profileAboutEditor(c)
	// 换头像的裁剪编辑器同样是盖在窗口上的模态框：选好图片后先在这里调整。
	a.profileAvatarCrop(c)
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
	tk := lobetheme.TokensOf(c)
	u := ps.user
	// 头像图案按用户名生成：昵称会改、各页面拿到的也可能是昵称，只有用户名
	// 能把同一个人的头像在列表、评论与资料头之间对上。
	seed := u.Name
	if seed == "" {
		seed = a.auth.username()
	}
	avatar := u.Avatar
	if avatar == "" {
		avatar = a.auth.avatarURL()
	}
	title := a.profileTitleName(ps)
	if title == "" {
		title = "已登录"
	}
	ui.Row(c).FillWidth().Padding(14, 16).Gap(14).AlignItems(ui.Center).Children(func() {
		// 头像与列表、评论、账号面板同一套：有缓存位图就用它，没有时按用户名
		// 画同一个图案，各页面看到的是同一张。
		a.avatarWithPro(c, seed, a.avatarBitmap(avatar), 48, u.IsPro)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			// 昵称、等级与佩戴徽章同处一行，按这一行的 Gap 留白。等级紧跟
			// 昵称，和用户资料卡的资料头是同一个位置；概览里的等级统计块
			// 仍然保留，资料没读到时不画一个假的 Lv.0。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				display.Text(c, title, display.TextProps{Strong: true, FontSize: 16, Rows: 1})
				if ps.infoLoaded && u.Level > 0 {
					lv := ui.Box(c).Padding(2, 6).Radius(999).Background(tk.PrimaryBg).Center()
					lv.Children(func() {
						ui.Text(c, fmt.Sprintf("Lv.%d", u.Level)).FontSize(10).Bold().
							SingleLine().TextColor(tk.Primary)
					})
				}
				// 本人资料头也展示佩戴中的徽章；数据来自已经拉到的
				// GET /v1/users/:name，切换徽章后由 syncProfileEquippedBadges
				// 就地更新，不在这里额外请求。
				a.authorBadges(c, u.Badges)
			})
			line := profileIdentityLine(u)
			lineType := display.TextSecondary
			if ps.infoErr != "" {
				line = "资料暂时不可用：" + ps.infoErr
				lineType = display.TextDanger
			} else if ps.infoLoading && !ps.infoLoaded {
				line = "正在读取账号资料…"
			}
			display.Text(c, line, display.TextProps{Type: lineType, FontSize: 11, Rows: 1})
		})
		a.profileAvatarEntry(c, ps)
		a.smallRefreshButton(c, ps.infoLoading, func() { a.reloadProfile() })
	})
}

/* ------------------------------------------------------------------
   概览：用户数据看板
---------------------------------------------------------------- */

func (a *app) profileOverviewBody(c *ui.Context, ps *profileState) {
	// Lobe 的细滚动条与边缘渐隐，替换 mygo 原生 Scroll。
	list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			a.profileStatsSection(c, ps)
			a.profileAttributesSection(c, ps)
		})
	})
	list.Grow(1).MinHeight(0)
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

// profileStatChip 是一块统计。onClick 非空时整块可点（粉丝 / 关注打开名单），
// 悬停给浅底，不画圆角与描边，靠交互本身和旁边的只读数字区分。
func profileStatChip(c *ui.Context, label, value string, onClick func()) {
	tk := lobetheme.TokensOf(c)
	b := popupSurface(c, onClick != nil).Padding(6, 10).Shrink(0)
	if onClick != nil {
		if b.Hovered() {
			b.Background(tk.FillTertiary)
		}
		b.Label(label)
		b.OnClick(onClick)
	}
	b.Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			display.Text(c, value, display.TextProps{Strong: true, FontSize: 14, Rows: 1})
			display.Text(c, label, display.TextProps{
				Type: display.TextSecondary, FontSize: 10, Rows: 1,
			})
		})
	})
}

/* ------------------------------------------------------------------
   个人介绍：个人自述
---------------------------------------------------------------- */

// profileAboutBody 是「个人介绍」页：正文、公开状态与编辑入口和
// 用户资料卡共用 aboutBody，本人且正文已读到时右上角出现「编辑介绍」。
func (a *app) profileAboutBody(c *ui.Context, ps *profileState) {
	a.aboutBody(c, aboutViewState{
		about: ps.about, loaded: ps.aboutLoaded, loading: ps.aboutLoading,
		err: ps.aboutErr, busy: ps.aboutBusy,
	}, ps.aboutLoaded && ps.aboutErr == "",
		a.openProfileAboutEditor, a.loadProfileAbout)
}

/* ------------------------------------------------------------------
   内容（发布 / 合集 / 评论）与资产（收藏 / 书签）
---------------------------------------------------------------- */

// profileContentUnits 是「共 N <单位>」里的单位，与四份列表状态一一对应。
var profileContentUnits = []string{"篇", "条", "条", "个"}

// profileContentEmpty 是各分段没有内容时的两行说明。
var profileContentEmpty = [][2]string{
	{"还没有发布过帖子。", "需要发帖请到站点完成。"},
	{"还没有收藏过帖子。", "在站点收藏的帖子会出现在这里。"},
	{"还没有加过书签。", "在站点给评论加的书签会出现在这里。"},
	{"还没有创建合集。", "在帖子详情页的「合集」里新建；这里可以改名、删除与查看合集内容。"},
}

// profileContentEmptyIcons 与四段空状态一一对应：图标只是空状态的装饰，两行
// 文案仍由 profileContentEmpty 给出。
var profileContentEmptyIcons = []*ui.SVG{
	lucide.FileText(), lucide.Heart(), lucide.Bookmark(), lucide.FolderClosed(),
}

// profileTopicCollRow 是「帖子」分段上方的合集下拉。选项从 GET /v1/me/collections
// 读，不让人手输合集名——拼错一个字就得到空列表，还看不出为什么。
// 未筛选与筛选后的帖子都走公开端点 /v1/users/:name/topics，后者多带一个
// collection 参数，所以下面用单独的列表状态，选中即应用，选回「全部合集」
// 立刻回到原来的「我的帖子」。
func (a *app) profileTopicCollRow(c *ui.Context, ps *profileState) {
	st := &ps.collNames
	names := make([]string, 0, len(st.names)+1)
	names = append(names, profileTopicCollAll)
	names = append(names, st.names...)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			filter := lobeSelect(c, &ps.topicCollDraft, field.SelectProps{
				Options:     selectOptions(names),
				Size:        lobetheme.Small,
				Placeholder: "选择合集筛选帖子",
			})
			filter.Label("按合集名筛选帖子")
			if filter.Changed() {
				a.applyProfileTopicColl(ps.topicCollDraft)
			}
		})
		if st.loading {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
		} else if !st.loaded {
			a.smallAction(c, "重试", false, func() {
				// 手动重试跳过退避：用户点了就是要现在再试一次。
				s := &ps.collNames
				s.triedAt = time.Time{}
				a.ensureProfileCollNames()
			})
		}
	})
}

// profileListTabs 画「内容 / 资产」页的二级分段行。选中变化在构建时写回 sel，
// trailing 随后拿到新的分段：右侧的计数与刷新因此总跟着当前选中的那一段。
func (a *app) profileListTabs(c *ui.Context, key string, labels []string, sel *int, trailing func(int)) {
	kind := *sel
	if kind < 0 || kind >= len(labels) {
		kind, *sel = 0, 0
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		c.Key(key)
		items := make([]nav.TabItem, 0, len(labels))
		for _, label := range labels {
			items = append(items, nav.TabItem{Label: label})
		}
		selected := kind
		nav.Tabs(c, &selected, nav.TabsProps{
			Items:   items,
			Variant: nav.TabsRounded,
			Size:    lobetheme.Small,
		}, nil)
		if selected != kind && selected >= 0 && selected < len(labels) {
			kind, *sel = selected, selected
		}
		ui.Spacer(c).Grow(1)
		if trailing != nil {
			trailing(kind)
		}
	})
}

// profileListTrailing 画分段行右侧的计数与刷新，stored 指向真正存数据的列表。
func (a *app) profileListTrailing(c *ui.Context, ps *profileState, stored int) {
	st, _ := a.profileActiveContent(ps, stored)
	if st == nil {
		return
	}
	if st.loaded {
		display.Text(c, fmt.Sprintf("共 %d %s", profileContentTotal(st), profileContentUnits[stored]),
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
	}
	a.smallRefreshButton(c, st.loading, func() { a.reloadProfileActiveContent(stored) })
}

// profileListItems 画分段下方的正文：合集筛选、加载状态、行列表与续拉。
func (a *app) profileListItems(c *ui.Context, ps *profileState, stored int) {
	st, spec := a.profileActiveContent(ps, stored)
	if st == nil {
		return
	}
	// 合集下拉常驻在「发布」分段上方；筛选生效时下面整段换成筛选结果。
	if stored == profileContentTopics {
		a.profileTopicCollRow(c, ps)
	}
	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Large})
			display.Text(c, "正在加载"+spec.Label+"…",
				display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 1})
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			feedback.Result(c, feedback.ResultProps{
				Status:   feedback.ResultError,
				Title:    "加载失败",
				SubTitle: st.err,
				Extra: func() {
					if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.loadProfileActiveContent(stored, 1)
					}
				},
			})
		})
		return
	case len(st.items) == 0:
		empty := profileContentEmpty[stored]
		// 筛选后的空状态要说清是「这个合集里没有」，别让人以为是没发过帖。
		if stored == profileContentTopics && ps.topicColl != "" {
			empty = [2]string{"这个合集里没有帖子。", "换一个合集，或者选「全部合集」。"}
		}
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        profileContentEmptyIcons[stored],
				Title:       empty[0],
				Description: empty[1],
			})
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
	// 列表与底部的续拉行同属内容区，行距按 Lobe 列表的 8 给。
	ui.Column(c).Grow(1).MinHeight(0).Gap(8).Children(func() {
		ui.List(c, &st.list, len(items), func(i int) {
			a.profileContentRow(c, stored, items[i])
		}).Grow(1).MinHeight(0).Gap(8)
		if spec.Paging == pagingPages {
			a.profileMoreContentWith(c, stored, st, a.loadProfileActiveContent)
		}
	})
}

// profileListBody 画一个带二级分段与列表正文的页。sel 指向要写的二级分段，
// storage 把它映射到真正存数据的列表下标；「内容」与「资产」共用这一套画法。
// trailing / body 非空时替换分段行右侧信息与正文（评论有自己的列表状态）。
func (a *app) profileListBody(c *ui.Context, ps *profileState,
	sel *int, labels []string, key string, storage func(int) int, collBrowse bool,
	trailing func(int), body func()) {
	kind := *sel
	if kind < 0 || kind >= len(labels) {
		kind, *sel = 0, 0
	}
	stored := storage(kind)

	// 点开一个合集时整块换成它里面的帖子，「返回合集列表」再回到这一页的分段。
	if collBrowse && stored == profileContentCollections && ps.collBrowse != nil {
		a.collectionTopicsBody(c, ps.collBrowse, a.openProfileItem, func() {
			ps.collBrowse = nil
		})
		return
	}

	// 分段与正文是两层关键部分，各留一段 Lobe 的呼吸间距；原先紧贴在一起，
	// 分段的下边线看着像连到了下拉框，列表首行也顶着筛选控件。根节点必须拿到
	// 固定的可视高度（Grow + MinHeight(0)）：只写 FillWidth 的话，列表会按全部
	// 内容撑高，后面的行画到窗口外面，滚轮也跟着失效。
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Gap(12).Children(func() {
		a.profileListTabs(c, key, labels, sel, func(kind int) {
			if trailing != nil {
				trailing(kind)
				return
			}
			a.profileListTrailing(c, ps, storage(kind))
		})
		stored := storage(*sel)
		if body != nil {
			body()
			return
		}
		a.profileListItems(c, ps, stored)
	})
}

// profileContentBody 是「内容」页：发布、合集与评论共用同一个二级分段条。
// 评论有自己独立的列表与排序，但和另外两段一样直接画在选中的分段下面，不再
// 跳进单独的「评论与回复」页面——那样分段条会消失，回不去另外两段。
func (a *app) profileContentBody(c *ui.Context, ps *profileState) {
	a.profileListBody(c, ps, &ps.contentKind, profileContentLabels[:],
		"profile-content-tabs", profileContentStorage, true,
		func(kind int) {
			if kind == profileContentComments {
				a.profileReplyTrailing(c, ps)
				return
			}
			a.profileListTrailing(c, ps, profileContentStorage(kind))
		},
		func() {
			if ps.contentKind == profileContentComments {
				a.profileRepliesBody(c, ps)
				return
			}
			a.profileListItems(c, ps, profileContentStorage(ps.contentKind))
		})
}

// profileReplyTrailing 画「评论」分段右侧的计数与刷新：评论有独立的回复状态，
// 不在 profileContent 那份列表里。
func (a *app) profileReplyTrailing(c *ui.Context, ps *profileState) {
	if ps.replyLoaded {
		display.Text(c, fmt.Sprintf("共 %d 条", ps.replyTotal),
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
	}
	a.smallRefreshButton(c, ps.replyLoading, func() {
		if name := a.auth.username(); name != "" {
			a.loadProfileReplies(name, 1)
		} else {
			// 用户名还没读到时先重读账号资料，下一次帧里再补上评论请求。
			a.reloadProfile()
		}
	})
}

// profileAssetsBody 是「资产」页：收藏与书签。两段都走通用列表，只是数据
// 分别来自 /v1/me/favorites 与 /v1/me/bookmarks。
func (a *app) profileAssetsBody(c *ui.Context, ps *profileState) {
	a.profileListBody(c, ps, &ps.assetKind, profileAssetLabels[:],
		"profile-asset-tabs", profileAssetStorage, false, nil, nil)
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
// 稍后再看只在主窗口信息流里出现，这里走 rowWithNoWatchLater，不带那枚按钮。
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
		a.rowWithNoWatchLater(c, it, a.openProfileItem)
	}
}

// profileBookmarkRow 是「资产 - 书签」的一行：整行打开评论，右侧一枚
// 移除按钮。
func (a *app) profileBookmarkRow(c *ui.Context, it Item) {
	busy := a.profile != nil && a.profile.bookmarkBusy == it.CommentID
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			a.rowWithNoWatchLater(c, it, a.openProfileItem)
		})
		if busy {
			b := ui.Box(c).Size(28, 28).Center()
			b.Children(func() { lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small}) })
			b.Label("移除书签").Tooltip("从书签里移除这条评论")
			return
		}
		// 移除是危险动作：Lobe 的 ActionIcon 平时用弱化的错误色，悬停加深，
		// 图标按钮的标签与 tooltip 保持原来的无障碍名和说明。
		b := action.StyleActionIcon(c, ui.ButtonBase(c), bookmarkOnIcon, action.ActionIconProps{
			Size: lobetheme.Small, Danger: true,
		})
		b.Label("移除书签").Tooltip("从书签里移除这条评论")
		if b.Clicked() {
			a.removeProfileBookmark(it)
		}
	})
}

// profileCollectionRow 是「内容 - 合集」的一行：整行点开合集里的帖子，
// 右侧两枚图标按钮分别改名与删除。合集在站点上只有创建者能改，这里列的
// 本来就是当前账号创建的，所以不需要再判断归属。
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
			a.rowWithNoWatchLater(c, row, func(Item) { a.openProfileCollection(it) })
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

// profileMoreContentWith 是分页列表底部的续拉行：还有下一页时给按钮，续拉
// 失败时给出原因并可重试，已有内容保持不动。load 由调用方决定，合集筛选时
// 要派给筛选列表，不能回到 profileContentSpecs 的端点。
func (a *app) profileMoreContentWith(c *ui.Context, kind int, st *tabState, load func(int, int)) {
	tk := lobetheme.TokensOf(c)
	switch {
	case st.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "加载更多失败："+st.moreErr, display.TextProps{
				Type: display.TextDanger, FontSize: 11, Rows: 2,
			}).Grow(1)
			a.smallAction(c, "重试", false, func() { load(kind, st.page) })
		})
	case st.loading && len(st.items) > 0:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
			display.Text(c, "正在加载更多…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		})
	// 服务端给了总数且已经全部拿到时不再给按钮：留着它只会让人点一下拿到
	// 空页，白等一次请求。
	case !st.noMore && (st.totalPages == 0 || st.page < st.totalPages) &&
		!(st.total > 0 && len(st.items) >= st.total):
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			b := action.Button(c, "加载更多", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Default,
			})
			b.TextColor(tk.Primary)
			if b.Clicked() {
				load(kind, st.page+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}

/* ------------------------------------------------------------------
   内容 - 评论：我发表过的评论与回复
---------------------------------------------------------------- */

// profileRepliesBody 只画评论分段选中的正文：二级分段条与右侧的计数、刷新由
// profileListBody / profileReplyTrailing 提供，这里不再自己占一行标题。
func (a *app) profileRepliesBody(c *ui.Context, ps *profileState) {
	name := a.auth.username()
	if name == "" {
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			display.Empty(c, display.EmptyProps{
				Title:       "还不知道当前账号的用户名",
				Description: "点右上角「刷新」重新读取账号资料后再试。",
			})
		})
		return
	}

	switch {
	case ps.replyLoading && len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Large})
			display.Text(c, "正在加载我的回复…",
				display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 1})
		})
		return
	case ps.replyErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			feedback.Result(c, feedback.ResultProps{
				Status:   feedback.ResultError,
				Title:    "加载失败",
				SubTitle: ps.replyErr,
				Extra: func() {
					if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.loadProfileReplies(name, 1)
					}
				},
			})
		})
		return
	case len(ps.replies) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.MessageCircle(),
				Title:       "还没有发表过回复。",
				Description: "在帖子详情页底部的评论框里发表评论。",
			})
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
	// 续拉状态沿用信息流的扁平底栏：只有加载中与失败才出现，不画分割线，
	// 也没有「加载更多」按钮——滚到底自动拉下一页。失败后停在这里等重试，
	// 避免每次重建都再发一次注定失败的请求。
	a.loadMoreFooter(c, ps.replyMoreErr, ps.replyLoading, func() {
		a.loadProfileReplies(name, ps.replyPage)
	})
	if a.autoLoad && ps.replyLoaded && !ps.replyLoading && ps.replyMoreErr == "" &&
		!ps.replyNoMore && len(ps.replies) > 0 && ps.replyList.AtEnd() {
		a.loadProfileReplies(name, ps.replyPage+1)
	}
}

func (a *app) profileReplyRow(c *ui.Context, it Item) {
	// 整行是扁平列表行：不画圆角与整圈描边，只留底部分隔线，悬停底色跟随主题。
	row := popupRow(c, true).FillWidth().Padding(10, 12)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			display.Text(c, it.Title, display.TextProps{FontSize: 12, Rows: 2})
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
				display.Text(c, strings.Join(parts, " · "),
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			}
		})
	})
	row.Label(it.Title)
	if it.ShortID != "" {
		if row.Clicked() {
			a.openProfileDetail(it)
		}
	} else if it.URL != "" {
		if row.Clicked() {
			a.openExternal(it.URL)
		}
	}
}

/* ------------------------------------------------------------------
   Tab 6：设置（账号与权限 / 屏蔽设置）
---------------------------------------------------------------- */

// profileSettingsBody 是「设置」页的二级分段。账号与权限、屏蔽设置各自沿用
// 原来的页面，切换只换正文；进入屏蔽设置时的名单强制刷新由窗口统一处理。
func (a *app) profileSettingsBody(c *ui.Context, ps *profileState) {
	kind := ps.settingsKind
	if kind < 0 || kind >= profileSettingsSectionCount {
		kind, ps.settingsKind = profileSettingsAccount, profileSettingsAccount
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Padding(0, 0, 8, 0).Children(func() {
			c.Key("profile-settings-tabs")
			items := make([]nav.TabItem, 0, profileSettingsSectionCount)
			for _, label := range profileSettingsLabels {
				items = append(items, nav.TabItem{Label: label})
			}
			sel := kind
			nav.Tabs(c, &sel, nav.TabsProps{
				Items:   items,
				Variant: nav.TabsRounded,
				Size:    lobetheme.Small,
			}, nil)
			if sel != kind && sel >= 0 && sel < profileSettingsSectionCount {
				kind, ps.settingsKind = sel, sel
			}
		})
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			if kind == profileSettingsFilter {
				a.profileFilterBody(c, ps)
			} else {
				a.profileAccountBody(c, ps)
			}
		})
	})
}

func (a *app) profileAccountBody(c *ui.Context, ps *profileState) {
	u := ps.user
	list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			settingsSection(c, "账号", func() {
				a.profileAccountRow(c, "用户名", profileOrDash(u.Name, a.auth.username()))
				a.profileAccountRow(c, "用户 ID", profileUserNumber(u.UserNumber))
				a.profileAccountRow(c, "注册时间", profileRegisteredOrDash(u.CreatedAt))
				a.profileAccountRow(c, "等级", profilePositiveOrDash(ps.infoLoaded, u.Level))
				a.profileAccountRow(c, "粉丝 / 关注",
					profileFollowText(ps.infoLoaded, u.Followers, u.Following))
			})

			settingsSection(c, "授权范围", func() {
				scopes := a.auth.scopes()
				if len(scopes) == 0 {
					display.Text(c, "令牌里没有 scope 信息：可能是旧版本授权的会话，"+
						"重新登录一次即可拿到完整范围。",
						display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
				} else {
					display.Text(c, "当前令牌携带 "+fmt.Sprintf("%d", len(scopes))+" 项权限：",
						display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
					for _, s := range scopes {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							display.Text(c, s, display.TextProps{FontSize: 11, Rows: 1}).Width(110)
							display.Text(c, profileScopeLabel(s), display.TextProps{
								Type: display.TextSecondary, FontSize: 11, Rows: 1,
							}).Grow(1)
						})
					}
				}
			})

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				logout := action.Button(c, "退出登录", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Text,
				})
				logout.TextColor(lobetheme.TokensOf(c).Error)
				if logout.Clicked() {
					a.logoutAccount()
				}
				ui.Spacer(c).Grow(1)
			})
			display.Text(c, "撤销授权页在 2Libra 站点的「授权应用」里；撤销后需要重新登录本应用。",
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		})
	})
	list.Grow(1).MinHeight(0)
}

func (a *app) profileAccountRow(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		display.Text(c, label, display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		}).Width(90)
		display.Text(c, value, display.TextProps{FontSize: 12, Rows: 1})
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

// openProfileItem 是「个人中心」窗口里列表行的打开动作：站内帖子 / 评论回主窗口
// 正文（本窗口留着，读完还能接着看），其余交给系统浏览器。
func (a *app) openProfileItem(it Item) {
	switch {
	case it.ShortID != "":
		a.openProfileDetail(it)
	case it.URL != "":
		a.openExternal(it.URL)
	}
}

// openProfileDetail 从「个人中心」窗口打开一篇帖子或一条回复：把主窗口带到前台并
// 换到详情页，「个人中心」窗口本身留着不关 —— 读完后还能回到原来的标签页。
// 评论行会带上 CommentID，正文里据此定位楼层。
func (a *app) openProfileDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(it)
	a.showMainWindowAfterDetail()
}

/* ------------------------------------------------------------------
   合集：打开内容、改名、删除
--------------------------------------------------------------- */

// openProfileCollection 在「内容 - 合集」里点开一个合集：整块换成它里面
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
		a.requestLoginFor(toastProfile, "登录后才能修改合集。")
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
				if a.handleAuthErrorFor(toastProfile, err) {
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
		a.requestLoginFor(toastProfile, "登录后才能删除合集。")
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
				if a.handleAuthErrorFor(toastProfile, err) {
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
// 关掉之后才轮到删除确认。两者都用 Lobe 的 Modal：标题、✕ 关闭、取消/确认
// 与滚动区由组件提供，和发帖弹窗用的是同一套 token 与动画。
func (a *app) profileCollectionDialogs(c *ui.Context) {
	ps := a.profile
	if ps == nil {
		return
	}
	if ps.collEdit != "" {
		open := true
		ok := feedback.Modal(c, &open, feedback.ModalProps{
			Title:      "重命名合集",
			Width:      380,
			OkText:     "保存",
			CancelText: "取消",
		}, func() {
			ui.Column(c).FillWidth().Gap(10).Children(func() {
				field.Input(c, &ps.collDraft, field.InputProps{
					Placeholder: "合集名称", Size: lobetheme.Small,
				}).Label("合集名称").FillWidth().Disabled(ps.collBusy != "")
				display.Text(c, fmt.Sprintf("最多 %d 个字；改名不会改变合集里的帖子。", collectionNameMax),
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
			})
		})
		// 取消、✕、Esc 与点窗外都由 Modal 关窗：清空草稿状态。
		if !open {
			ps.collEdit, ps.collDraft = "", ""
			return
		}
		// busy 时 Modal 的确认按钮仍是亮的，但 submit 自己会拦住重复提交；
		// 成功后由异步回调清掉 collEdit，弹窗自然消失。
		if ok {
			a.submitRenameCollection()
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
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "删除合集？",
		Width:      420,
		OkText:     "确认删除",
		CancelText: "取消",
	}, func() {
		display.Text(c, "删除"+name+"只删合集本身：合集里的帖子会被取消挂载，"+
			"帖子、评论和浏览数都不受影响。删除不可恢复。",
			display.TextProps{Type: display.TextSecondary, Rows: 3})
	})
	if !open {
		ps.collDelete = ""
		return
	}
	if ok {
		a.submitDeleteCollection()
	}
}
