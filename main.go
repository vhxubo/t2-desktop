package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type tabState struct {
	list       ui.ListState
	items      []Item
	page       int
	total      int
	totalPages int
	loaded     bool
	loading    bool
	noMore     bool // 分页已到底（空页 / 无新增），用于无限滚动停止
	err        string
	// moreErr 是「续拉下一页」失败的原因：首屏已经有内容时，续拉失败不能
	// 把整页换成错误页，只在新内容该出现的位置提示并可重试。
	moreErr string
	gen     uint64

	// 通知 tab 专用：分段筛选、正在提交已读的 id、勾选批量与操作错误。
	filter     int
	reading    map[string]bool
	selected   map[string]bool
	markingAll bool
	actionErr  string
}

// app holds only data and widget state. Never store ui.Element or *Context:
// background work publishes results through Window.Update.
type app struct {
	auth     *AuthManager
	client   *Client
	win      *mygo.Window
	srv      ui.Services
	autoLoad bool

	tab     int
	lastTab int
	navSel  string // 左侧导航栏选中项的 id（"t"+序号），与 tab 保持同步
	tabs    []*tabState
	reqSeq  uint64

	pageName    string // list / detail / login
	status      string
	loginMsg    string
	callbackURL string
	// 登录：点「登录」直接打开浏览器，loginPending 表示正在等系统跳回；
	// 只有失败或用户主动粘贴回调时才进入 login 页。
	loginPending bool
	// 头像位图与来源地址：登录完成后按 token 里缓存的地址异步加载一次，
	// 标题栏先显示姓名首字母，图片到了再替换。
	avatar        *ui.Bitmap
	avatarURL     string
	avatarLoading bool
	// schemeWarn 是「系统没有把回调协议关联到本应用」的说明，仅在这种情况下
	// 非空：它决定了浏览器授权后能不能自动跳回，得让用户在登录页看见。
	schemeWarn string

	detail       *detailState
	detailStack  []*detailState // 正文内联跳转的返回栈
	emojis       map[string]emojiInfo
	emojiLoading bool
	emojiTried   bool
	// articleImages 缓存正文图片（表情与配图）的下载结果，键是图片地址。
	// 只在 UI 线程读写；后台下载完成后通过 publish 回填。
	articleImages map[string]*articleImageEntry
	md            *markdown.Options

	// 通知 / 消息中心：未读数、SSE 长连接与原生通知。
	unread        int
	unreadLoading bool
	unreadGen     uint64
	streamLive    bool
	watchCancel   context.CancelFunc
	watchGen      uint64
	notifSeen     map[string]bool
	notifBaseline bool
	notifPollGen  uint64
	notifTimer    *time.Timer
	// notify 是平台通知函数；在应用就绪后才装上，headless 测试保持 nil，
	// 因此不会触碰真实的系统通知。
	notify       func(nativeNotice) error
	nativeNotify bool // 原生通知开关（默认开）
	// noticeTargets 是「原生通知 id → 目标地址」的映射：系统通知的点击回调
	// 只带 id，靠它把点击落回对应的帖子 / 评论，进程重启后再点击则回到站点。
	noticeTargets map[string]string

	// onReplyPosted is a hook invoked after a reply is posted (or fails),
	// carrying the created reply and any error. It runs wherever publish
	// runs (the UI thread in the app, the network goroutine under headless
	// tests). Nil in production; used by tests to await completion.
	onReplyPosted func(created replyItem, err error)
	// onNotificationsRead 是「按条已读」落账后的测试钩子：headless 测试没有
	// 窗口，publish 在请求协程里直接执行，靠它把结果同步回测试。
	onNotificationsRead func(ids []string)
}

// tabID / tabFromID 把 tab 序号与侧边栏条目的 id 互相映射。
func tabID(i int) string { return "t" + strconv.Itoa(i) }

func tabFromID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 't' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 0 || n >= len(tabSpecs) {
		return 0, false
	}
	return n, true
}

func newApp(dir string) *app {
	auth := newAuthManager(dir)
	a := &app{
		auth:         auth,
		client:       &Client{Auth: auth},
		tabs:         make([]*tabState, len(tabSpecs)),
		lastTab:      -1,
		navSel:       tabID(0),
		pageName:     "list",
		nativeNotify: true,
	}
	for i := range a.tabs {
		a.tabs[i] = &tabState{}
	}
	a.auth.onChange = func(err error) {
		a.publish(func() { a.onAuthChanged(err) })
	}
	a.md = newMarkdownOptions(a)
	return a
}

