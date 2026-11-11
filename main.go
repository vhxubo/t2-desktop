package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type tabState struct {
	list       ui.ListState
	items      []Item
	page       int
	total      int
	totalPages int
	loaded     bool
	loading    bool
	err        string
	gen        uint64
}

type settingsDraft struct {
	clientID    string
	redirectURI string
	scopes      string
	port        string
	message     string
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
	tabs    []*tabState
	reqSeq  uint64

	pageName    string // list / settings / login
	status      string
	loginMsg    string
	callbackURL string
	manualToken string
	draft       settingsDraft
}

func newApp(dir string) *app {
	auth := newAuthManager(dir)
	a := &app{
		auth:     auth,
		client:   &Client{Auth: auth},
		tabs:     make([]*tabState, len(tabSpecs)),
		lastTab:  -1,
		pageName: "list",
	}
	for i := range a.tabs {
		a.tabs[i] = &tabState{}
	}
	s := auth.settingsSnapshot()
	a.draft = settingsDraft{
		clientID:    s.ClientID,
		redirectURI: s.RedirectURI,
		scopes:      s.Scopes,
		port:        strconv.Itoa(s.Port),
	}
	a.auth.onChange = func(err error) {
		a.publish(func() { a.onAuthChanged(err) })
	}
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
			} else {
				// 页码分页可能重复：按资源 ID 去重
				seen := make(map[string]bool, len(st.items))
				for _, it := range st.items {
					seen[it.Key] = true
				}
				for _, it := range res.Items {
					if !seen[it.Key] {
						st.items = append(st.items, it)
						seen[it.Key] = true
					}
				}
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
	}
	a.ensureLoaded()
}

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	if a.tab != a.lastTab {
		a.lastTab = a.tab
		a.status = ""
	}

	switch a.pageName {
	case "settings":
		a.settingsView(c)
		return
	case "login":
		a.loginView(c)
		return
	}
	ui.Column(c).Fill().Children(func() {
		a.header(c)
		ui.Divider(c)
		ui.Tabs(c, &a.tab, tabLabels()...)
		ui.Divider(c)
		a.body(c)
		ui.Divider(c)
		a.footer(c)
	})
	a.ensureLoaded()
}

func (a *app) header(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(12, 16).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "2Libra").FontSize(19).Bold()
		ui.Text(c, tabSpecs[a.tab].Label).FontSize(12).TextColor(t.TextMuted)
		ui.Spacer(c).Grow(1)
		who := "未登录 · 匿名可读"
		if a.auth.loggedIn() {
			who = "已登录"
			if name := a.auth.username(); name != "" {
				who += " · " + name
			}
		}
		ui.Text(c, who).FontSize(12).TextColor(t.TextMuted)
		if a.auth.loggedIn() {
			if ui.Button(c, "退出").Clicked() {
				a.auth.Logout()
			}
		} else if ui.Button(c, "登录").Clicked() {
			a.pageName = "login"
			a.loginMsg = ""
		}
		if ui.Button(c, "设置").Clicked() {
			s := a.auth.settingsSnapshot()
			a.draft = settingsDraft{
				clientID:    s.ClientID,
				redirectURI: s.RedirectURI,
				scopes:      s.Scopes,
				port:        strconv.Itoa(s.Port),
			}
			a.pageName = "settings"
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
			ui.Text(c, "暂无内容，点「刷新」重试").TextColor(t.TextMuted)
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
	}).Grow(1).Padding(4, 8)
}

func (a *app) row(c *ui.Context, it Item) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 14).Radius(8).
		Background(t.Surface).Border(1, t.Border)
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
	if it.URL != "" {
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

func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(20, 24).Gap(12).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				a.pageName = "list"
			}
			ui.Text(c, "设置 · OAuth 公开客户端（PKCE S256）").FontSize(18).Bold()
		})
		ui.Divider(c)
		ui.Text(c, "在 2Libra 网页 → 设置 → OAuth 应用 申请应用，客户端形态选「无服务端（公开客户端）」，并把下面的回调地址原样注册进去。公开客户端没有 client_secret，换 token 只靠 PKCE code_verifier。").
			TextColor(t.TextMuted).MaxLines(4)
		ui.TextInput(c, &a.draft.clientID).Label("client_id").Placeholder("审核通过后得到的 client_id").FillWidth()
		ui.TextInput(c, &a.draft.redirectURI).Label("redirect_uri").Placeholder("http://127.0.0.1:8765/cb").FillWidth()
		ui.TextInput(c, &a.draft.scopes).Label("scope（空格分隔）").Placeholder("read profile read:me").FillWidth()
		ui.TextInput(c, &a.draft.port).Label("本地监听端口").Placeholder("8765").Width(180)
		if ui.PrimaryButton(c, "保存").Clicked() {
			a.saveSettings()
		}
		if a.draft.message != "" {
			ui.Text(c, a.draft.message).TextColor(t.TextMuted)
		}
		ui.Divider(c)
		ui.Text(c, "说明：回环回调的端口在规范化时可能被剔除。若浏览器跳到 http://127.0.0.1/cb 而应用没自动收到，请复制地址栏完整链接，到登录页的「回调链接」里粘贴完成。没有 client_id 时也可以直接粘贴 access_token 体验需要登录的 tab。").
			TextColor(t.TextMuted).MaxLines(5)
		ui.Text(c, "令牌与设置保存在用户配置目录，不写入项目仓库；client_id 可用环境变量 TWOLIBRA_CLIENT_ID 提供。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	})
}

