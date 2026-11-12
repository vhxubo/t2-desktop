package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 搜索窗口：和通知 / 设置 / 我的 / 金币一样是一个独立的 mygo 窗口。入口是主
// 窗口标题栏的放大镜（见 titleBar）。数据与请求在 search.go，这里只负责窗口
// 开关与界面。
//
// 为什么不放进侧边栏：搜索是「随时用一下就走」的动作，不是一页常驻内容；
// 独立窗口还能让主窗口的列表保持原来的滚动位置。

/* ------------------------------------------------------------------
   窗口
-------------------------------------------------------------- */

// showSearch 是搜索窗口的全局入口：把工作挪到主线程，已经开着就只带到前台。
func (a *app) showSearch() {
	a.publish(a.showSearchWindow)
}

func (a *app) showSearchWindow() {
	if a.win == nil {
		// headless 测试没有主循环：只把状态准备好，测试直接渲染 searchWindowView。
		if a.search == nil {
			a.search = newSearchState()
		}
		return
	}
	if a.search == nil {
		a.search = newSearchState()
	}
	if win := a.searchWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "搜索",
		Width:             searchWindowWidth,
		Height:            searchWindowHeight,
		MinWidth:          searchWinMinWidth,
		MinHeight:         searchWinMinHeight,
		StateKey:          "search",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.searchWindowView),
	})
	a.searchWin = win
	win.OnClosed(func() {
		a.stopSearchTimer()
		a.searchWin = nil
	})
	if a.win != nil {
		a.win.Invalidate() // 标题栏的放大镜高亮跟着窗口状态走
	}
}

// closeSearchWindow 关掉搜索窗口：退出登录、收进托盘与退出前调用。
func (a *app) closeSearchWindow() {
	a.stopSearchTimer()
	win := a.searchWin
	a.searchWin = nil
	if win != nil {
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// redrawSearchWindow 让搜索窗口按最新状态重建。只在主线程调用。
func (a *app) redrawSearchWindow() {
	if win := a.searchWin; win != nil {
		win.Invalidate()
	}
}

// stopSearchTimer 停掉还没到点的防抖定时器，并让在途请求的结果作废。
func (a *app) stopSearchTimer() {
	st := a.search
	if st == nil {
		return
	}
	st.gen++
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

// searchWindowVisible 报告搜索窗口此刻是否存在：标题栏的放大镜据此高亮。
func (a *app) searchWindowVisible() bool { return a.searchWin != nil }

/* ------------------------------------------------------------------
   界面
-------------------------------------------------------------- */

func (a *app) searchWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastSearch))
	// Esc 关闭窗口：随时可以从标题栏的放大镜再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeSearchWindow)

	st := a.search
	if st == nil {
		return
	}
	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与其它附属窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, searchIcon).Size(15, 15).TextColor(t.TextMuted)
				ui.Text(c, "搜索").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if st.loaded {
					ui.Textf(c, "共 %d 条", st.total).FontSize(11).
						TextColor(t.TextMuted).SingleLine()
				}
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.searchWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", a.closeSearchWindow)
		})
		ui.Divider(c)

		// 输入与类型：输入框占满整行，类型分段控件在下。
		ui.Column(c).FillWidth().Padding(12, 14).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, searchIcon).Size(16, 16).TextColor(t.TextMuted)
				ui.TextInput(c, &st.q).
					Placeholder("搜索帖子、用户、节点或标签（至少 2 个字）").
					FillWidth().Bind(&st.input)
			})
			ui.Segmented(c, &st.kind, searchKindLabels...)
		})
		ui.Divider(c)

		// 输入或类型变了：重排防抖。视图每帧都会走一遍，靠 lastQ / lastKind
		// 判断，只有真的变了才动请求。换类型要清掉上一次的结果，避免旧类型
		// 的行留在新类型下；改关键词则保留旧结果，等新结果回来再替换。
		if st.q != st.lastQ {
			st.lastQ = st.q
			a.scheduleSearch(st)
		}
		if st.kind != st.lastKind {
			st.lastKind = st.kind
			st.hits = nil
			st.loaded = false
			st.noMore = false
			st.page = 0
			st.totalPages = 0
			st.total = 0
			a.scheduleSearch(st)
		}

		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			a.searchBody(c, st)
		})
	})
}

