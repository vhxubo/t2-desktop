package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 搜索窗口：和通知 / 设置 / 我的 / 金币一样是一个独立的 mygo 窗口。入口是主
// 窗口标题栏搜索框的命令面板（见 titleBar / command_palette.go）。数据与请求在
// search.go，这里只负责窗口开关与界面。
//
// 为什么不放进侧边栏：搜索是「随时用一下就走」的动作，不是一页常驻内容；
// 独立窗口还能让主窗口的列表保持原来的滚动位置。
//
// 界面整体走 Lobe UI：搜索框、分段筛选、标签芯片、空状态、转圈与提示分别由
// field.Input、action.Segmented / Button、display.Empty、feedback.Spin / Alert
// 提供，颜色取 TokensOf(c)，和主窗口及各附属窗口同一套观感。列表仍用 mygo
// 的虚拟 ui.List：Lobe 没有带 AtEnd 无限滚动的列表组件，通知窗口也一样保留。
// 标题栏按钮沿用应用统一的 titleBarButton（图标 + tooltip 的关闭按钮约定）。

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
		Title:             a.subWindowTitle("搜索"),
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
		a.authGateWindowClosed(toastSearch)
		a.accountWindowClosed(win)
	})
}

// closeSearchWindow 关掉搜索窗口：退出登录、收进托盘与退出前调用。
func (a *app) closeSearchWindow() {
	a.stopSearchTimer()
	win := a.searchWin
	a.searchWin = nil
	if win != nil {
		win.Close()
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

/* ------------------------------------------------------------------
   界面
-------------------------------------------------------------- */

func (a *app) searchWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	defer a.drainToasts(c, a.claimsToasts(toastSearch))
	// Esc 关闭窗口：随时可以从标题栏搜索框的命令面板再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeSearchWindow)

	st := a.search
	if st == nil {
		return
	}
	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与其它附属窗口一致。
		// 图标换成 Lucide 放大镜，文字走 display.Text，配色走 Lobe token。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				display.Icon(c, lucide.Search(), display.IconProps{Size: display.IconSmall}).
					TextColor(tk.Primary)
				display.Text(c, "搜索", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				if st.loaded {
					display.Text(c, fmt.Sprintf("共 %d 条", st.total), display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.searchWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeSearchWindow)
		})
		layout.Divider(c, layout.DividerProps{})

		// 输入与类型：Lobe 的 Search 图标前缀输入框占满整行，类型用分段控件。
		ui.Column(c).FillWidth().Padding(12, 14).Gap(10).Children(func() {
			in := composeInput(c, &st.q, field.InputProps{
				Placeholder: "搜索帖子、用户、节点或标签（至少 2 个字）",
				Size:        lobetheme.Small,
				Prefix:      lucide.Search(),
			})
			in.Label("搜索关键词").Bind(&st.input)
			// action.Segmented 绑到本地值，写回 st.kind 后由下面的逐帧比较
			// 统一清掉旧类型的结果并重排防抖。
			selected := st.kind
			action.Segmented(c, &selected, searchKindLabels, action.SegmentedProps{
				Size: lobetheme.Small, Block: true,
			})
			if selected != st.kind {
				st.kind = selected
			}
		})
		layout.Divider(c, layout.DividerProps{})

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
		// 热门标签的周期换了：丢掉旧周期的标签，重新拉一份。标签是公开数据，
		// 与登录态无关；autoLoad 关掉时只清状态，测试自己驱动 loadHotTags。
		if st.hotTagsSpan != st.lastHotTagsSpan {
			st.lastHotTagsSpan = st.hotTagsSpan
			st.hotTags = nil
			st.hotTagsLoaded = false
			st.hotTagsErr = ""
			if a.autoLoad {
				a.loadHotTags(st, hotTagPeriod(st.hotTagsSpan))
			}
		}

		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			a.searchBody(c, st)
		})
	})
	a.ensureSearchHotTags(st)
	// 缺权限时就在这个窗口里弹授权：搜索的请求是从这里发起的。
	a.authDialogFor(c, toastSearch)
}

// searchBody 是结果区：各种说明与结果列表。整页级的空 / 错误状态保留内联，
// 因为它们要说明页面为什么空着，并给出「重试」入口（不是一次操作的回执）。
func (a *app) searchBody(c *ui.Context, st *searchState) {
	query := strings.TrimSpace(st.q)
	switch {
	case query == "":
		a.searchHome(c, st)
		return
	case len([]rune(query)) < searchQueryMin:
		a.searchPlaceholder(c, "关键词太短", "至少输入 2 个字符再搜索。")
		return
	case searchKind(st.kind) == searchFavorites && !a.auth.loggedIn():
		ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Bookmark(),
				Title:       "搜索自己的收藏需要登录",
				Description: "收藏是私人内容，只有登录后才能搜索。",
				Action: func() {
					if action.Button(c, "去登录", action.ButtonProps{
						Size: lobetheme.Small, Variant: action.Primary,
					}).Clicked() {
						a.requestLoginFor(toastSearch, "登录后才能搜索自己的收藏。", func() {
							a.runSearch(st, st.gen, 1)
						})
					}
				},
			})
		})
		return
	}
	switch {
	case st.loading && len(st.hits) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 26})
			display.Text(c, searchSearching, display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "搜索失败", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: st.err,
			})
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
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
		display.Text(c, st.note, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 2,
		}).Padding(0, 14, 6, 14)
	}
	if st.firstLoading && len(st.hits) > 0 {
		ui.Row(c).Padding(0, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 13})
			display.Text(c, searchSearching, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
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
			lobeSpin(c, feedback.SpinProps{CustomSize: 13})
			display.Text(c, "正在加载更多…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		})
	}
}