func (a *app) saveSettings() {
	port, err := strconv.Atoi(strings.TrimSpace(a.draft.port))
	if err != nil || port <= 0 || port >= 65536 {
		a.draft.message = "端口必须是 1~65535 的整数，已恢复为 8765"
		port = 8765
		a.draft.port = "8765"
	}
	s := Settings{
		ClientID:    strings.TrimSpace(a.draft.clientID),
		RedirectURI: strings.TrimSpace(a.draft.redirectURI),
		Scopes:      strings.TrimSpace(a.draft.scopes),
		Port:        port,
	}
	if s.Scopes == "" {
		s.Scopes = "read profile read:me"
	}
	if s.RedirectURI == "" {
		s.RedirectURI = "http://127.0.0.1:8765/cb"
	}
	if err := a.auth.updateSettings(s); err != nil {
		a.draft.message = "保存失败：" + err.Error()
		return
	}
	a.draft.message = "已保存。已有的登录态不会被覆盖。"
	a.status = "设置已保存"
}

func (a *app) loginView(c *ui.Context) {
	t := c.Theme()
	s := a.auth.settingsSnapshot()
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
		ui.Textf(c, "client_id：%s", maskConfig(s.ClientID)).FontSize(12).TextColor(t.TextMuted)
		ui.Textf(c, "redirect_uri：%s", s.RedirectURI).FontSize(12).TextColor(t.TextMuted)
		ui.Textf(c, "scope：%s", s.Scopes).FontSize(12).TextColor(t.TextMuted)
		if a.loginMsg != "" {
			ui.Text(c, a.loginMsg).TextColor(t.TextMuted).MaxLines(4)
		}
		if ui.PrimaryButton(c, "打开浏览器授权").Clicked() {
			a.startLogin()
		}
		ui.Divider(c)
		ui.Text(c, "如果浏览器没有自动跳回应用，复制地址栏完整链接粘贴到这里：").FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.callbackURL).Label("回调链接").Placeholder("http://127.0.0.1/cb?code=...&state=...").FillWidth()
		if ui.Button(c, "用回调链接完成登录").Clicked() {
			if err := a.auth.Complete(a.callbackURL); err != nil {
				a.loginMsg = err.Error()
			} else {
				a.loginMsg = "已收到回调，正在用授权码换取令牌…"
			}
		}
		ui.Divider(c)
		ui.Text(c, "还没有申请应用？也可以直接粘贴 access_token（匿名读端点本来就不需要登录）：").FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.manualToken).Label("access_token").Placeholder("粘贴令牌").FillWidth()
		if ui.Button(c, "用令牌登录").Clicked() {
			tok := a.manualToken
			a.manualToken = ""
			a.loginMsg = "正在校验令牌…"
			go a.auth.LoginWithToken(context.Background(), tok)
		}
		ui.Spacer(c).Grow(1)
		if v := a.auth.verifyStatus(); v != "" {
			ui.Text(c, v).FontSize(11).TextColor(t.TextMuted)
		}
	})
}

func maskConfig(s string) string {
	if s == "" {
		return "（未填写，请先到「设置」配置）"
	}
	if len(s) <= 8 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func (a *app) startLogin() {
	authorizeURL, err := a.auth.Begin(context.Background())
	if err != nil {
		a.loginMsg = err.Error()
		return
	}
	a.loginMsg = "已在浏览器打开授权页。同意后会自动回到应用；若浏览器跳到无端口的 127.0.0.1 地址，请复制地址栏链接粘贴到下方。"
	a.srv.OpenURL(authorizeURL) // URL 服务在 UI 线程调用
}

func main() {
	a := newApp("")
	a.autoLoad = true
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