// publish marshals a state change onto the UI thread. In headless tests
// there is no window, so the change applies directly.
func (a *app) publish(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	a.win.Update(fn)
}

func (a *app) load(idx, page int) {
	if idx < 0 || idx >= len(a.tabs) || page < 1 {
		return
	}
	st := a.tabs[idx]
	spec := tabSpecs[idx]
	if spec.Kind == kindNotification {
		// 分段筛选直接改接口参数，不额外请求参数名。
		spec.Query = notificationsQuery(st.filter)
	}
	if st.loading {
		return
	}
	if spec.NeedsAuth && !a.auth.loggedIn() {
		st.err = "需要登录后才能查看「" + spec.Label + "」"
		return
	}
	st.loading = true
	st.err = ""
	st.moreErr = ""
	st.page = page
	a.reqSeq++
	gen := a.reqSeq
	st.gen = gen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetch(ctx, spec, page)
		a.publish(func() {
			if st.gen != gen { // a newer request superseded this one
				return
			}
			st.loading = false
			if err != nil {
				if page == 1 {
					st.err = friendlyError(err)
					st.items = nil
				} else {
					// 已有内容时续拉失败：保留当前列表，在列表底部提示。
					st.moreErr = friendlyError(err)
				}
				return
			}
			st.moreErr = ""
			if page == 1 {
				st.items = res.Items
				st.list.ScrollTo(0, ui.Start)
				st.noMore = len(res.Items) == 0
			} else {
				// 页码分页可能重复：按资源 ID 去重
				seen := make(map[string]bool, len(st.items))
				for _, it := range st.items {
					seen[it.Key] = true
				}
				added := 0
				for _, it := range res.Items {
					if !seen[it.Key] {
						st.items = append(st.items, it)
						seen[it.Key] = true
						added++
					}
				}
				// 服务端返回空页或没有新增条目：标记到底，避免无限加载
				st.noMore = added == 0
			}
			st.total = res.Total
			st.totalPages = res.TotalPages
			st.page = page
			st.loaded = true
		})
	}()
}

// ensureLoaded is a guarded job: it starts at most one request per tab
// until the tab has been loaded, and can be called from every rebuild.
func (a *app) ensureLoaded() {
	if !a.autoLoad {
		return
	}
	st := a.tabs[a.tab]
	spec := tabSpecs[a.tab]
	if st.loading || st.loaded {
		return
	}
	if spec.NeedsAuth && !a.auth.loggedIn() {
		return
	}
	a.load(a.tab, 1)
}

