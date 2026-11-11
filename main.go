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
	gen        uint64
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

	detail       *detailState
	detailStack  []*detailState // 正文内联跳转的返回栈
	emojis       map[string]emojiInfo
	emojiLoading bool
	emojiTried   bool
	md           *markdown.Options

	// onReplyPosted is a hook invoked after a reply is posted (or fails),
	// carrying the created reply and any error. It runs wherever publish
	// runs (the UI thread in the app, the network goroutine under headless
	// tests). Nil in production; used by tests to await completion.
	onReplyPosted func(created replyItem, err error)
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
		auth:     auth,
		client:   &Client{Auth: auth},
		tabs:     make([]*tabState, len(tabSpecs)),
		lastTab:  -1,
		navSel:   tabID(0),
		pageName: "list",
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
	if st.loading {
		return
	}
	if spec.NeedsAuth && !a.auth.loggedIn() {
		st.err = "需要登录后才能查看「" + spec.Label + "」"
		return
	}
	st.loading = true
	st.err = ""
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
				st.err = friendlyError(err)
				if page == 1 {
					st.items = nil
				}
				return
			}
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
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

func (a *app) onAuthChanged(err error) {
	if err != nil {
		a.loginMsg = friendlyError(err)
		a.status = a.loginMsg
	} else {
		a.loginMsg = "已登录 · " + a.auth.username()
		a.status = a.auth.verifyStatus()
		if a.status == "" {
			a.status = "登录成功"
		}
		a.pageName = "list"
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
	a.ensureLoaded()
}

// homeTheme 是首页使用的浅色主题：纯白背景（页面与侧边栏均白）+ 指定
// 边框色 oklab(0.21 0.00164224 -0.00577088 / 0.1)（即 oklch(0.21 0.006
// 285.885) 的 10% 不透明版本），其余文字/强调色沿用浅色主题，保证整体
// 可读且风格统一。视觉结构由半透明 oklch 边框与分隔线承担，呈现干净的
// 卡片式列表。
func homeTheme() *ui.Theme {
	th := ui.LightTheme()
	th.Background = ui.Hex("#ffffff")
	th.Surface = ui.Hex("#ffffff")
	th.Border = ui.Oklch(0.21, 0.006, 285.885).Alpha(0.1)
	return th
}

func (a *app) view(c *ui.Context) {
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

	// 首页使用白色背景 + 指定 oklch 边框色的浅色主题。SetTheme 只作用于
	// 当前帧（每帧从系统主题重置），因此详情页/登录页仍保持原主题。
	c.SetTheme(homeTheme())
	t = c.Theme()

	// 左右布局：左侧（宽 150）放品牌顶栏 + 功能导航；右侧放内容区。
	ui.Row(c).Fill().Children(func() {
		// 左侧导航栏：顶部品牌/账号区 + 带图标的侧边栏
		ui.Column(c).Width(150).Shrink(0).FillHeight().Background(t.Surface).Children(func() {
			a.sidebarHeader(c)
			ui.Divider(c)
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
					for i, s := range tabSpecs {
						if !s.NeedsAuth {
							continue
						}
						ui.SidebarItem(c, tabID(i), tabIcons[i], s.Label)
					}
				})
			}).Grow(1)
			// 侧边栏选中变化 → 同步到 a.tab（用于 body/footer/加载逻辑）
			if sb.Changed() {
				if i, ok := tabFromID(a.navSel); ok && i != a.tab {
					a.tab = i
				}
			}
			if a.tab != a.lastTab {
				a.lastTab = a.tab
				a.status = ""
			}
		})
		ui.Divider(c)
		// 右侧内容区：正文列表 + 底部状态栏
		ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
			a.body(c)
			ui.Divider(c)
			a.footer(c)
		})
	})
	a.ensureLoaded()
}

// sidebarHeader 是左侧导航栏顶部的品牌与账号区（原顶栏收进左侧）。
func (a *app) sidebarHeader(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(12, 12).Gap(8).Children(func() {
		ui.Text(c, "2Libra").FontSize(19).Bold()
		who := "未登录 · 匿名可读"
		if a.auth.loggedIn() {
			who = "已登录"
			if name := a.auth.username(); name != "" {
				who += " · " + name
			}
		}
		ui.Text(c, who).FontSize(11).TextColor(t.TextMuted).SingleLine()
		if a.auth.loggedIn() {
			if ui.Button(c, "退出").Clicked() {
				a.auth.Logout()
			}
		} else if ui.Button(c, "登录").Clicked() {
			a.pageName = "login"
			a.loginMsg = ""
		}
	})
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
				a.pageName = "login"
				a.loginMsg = ""
			}
		})
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
					a.pageName = "login"
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
	// 无限滚动：滚动到列表底部且还有下一页时，自动加载更多，不再依赖
	// 页码按钮。「我的帖子 / 我的合集」不分页、最新评论服务端忽略 page，
	// 因此只对 pagingPages 的 tab 触发。
	if spec.Paging == pagingPages && st.loaded && !st.loading && !st.noMore &&
		len(st.items) > 0 && st.list.AtEnd() {
		hasMore := st.totalPages == 0 || st.page < st.totalPages
		if hasMore {
			a.load(a.tab, st.page+1)
		}
	}
}