// searchBody 是结果区：各种说明与结果列表。
func (a *app) searchBody(c *ui.Context, st *searchState) {
	t := c.Theme()
	query := strings.TrimSpace(st.q)
	switch {
	case query == "":
		a.searchPlaceholder(c, "输入关键词开始搜索",
			"帖子、用户、节点与标签都能搜。关键词至少 2 个字。")
		return
	case len([]rune(query)) < searchQueryMin:
		a.searchPlaceholder(c, "关键词太短", "至少输入 2 个字符再搜索。")
		return
	case searchKind(st.kind) == searchFavorites && !a.auth.loggedIn():
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "搜索自己的收藏需要登录").FontSize(15).Bold()
			ui.Text(c, "收藏是私人内容，只有登录后才能搜索。").TextColor(t.TextMuted)
			if ui.PrimaryButton(c, "去登录").Clicked() {
				a.requestLogin("登录后才能搜索自己的收藏。", func() {
					a.runSearch(st, st.gen, 1)
				})
			}
		})
		return
	}
	switch {
	case st.loading && len(st.hits) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(26, 26)
			ui.Text(c, searchSearching).TextColor(t.TextMuted)
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "搜索失败").FontSize(15).Bold()
			ui.Text(c, st.err).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.runSearch(st, st.gen, 1)
			}
		})
		return
	case st.loaded && len(st.hits) == 0:
		a.searchPlaceholder(c, "没有找到结果",
			"换个关键词，或把类型切到帖子 / 用户再试一次。")
		return
	case !st.loaded && !st.loading && len(st.hits) == 0:
		// 关键词够了但防抖还没到点：说明马上就搜，不留一屏空白。
		a.searchPlaceholder(c, "稍候即可搜索", "输入停顿一下就会自动搜索。")
		return
	}

	if st.note != "" {
		ui.Text(c, st.note).FontSize(11).TextColor(t.TextMuted).Padding(0, 14, 6, 14).MaxLines(2)
	}
	if st.firstLoading && len(st.hits) > 0 {
		ui.Row(c).Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(13, 13)
			ui.Text(c, searchSearching).FontSize(11).TextColor(t.TextMuted)
		})
	}
	hits := st.hits
	st.list.Key = func(i int) any {
		if i >= 0 && i < len(hits) {
			return searchHitKey(hits[i])
		}
		return i
	}
	ui.List(c, &st.list, len(hits), func(i int) {
		a.searchRow(c, st, hits[i])
	}).Grow(1).Padding(8, 14).Gap(10)
	// 续拉：只有帖子 / 收藏分页。滚到底自动拉下一页，失败停在提示上。
	// 已有结果才续拉：空列表的 AtEnd 也会是 true，不能让它去请求第 2 页。
	if a.autoLoad && len(hits) > 0 && searchCanLoadMore(st) && st.list.AtEnd() {
		a.runSearch(st, st.gen, st.page+1)
	}
	if st.loading && !st.firstLoading && len(st.hits) > 0 {
		ui.Row(c).Padding(6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(13, 13)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	}
}

// searchPlaceholder 是结果区的空状态：标题 + 一行说明，居中。
func (a *app) searchPlaceholder(c *ui.Context, title, hint string) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Padding(24).Children(func() {
		ui.Icon(c, searchIcon).Size(30, 30).TextColor(t.TextMuted)
		ui.Text(c, title).FontSize(14).Bold()
		ui.Text(c, hint).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
	})
}

// searchRow 画一条结果：帖子 / 收藏用主窗口同一套列表行；用户点开资料卡；
// 节点与标签在浏览器打开站点对应页面（应用内还没有节点页与标签页）。
func (a *app) searchRow(c *ui.Context, st *searchState, hit searchHit) {
	t := c.Theme()
	switch hit.kind {
	case searchPosts, searchFavorites:
		a.rowWith(c, hit.item, a.openSearchTopic)
		return
	}
	row := ui.ButtonBase(c).FillWidth().Padding(12, 14).Radius(10).
		Background(t.Background).Border(1, t.Border)
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			switch hit.kind {
			case searchUsers:
				a.userTag(c, userTagSpec{
					Name:   hit.username,
					Avatar: hit.avatar,
					Size:   14,
					Bold:   true,
				})
				ui.Text(c, "用户").FontSize(11).TextColor(t.TextMuted).SingleLine()
			case searchNodes:
				ui.Text(c, hit.name).FontSize(14).Bold().MaxLines(2)
				label := "节点"
				if hit.parent != "" {
					label = "节点 · " + hit.parent + " 的子节点"
				}
				ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).SingleLine()
			default: // searchTags
				ui.Text(c, "#"+hit.name).FontSize(14).Bold().MaxLines(2)
				ui.Text(c, "标签").FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		if hit.count > 0 && hit.kind != searchUsers {
			ui.Textf(c, "%d 个帖子", hit.count).FontSize(11).
				TextColor(t.TextMuted).SingleLine().TextAlign(ui.End)
		} else if hit.kind == searchUsers {
			ui.Text(c, "查看资料").FontSize(10).TextColor(t.TextMuted).SingleLine()
		} else if hit.url != "" {
			ui.Text(c, "在浏览器打开").FontSize(10).TextColor(t.TextMuted).SingleLine()
		}
	})
	if hit.kind == searchUsers {
		row.Label("用户 " + hit.username).Tooltip("打开 " + hit.username + " 的资料卡")
		row.OnClick(func() { a.showUserCard(hit.username) })
		return
	}
	if hit.url == "" {
		return
	}
	row.Label(hit.name).Tooltip("在浏览器打开：" + hit.url)
	row.OnClick(func() { a.openExternal(hit.url) })
}

// openSearchTopic 从搜索结果打开帖子：在**主窗口**里进详情，搜索窗口留着。
// 用户看完帖回来还能接着看结果，不用重新搜。
func (a *app) openSearchTopic(it Item) {
	if it.ShortID == "" {
		if it.URL != "" {
			a.openExternal(it.URL)
		}
		return
	}
	a.openDetail(it)
	if a.win != nil {
		a.win.Focus()
	}
}