func (a *app) reloadCurrent() {
	st := a.tabs[a.tab]
	if st.loading {
		return
	}
	st.loaded = false
	st.items = nil
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

func (a *app) onAuthChanged(err error) {
	a.loginPending = false
	switch {
	case err != nil:
		a.loginMsg = friendlyError(err)
		a.status = a.loginMsg
		// 失败时才把回退页摆出来：那里有「重新打开浏览器」和粘贴回调链接，
		// 正常授权成功不进这一页。
		a.pageName = "login"
	case a.auth.loggedIn():
		a.loginMsg = "已登录"
		if name := a.auth.username(); name != "" {
			a.loginMsg += " · " + name
		}
		a.status = a.auth.verifyStatus()
		if a.status == "" {
			a.status = "登录成功"
		}
		if a.pageName == "login" {
			a.pageName = "list"
		}
		a.ensureAvatar()
	default:
		// 退出登录也会走 onChange(nil)：不能把它当成一次登录成功。
		a.loginMsg = "已退出登录"
		a.status = ""
		a.avatar, a.avatarURL, a.avatarLoading = nil, "", false
	}
	// 私人 tab 的缓存与登录态绑定，一律丢弃后按需重新拉取。
	for i, s := range tabSpecs {
		if s.NeedsAuth {
			a.tabs[i] = &tabState{}
		}
	}
	if !a.auth.loggedIn() && tabSpecs[a.tab].NeedsAuth {
		a.tab = 0
		a.lastTab = 0
		a.navSel = tabID(0)
	}
	if a.auth.loggedIn() {
		a.refreshUnread()
		a.startNotificationWatch()
	} else {
		a.stopNotificationWatch()
		a.unread = 0
		a.unreadLoading = false
		a.notifSeen = nil
		a.notifBaseline = false
		// 退出后系统里可能还留着通知，但目标属于上一个账号，一律不再解析。
		a.noticeTargets = nil
	}
	a.ensureLoaded()
}

// homeTheme 是全应用使用的浅色主题（首页、登录页、详情页一致）：纯白背景
// （页面、侧边栏与自绘标题栏均白）+ 指定边框色 oklab(0.21 0.00164224
// -0.00577088 / 0.1)（即 oklch(0.21 0.006 285.885) 的 10% 不透明版本），
// 其余文字/强调色沿用浅色主题，保证整体可读且风格统一。视觉结构由半透明
// oklch 边框与分隔线承担，呈现干净的卡片式列表。
func homeTheme() *ui.Theme {
	th := ui.LightTheme()
	th.Background = ui.Hex("#ffffff")
	th.Surface = ui.Hex("#ffffff")
	th.Border = ui.Oklch(0.21, 0.006, 285.885).Alpha(0.1)
	return th
}

func (a *app) view(c *ui.Context) {
	// 整个应用统一使用白色主题（首页、登录页、详情页一致）。SetTheme 只
	// 作用于当前帧，每帧从系统主题重置，所以要在建树前先定主题、再取
	// 根背景：否则深色系统主题下根背景会是深色，与页面白底之间露出缝。
	c.SetTheme(homeTheme())
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)

	switch a.pageName {
	case "login":
		a.loginView(c)
		return
	case "detail":
		a.detailView(c)
		return
	}

	// 顶部自绘标题栏（可拖拽、避让系统窗口按钮）+ 下方 侧边栏/内容 布局。
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Divider(c)
		ui.Row(c).Fill().MinHeight(0).Children(func() {
			// 左侧导航栏：带图标的侧边栏
			ui.Column(c).Width(150).Shrink(0).FillHeight().Background(t.Surface).Children(func() {
				sb := ui.Sidebar(c, &a.navSel, func() {
					// 公开功能
					ui.SidebarSection(c, "浏览", nil, func() {
						for i, s := range tabSpecs {
							if s.NeedsAuth {
								continue
							}
							ui.SidebarItem(c, tabID(i), tabIcons[i], s.Label)
						}
					})
					// 「我的」单独成组：需登录后才能查看的功能
					ui.SidebarSection(c, "我的", nil, func() {
						// 通知排在最前：未读消息是最常用的入口，角标显示未读数。
						if idx := notificationTabIndex(); idx >= 0 {
							item := ui.SidebarItem(c, tabID(idx), tabIcons[idx], tabSpecs[idx].Label)
							if a.unread > 0 {
								item.Children(func() {
									ui.Badge(c, strconv.Itoa(a.unread))
								})
							}
						}
						for i, s := range tabSpecs {
							if !s.NeedsAuth || s.Kind == kindNotification {
								continue
							}
							ui.SidebarItem(c, tabID(i), tabIcons[i], s.Label)
						}
					})
				}).Grow(1)
				// 侧边栏选中变化 → 同步到 a.tab（用于 body 加载逻辑）
				if sb.Changed() {
					if i, ok := tabFromID(a.navSel); ok && i != a.tab {
						a.tab = i
					}
				}
				if a.tab != a.lastTab {
					a.lastTab = a.tab
					a.status = ""
					if tabSpecs[a.tab].Kind == kindNotification {
						a.refreshUnread()
					}
				}
			})
			ui.Divider(c)
			// 右侧内容区：正文列表（无限滚动，无底部状态栏）
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				a.body(c)
			})
		})
	})
	a.ensureLoaded()
}

// 自绘标题栏的尺寸。32 DIP 是 Windows 标题栏的常规高度，窗口按钮 46×32
// 也与系统按钮同宽同高，文字和图标因此不会被压扁、裁切或挤在一起。
const (
	titleBarHeight   = 32
	titleButtonWidth = 46
)