func (a *app) row(c *ui.Context, it Item) {
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

func (a *app) footer(c *ui.Context) {
	t := c.Theme()
	st := a.tabs[a.tab]
	spec := tabSpecs[a.tab]
	ui.Row(c).Padding(8, 16).Gap(10).AlignItems(ui.Center).Children(func() {
		page := st.page
		if page < 1 {
			page = 1
		}
		ui.Textf(c, "%s · 第 %d 页 · %d 条", spec.Label, page, st.total).
			FontSize(11).TextColor(t.TextMuted).Grow(1)
		if a.status != "" {
			ui.Text(c, a.status).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		if ui.Button(c, "上一页").Disabled(st.page <= 1 || st.loading).Clicked() {
			a.load(a.tab, st.page-1)
		}
		hasMore := spec.Paging == pagingPages && st.loaded && !st.loading &&
			len(st.items) > 0 && (st.totalPages == 0 || st.page < st.totalPages)
		if ui.Button(c, "下一页").Disabled(!hasMore || st.loading).Clicked() {
			a.load(a.tab, st.page+1)
		}
		if ui.Button(c, "刷新").Clicked() {
			a.reloadCurrent()
		}
	})
}

func (a *app) loginView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(20, 24).Gap(12).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				a.pageName = "list"
			}
			ui.Text(c, "登录 2Libra").FontSize(18).Bold()
			ui.Spacer(c).Grow(1)
			if a.auth.loggedIn() {
				ui.Text(c, "当前："+a.auth.username()).FontSize(12).TextColor(t.TextMuted)
				if ui.Button(c, "退出登录").Clicked() {
					a.auth.Logout()
				}
			}
		})
		ui.Divider(c)
		ui.Textf(c, "client_id：%s", clientID).FontSize(12).TextColor(t.TextMuted)
		ui.Textf(c, "redirect_uri：%s", defaultRedirectURI).FontSize(12).TextColor(t.TextMuted)
		ui.Textf(c, "scope：%s", defaultScopes).FontSize(12).TextColor(t.TextMuted)
		if a.loginMsg != "" {
			ui.Text(c, a.loginMsg).TextColor(t.TextMuted).MaxLines(4)
		}
		if ui.PrimaryButton(c, "打开浏览器授权").Clicked() {
			a.startLogin()
		}
		ui.Divider(c)
		ui.Text(c, "浏览器同意后会通过 "+defaultRedirectURI+" 跳回应用；若系统没有自动跳回，复制地址栏完整链接粘贴到这里，完成同一次授权：").FontSize(12).TextColor(t.TextMuted)
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
		if v := a.auth.verifyStatus(); v != "" {
			ui.Text(c, v).FontSize(11).TextColor(t.TextMuted)
		}
	})
}

// handleCallbackURL receives the OS deep link. The PKCE verifier is held in
// memory, so a callback that cold-started the app cannot resume the login.
func (a *app) handleCallbackURL(raw string) {
	if !isCallbackLink(raw) {
		return // some other deep link, not an OAuth callback
	}
	if err := a.auth.Complete(raw); err != nil {
		a.loginMsg = "回调无法完成登录：" + err.Error() + "。如果应用是被浏览器链接刚启动的，PKCE 会话不在本进程，请点「打开浏览器授权」重新发起。"
		a.status = a.loginMsg
		a.pageName = "login"
		return
	}
	a.loginMsg = "已收到系统回调，正在用授权码换取令牌…"
}

func (a *app) startLogin() {
	authorizeURL, err := a.auth.Begin(context.Background())
	if err != nil {
		a.loginMsg = err.Error()
		return
	}
	a.loginMsg = "已在浏览器打开授权页。同意后浏览器会通过 " + defaultRedirectURI + " 跳回应用；若没有跳回，请复制地址栏完整链接粘贴到下方。"
	a.srv.OpenURL(authorizeURL) // URL 服务在 UI 线程调用
}

func main() {
	a := newApp("")
	a.autoLoad = true
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
		log.Printf("运行时注册 %s 未成功（打包版由 mygo.json 的 urlSchemes 声明；仍可手动粘贴回调链接）：%v", appIdentifier, err)
	}
	mygo.App.OnOpenURL(func(raw string) {
		a.publish(func() { a.handleCallbackURL(raw) })
	})
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "2Libra 阅读器",
			Width:     1080,
			Height:    720,
			MinWidth:  760,
			MinHeight: 520,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		if a.auth.loggedIn() {
			go func() {
				_ = a.auth.verify(context.Background(), a.auth.accessToken())
				a.publish(func() { a.status = a.auth.verifyStatus() })
			}()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal("应用启动失败：", err)
	}
}