// searchPlaceholder 是结果区的空状态：Lobe 的 Empty（图标 + 标题 + 一行说明），
// 在剩余空间里居中。
func (a *app) searchPlaceholder(c *ui.Context, title, hint string) {
	ui.Column(c).Grow(1).MinHeight(0).Center().Children(func() {
		display.Empty(c, display.EmptyProps{
			Icon: lucide.Search(), Title: title, Description: hint,
		})
	})
}

// searchHome 是空关键词时的首页：搜索说明 + 热门标签。标签来自
// GET /v1/tags/hot，点一下就把名字填进搜索框并切到「标签」类型。
// 滚动用 Lobe 的 ScrollArea，细滚动条与边缘渐隐和窗口其它页面一致。
func (a *app) searchHome(c *ui.Context, st *searchState) {
	tk := lobetheme.TokensOf(c)
	sc := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Padding(28, 24).Gap(18).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				display.Icon(c, lucide.Search(), display.IconProps{Size: display.IconLarge}).
					FontSize(30).TextColor(tk.TextQuaternary)
				display.Text(c, "输入关键词开始搜索", display.TextProps{
					Strong: true, FontSize: 14, Rows: 1,
				})
				display.Text(c, "帖子、用户、节点与标签都能搜。关键词至少 2 个字。",
					display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2})
			})
			a.searchHotTagsBlock(c, st)
		})
	})
	sc.Grow(1).MinHeight(0)
}

// searchHotTagsBlock 是空状态里的热门标签块：周期切换、加载 / 失败 / 空
// 状态与一排标签芯片。最多画 24 枚，避免一次把整页塞满。
func (a *app) searchHotTagsBlock(c *ui.Context, st *searchState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "热门标签", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
			ui.Spacer(c).Grow(1)
			span := st.hotTagsSpan
			action.Segmented(c, &span, hotTagPeriodLabels, action.SegmentedProps{
				Size: lobetheme.Small,
			})
			if span != st.hotTagsSpan {
				st.hotTagsSpan = span
			}
		})

		switch {
		case st.hotTagsErr != "":
			feedback.Alert(c, "热门标签暂时不可用", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: st.hotTagsErr,
			})
			a.smallAction(c, "重试", st.hotTagsLoading, func() {
				a.loadHotTags(st, hotTagPeriod(st.hotTagsSpan))
			})
		case st.hotTagsLoading && len(st.hotTags) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
				display.Text(c, "正在读取热门标签…", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			})
		case len(st.hotTags) == 0 && st.hotTagsLoaded:
			display.Text(c, "这个周期还没有热门标签。", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		case len(st.hotTags) == 0:
			display.Text(c, "正在读取热门标签…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		default:
			tags := st.hotTags
			if len(tags) > 24 {
				tags = tags[:24]
			}
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				for _, tag := range tags {
					label := "#" + tag.Name
					tip := "搜索标签「" + tag.Name + "」"
					if tag.Count > 0 {
						label += " " + strconv.Itoa(tag.Count)
						tip += fmt.Sprintf("（%d 个帖子）", tag.Count)
					}
					// Lobe 的胶囊按钮当芯片：填充底 + 全圆角，可读名与
					// tooltip 照旧，测试仍按「标签 <名字>」点它。
					chip := action.Button(c, label, action.ButtonProps{
						Size: lobetheme.Small, Variant: action.Filled,
					})
					chip.Radius(999).Label("标签 " + tag.Name).Tooltip(tip)
					if chip.Clicked() {
						a.searchHotTag(st, tag.Name)
					}
				}
			})
		}
	})
}

// searchRow 画一条结果：帖子 / 收藏用主窗口同一套列表行；用户点开资料卡；
// 节点与标签在浏览器打开站点对应页面（应用内还没有节点页与标签页）。
// 非帖子的行是一张可点的 Lobe Block：描边卡片与悬停底色都来自主题。
func (a *app) searchRow(c *ui.Context, st *searchState, hit searchHit) {
	switch hit.kind {
	case searchPosts, searchFavorites:
		a.rowWith(c, hit.item, a.openSearchTopic)
		return
	}
	row := layout.Block(c, layout.BlockProps{Clickable: true, Variant: layout.BlockOutlined}).
		FillWidth().Row().AlignItems(ui.Center).Padding(12, 14)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			switch hit.kind {
			case searchUsers:
				a.userTag(c, userTagSpec{
					Name:   hit.username,
					Avatar: hit.avatar,
					Size:   14,
					Bold:   true,
					Badges: hit.badges,
				})
				display.Text(c, "用户", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			case searchNodes:
				display.Text(c, hit.name, display.TextProps{
					Strong: true, FontSize: 14, Rows: 2,
				})
				label := "节点"
				if hit.parent != "" {
					label = "节点 · " + hit.parent + " 的子节点"
				}
				display.Text(c, label, display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			default: // searchTags
				display.Text(c, "#"+hit.name, display.TextProps{
					Strong: true, FontSize: 14, Rows: 2,
				})
				display.Text(c, "标签", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
			}
		})
		switch {
		case hit.count > 0 && hit.kind != searchUsers:
			display.Text(c, fmt.Sprintf("%d 个帖子", hit.count), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			}).TextAlign(ui.End)
		case hit.kind == searchUsers:
			display.Text(c, "查看资料", display.TextProps{
				Type: display.TextSecondary, FontSize: 10, Rows: 1,
			})
		case hit.url != "":
			display.Text(c, "在浏览器打开", display.TextProps{
				Type: display.TextSecondary, FontSize: 10, Rows: 1,
			})
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