// titleBar 是完全自绘的顶部标题栏：可拖拽移动窗口（双击最大化），左侧是
// 品牌与账号状态，右侧是最小化 / 最大化·还原 / 关闭按钮；系统标题栏与
// 系统按钮已由 TitleBarHidden + HideWindowButtons 隐藏。
//
// 配色全部取自当前主题：整个应用是白色主题，标题栏因此与页面同色，不再
// 是一条突兀的灰带，深色系统主题下也不会出现浅色条压着深色页面。
func (a *app) titleBar(c *ui.Context) {
	t := c.Theme()
	// bar 是系统窗口按钮占用的区域，HideWindowButtons 下为零。保留这段
	// 计算，将来若改回系统按钮，标题栏仍然给它们让出位置。
	bar := c.TitleBar()
	h := float32(titleBarHeight)
	if bar.Height > h {
		h = bar.Height
	}
	ui.Row(c).Height(h).FillWidth().AlignItems(ui.Stretch).
		Padding(0, 0, 0, bar.Left).Background(t.Surface).DragWindow().Children(func() {
		// 左段：品牌、账号状态与登录/退出，整体可随标题栏拖动。
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10, 0, 12).Gap(10).
			AlignItems(ui.Center).Children(func() {
			ui.Text(c, "T2 Desktop").FontSize(13).Bold().SingleLine().TextColor(t.Text)
			ui.Spacer(c).Grow(1)
			a.accountBar(c)
		})
		// 右段：自绘窗口控制按钮（最小化 / 最大化·还原 / 关闭），紧贴右上角。
		a.titleButton(c, titleMinimize, "最小化", func() {
			if a.win != nil {
				a.win.Minimize()
			}
		})
		if a.win != nil && a.win.IsMaximized() {
			a.titleButton(c, titleRestore, "还原", func() {
				a.win.ToggleMaximize()
			})
		} else {
			a.titleButton(c, titleMaximize, "最大化", func() {
				if a.win != nil {
					a.win.ToggleMaximize()
				}
			})
		}
		a.titleButton(c, titleClose, "关闭", func() {
			if a.win != nil {
				a.win.Close()
			}
		})
	})
}

// accountBar 是标题栏右侧的账号区。未登录时只有一个「登录」，点下去直接
// 打开系统浏览器，不再先进登录页（那一步既多余，也容易让人以为要再点一次）；
// 授权等待中显示进度与取消；登录后显示头像、用户名和「退出」。
func (a *app) accountBar(c *ui.Context) {
	t := c.Theme()
	switch {
	case a.loginPending:
		waiting := "等待浏览器授权…"
		if a.schemeWarn != "" {
			// 系统没注册回调协议时不会有自动跳回，别让人干等。
			waiting = "等待授权：系统未注册回调协议，授权后请点「粘贴回调」"
		}
		ui.Text(c, waiting).FontSize(12).TextColor(t.TextMuted).SingleLine()
		a.titleTextButton(c, "粘贴回调", func() { a.pageName = "login" })
		a.titleTextButton(c, "取消", func() { a.cancelLogin() })
	case a.auth.loggedIn():
		name := a.auth.username()
		if name != "" {
			ui.Avatar(c, name, a.avatar).Size(20, 20)
		}
		who := "已登录"
		if name != "" {
			who += " · " + name
		}
		ui.Text(c, who).FontSize(12).TextColor(t.TextMuted).SingleLine()
		a.titleTextButton(c, "退出", func() { a.auth.Logout() })
	default:
		ui.Text(c, "未登录 · 匿名可读").FontSize(12).TextColor(t.TextMuted).SingleLine()
		a.titleTextButton(c, "登录", func() { a.startLogin() })
	}
}

// titleGlyph 是窗口按钮上要画的图形。
type titleGlyph int

const (
	titleMinimize titleGlyph = iota
	titleMaximize
	titleRestore // 已最大化时显示的「还原」
	titleClose
)

// titleButton 画一个窗口按钮。图形用矢量线条绘制，不依赖字体里的字符：
// 「– / □ / ▢ / ✕」这类符号在不同字体下大小不一，有的字体还缺字，最大化
// 之后会显示成空白或豆腐块，正是标题栏「显示不正常」的来源。
func (a *app) titleButton(c *ui.Context, g titleGlyph, label string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Width(titleButtonWidth).Shrink(0).Background(ui.Transparent)
	ink := t.Text
	if b.Hovered() {
		if g == titleClose {
			b.Background(ui.Hex("#e81123")) // 关闭：红底白叉，与系统一致
			ink = ui.Hex("#ffffff")
		} else {
			b.Background(t.SurfaceHover)
		}
	}
	b.Draw(func(p *ui.Painter, r ui.Rect) {
		// r 是窗口坐标里的按钮矩形，图形要相对 r 偏移来画。
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		const half = 5 // 图标约 10×10 DIP
		switch g {
		case titleMinimize:
			p.Line(cx-half, cy+0.5, cx+half, cy+0.5, 1, ink)
		case titleMaximize:
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half, W: 2 * half, H: 2 * half}, ink, 0, 1)
		case titleRestore:
			// 前面的方框，加上后面方框露出的上边和右边。
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half + 2, W: 8, H: 8}, ink, 0, 1)
			p.Line(cx-half+2, cy-half, cx+half, cy-half, 1, ink)
			p.Line(cx+half, cy-half, cx+half, cy+half-2, 1, ink)
		case titleClose:
			p.StrokePolyline([][2]float32{{cx - half, cy - half}, {cx + half, cy + half}}, 1, ink)
			p.StrokePolyline([][2]float32{{cx + half, cy - half}, {cx - half, cy + half}}, 1, ink)
		}
	})
	b.Label(label).Tooltip(label).OnClick(onClick)
}

// titleTextButton 是标题栏里的紧凑文字按钮（登录/退出）：高度贴合标题栏、
// 悬停浅灰底，避免在矮标题栏里被原生 Button 的默认高度挤出。
func (a *app) titleTextButton(c *ui.Context, label string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Padding(5, 9).Center().Radius(6).Background(ui.Transparent)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.Text)
	})
	b.OnClick(onClick)
}

func (a *app) body(c *ui.Context) {
	t := c.Theme()
	st := a.tabs[a.tab]
	spec := tabSpecs[a.tab]

	if spec.NeedsAuth && !a.auth.loggedIn() {
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "「"+spec.Label+"」需要登录").FontSize(17).Bold()
			ui.Text(c, "该端点要求 read:me / profile 权限，登录后才会发起请求。").TextColor(t.TextMuted)
			if ui.PrimaryButton(c, "去登录").Clicked() {
				a.startLogin()
			}
		})
		return
	}
	// 通知有独立的头部、分段筛选与已读操作，整块交给它自己渲染。
	if spec.Kind == kindNotification {
		a.notificationsBody(c, st)
		return
	}
	if st.loading && len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(28, 28)
			ui.Text(c, "正在加载 "+spec.Label+"…").TextColor(t.TextMuted)
		})
		return
	}
	if st.err != "" {
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "加载失败").FontSize(16).Bold()
			ui.Text(c, st.err).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.reloadCurrent()
			}
			if strings.Contains(st.err, "登录") {
				if ui.Button(c, "去登录").Clicked() {
					a.startLogin()
				}
			}
		})
		return
	}
	if len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "暂无内容").TextColor(t.TextMuted)
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
		a.row(c, items[i])
	}).Grow(1).Padding(8, 16).Gap(14)
	a.loadMoreFooter(c, st.moreErr, st.loading, func() {
		a.load(a.tab, st.page)
	})
	// 无限滚动：滚动到列表底部且还有下一页时，自动加载更多，不再依赖
	// 页码按钮。「我的帖子 / 我的合集」不分页、最新评论服务端忽略 page，
	// 因此只对 pagingPages 的 tab 触发。续拉失败后停在提示上，等用户点
	// 「重试」，否则每次重建都会再发一次注定失败的请求。
	if spec.Paging == pagingPages && st.loaded && !st.loading && st.moreErr == "" &&
		!st.noMore && len(st.items) > 0 && st.list.AtEnd() {
		hasMore := st.totalPages == 0 || st.page < st.totalPages
		if hasMore {
			a.load(a.tab, st.page+1)
		}
	}
}

// loadMoreFooter 是无限滚动列表底部的续拉状态行：加载中显示进度，失败时
// 给出原因与「重试」。已有内容时，续拉失败只出现在这里，列表本身保持不动。
func (a *app) loadMoreFooter(c *ui.Context, errText string, loading bool, retry func()) {
	if errText == "" && !loading {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		if errText != "" {
			ui.Text(c, "加载更多失败："+errText).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			if ui.Button(c, "重试").Clicked() {
				retry()
			}
			return
		}
		ui.Spinner(c).Size(14, 14)
		ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
	})
}

func (a *app) row(c *ui.Context, it Item) {
	if it.Notif != nil {
		a.notificationRow(c, it)
		return
	}
	if it.Extra != nil {
		a.richRow(c, it)
		return
	}
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(14, 16).Radius(10).
		Background(t.Background).Border(1, t.Border)
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, it.Title).FontSize(14).Bold().MaxLines(2)
			if it.Subtitle != "" {
				ui.Text(c, it.Subtitle).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		if it.Meta != "" {
			ui.Text(c, it.Meta).FontSize(11).TextColor(t.TextMuted).
				SingleLine().Width(190).TextAlign(ui.End)
		}
	})
	// 有 short_id 的帖子/评论在应用内看正文和评论；其余条目保持打开原文。
	if it.ShortID != "" {
		row.OnClick(func() { a.openDetail(it) })
	} else if it.URL != "" {
		row.OnClick(func() { a.srv.OpenURL(it.URL) })
	}
}

// loginView 只是回退页：正常登录在标题栏点一下就打开浏览器、等系统跳回，
// 不需要来这里。它只在授权失败、或用户主动选择粘贴回调链接时出现。
func (a *app) loginView(c *ui.Context) {
	t := c.Theme()
	a.titleBar(c)
	ui.Divider(c)
	ui.Column(c).Fill().Padding(20, 24).Gap(12).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				a.pageName = "list"
			}
			ui.Text(c, "登录 2Libra").FontSize(18).Bold()
			ui.Spacer(c).Grow(1)
			if name := a.auth.username(); name != "" {
				ui.Text(c, "当前："+name).FontSize(12).TextColor(t.TextMuted)
				if ui.Button(c, "退出登录").Clicked() {
					a.auth.Logout()
				}
			}
		})
		ui.Divider(c)
		if a.schemeWarn != "" {
			ui.Text(c, a.schemeWarn).FontSize(12).TextColor(t.Danger).MaxLines(4)
		}
		if a.loginMsg != "" {
			ui.Text(c, a.loginMsg).TextColor(t.TextMuted).MaxLines(4)
		}
		if ui.PrimaryButton(c, "打开浏览器授权").Clicked() {
			a.startLogin()
		}
		ui.Text(c, "浏览器没有自动跳回？把地址栏里的完整链接粘贴到这里，完成同一次授权：").
			FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.callbackURL).Label("回调链接").Placeholder(defaultRedirectURI + "?code=...&state=...").FillWidth()
		if ui.Button(c, "用回调链接完成登录").Clicked() {
			if err := a.auth.Complete(a.callbackURL); err != nil {
				a.loginMsg = err.Error()
			} else {
				a.loginMsg = "已收到回调，正在用授权码换取令牌…"
			}
		}
		ui.Divider(c)
		ui.Text(c, "本应用由第三方开发者独立开发运营，与 2Libra 无隶属或合作关系。2Libra 仅提供账号授权与接口，不对本应用的可用性、内容与数据处理负责。你可以随时在 2Libra 的「授权应用」页面撤销本应用的授权。应用内展示的内容版权归原作者所有，详情页保留作者昵称与原文链接。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(4)
		ui.Spacer(c).Grow(1)
	})
}

// handleCallbackURL receives the OS deep link. The PKCE verifier is held in
// memory, so a callback that cold-started the app cannot resume the login.
//
// The link is echoed to the console on every delivery: mygo dev forwards the
// app's stdout/stderr, and a callback the system never delivered and one it
// delivered in an unexpected spelling both look like "nothing happened" in
// the window. Only the console tells them apart.
func (a *app) handleCallbackURL(raw string) {
	raw = strings.TrimSpace(raw)
	if !hasCallbackScheme(raw) {
		log.Printf("深链：忽略非本应用协议的链接 %q", raw)
		return
	}
	log.Printf("深链：收到 %q", raw)
	if !isCallbackLink(raw) {
		// It is our scheme but not the callback path. Links carrying a code
		// are attempts at logging in: dropping them silently leaves the user
		// on the login page with no idea what happened, so say what arrived.
		if strings.Contains(raw, "code=") {
			a.loginMsg = "收到的系统回调地址与 " + defaultRedirectURI + " 不一致：" + raw + "。请把浏览器地址栏里的完整链接粘贴到下方完成登录。"
			a.status = a.loginMsg
			a.pageName = "login"
			return
		}
		return // some other deep link of this app, not an OAuth callback
	}
	if err := a.auth.Complete(raw); err != nil {
		a.loginMsg = "回调无法完成登录：" + err.Error() + "。如果应用是被浏览器链接刚启动的，PKCE 会话不在本进程，请点「打开浏览器授权」重新发起。"
		a.status = a.loginMsg
		a.pageName = "login"
		return
	}
	a.loginMsg = "已收到系统回调，正在用授权码换取令牌…"
}

// startLogin 打开系统浏览器并进入等待状态：不回登录页、不需要第二次点击，
// 系统回调（或用户粘贴的回调链接）完成授权后自行登录。
func (a *app) startLogin() {
	authorizeURL, err := a.auth.Begin(context.Background())
	if err != nil {
		a.loginMsg = err.Error()
		a.pageName = "login"
		return
	}
	a.loginPending = true
	a.loginMsg = "已在浏览器打开授权页，同意后会自动跳回。"
	a.status = a.loginMsg
	a.srv.OpenURL(authorizeURL) // URL 服务在 UI 线程调用
}

// cancelLogin 放弃本次授权：AuthManager 丢掉 PKCE 会话，界面回到未登录。
func (a *app) cancelLogin() {
	a.auth.Cancel()
	a.loginPending = false
	a.loginMsg = "已取消登录"
	a.status = a.loginMsg
}

func main() {
	a := newApp("")
	a.autoLoad = true
	// 深链回调先注册，再取单实例锁：浏览器跳回时系统拉起的是一个新进程，
	// 它把命令行转给第一个实例，转发可能早于下面几行执行。mygo 的
	// OnOpenURL 只在投递那一刻有监听者时才送达，注册晚了链接就被丢掉。
	mygo.App.OnOpenURL(func(raw string) {
		a.publish(func() { a.handleCallbackURL(raw) })
	})
	// On Windows/Linux the custom-scheme callback starts a second process;
	// hand its URL to the instance that holds the PKCE verifier.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// mygo.json declares the scheme for packaged builds (macOS bundle,
	// Linux desktop entry). Registering at run time also covers Windows
	// and apps launched without an installer. Failure only disables the
	// automatic jump back: pasting the callback link still works.
	if err := mygo.App.RegisterURLScheme(appIdentifier); err != nil {
		a.schemeWarn = "系统未把 " + appIdentifier + " 关联到本应用（" + err.Error() + "）：授权后浏览器不会自动跳回，请复制地址栏里的完整链接粘贴到下方。"
		log.Printf("运行时注册 %s 未成功（打包版由 mygo.json 的 urlSchemes 声明；仍可手动粘贴回调链接）：%v", appIdentifier, err)
	} else {
		log.Printf("回调协议已注册：%s", appIdentifier)
	}
	// 原生通知的点击只带回 id，由 openNotificationNotice 查回目标：帖子和
	// 评论在应用内打开，其余交给系统浏览器。注册在 Run 之前，因此连「点击
	// 通知才启动本进程」的那一次也能收到。
	mygo.App.OnNotificationClick(func(id string) {
		a.publish(func() { a.openNotificationNotice(id) })
	})
	// 站内读掉消息不会产生 notification_update，窗口回到前台时校准一次。
	mygo.App.OnDidBecomeActive(func() {
		a.publish(func() { a.onBecameActive() })
	})
	mygo.App.WhenReady(func() {
		// 原生通知要等应用就绪：Windows 走托盘气泡，macOS 首次会询问授权。
		// 头less 测试不经过这里，a.notify 保持 nil，不会触碰系统通知。
		if mygo.NotificationsSupported() {
			a.notify = showNativeNotice
		}
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:             "T2 Desktop",
			Width:             1080,
			Height:            720,
			MinWidth:          760,
			MinHeight:         520,
			StateKey:          "main",
			Content:           ui.View(a.view),
			TitleBarStyle:     mygo.TitleBarHidden, // 隐藏原生标题栏
			HideWindowButtons: true,                // 连系统按钮也隐藏，完全自绘最小化/最大化/关闭
		})
		if a.auth.loggedIn() {
			a.refreshUnread()
			a.startNotificationWatch()
			// 令牌里已经缓存了用户名与头像地址；这里再校准一次，老会话
			// 也能补上头像，并在加载完成后刷新标题栏。
			a.ensureAvatar()
			go func() {
				ctx := context.Background()
				token := a.auth.accessToken()
				_ = a.auth.verify(ctx, token)
				a.auth.fetchIdentity(ctx, token)
				a.publish(func() {
					a.status = a.auth.verifyStatus()
					a.ensureAvatar()
				})
			}()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal("应用启动失败：", err)
	}
}
