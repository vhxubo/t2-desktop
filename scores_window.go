package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 评分榜窗口：侧边栏「动态」下方（分割线之后）的「评分榜」入口（见
// sidebarNavItems）打开的一扇独立窗口，数据与解码在 scores.go。
//
// 界面走 Lobe UI：颜色取 TokensOf(c)，标题、筛选、卡片、空状态与按钮分别由
// display / field / action / feedback / layout 提供，和其它窗口保持同一套观感。
// 版式：标题栏（拖动 / 最小化 / 关闭）→ 排序与分类筛选 → 评分列表。
// 每行是「对象名 + 分数 + 分类 + 短评 + 评分人」，点一下打开对应帖子。

const (
	scoresWindowWidth  = 680
	scoresWindowHeight = 720
	scoresWinMinWidth  = 460
	scoresWinMinHeight = 420
)

// scoresState 是评分榜窗口的全部界面状态。只在 UI 线程读写；请求结果经
// publish 回来，用 seq 丢弃过期页。
type scoresState struct {
	categories        []string
	categoriesLoading bool
	categoriesLoaded  bool
	categoriesErr     string
	categoriesSeq     uint64

	category       string
	lastCategory   string
	categoryActive int
	sort           int
	lastSort       int

	items      []scoreEntry
	total      int
	page       int
	totalPages int
	noMore     bool
	loading    bool
	loaded     bool
	err        string
	moreErr    string
	seq        uint64
	list       ui.ListState
}

func newScoresState() *scoresState {
	return &scoresState{
		category:     scoreCategoryAll,
		lastCategory: scoreCategoryAll,
	}
}

func scoreSortValue(i int) string {
	if i < 0 || i >= len(scoreSortValues) {
		return scoreSortValues[0]
	}
	return scoreSortValues[i]
}

// categoryOptions 是分类下拉框的选项：第一项是「全部分类」，其余来自字典。
func (st *scoresState) categoryOptions() []string {
	out := make([]string, 0, len(st.categories)+1)
	out = append(out, scoreCategoryAll)
	out = append(out, st.categories...)
	return out
}

/* ------------------------------------------------------------------
   窗口
--------------------------------------------------------------- */

func (a *app) showScores() {
	a.publish(a.showScoresWindow)
}

func (a *app) showScoresWindow() {
	if a.scores == nil {
		a.scores = newScoresState()
	}
	a.ensureScoresLoaded()
	// headless 测试没有主循环：只准备状态，测试直接渲染 scoresWindowView。
	if a.win == nil {
		return
	}
	if win := a.scoresWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("评分榜"),
		Width:             scoresWindowWidth,
		Height:            scoresWindowHeight,
		MinWidth:          scoresWinMinWidth,
		MinHeight:         scoresWinMinHeight,
		StateKey:          "scores",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.scoresWindowView)),
	})
	a.scoresWin = win
	win.OnClosed(func() {
		a.scoresWin = nil
		a.accountWindowClosed(win)
	})
	a.win.Invalidate()
}

func (a *app) closeScoresWindow() {
	win := a.scoresWin
	a.scoresWin = nil
	if win != nil {
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *app) redrawScoresWindow() {
	if win := a.scoresWin; win != nil {
		win.Invalidate()
	}
}

func (a *app) scoresWindowVisible() bool { return a.scoresWin != nil }

/* ------------------------------------------------------------------
   加载
--------------------------------------------------------------- */

func (a *app) ensureScoresLoaded() {
	st := a.scores
	if st == nil || !a.autoLoad {
		return
	}
	if !st.categoriesLoaded && !st.categoriesLoading && st.categoriesErr == "" {
		a.loadScoreCategories()
	}
	if !st.loaded && !st.loading && st.err == "" {
		a.loadScores(1)
	}
}

func (a *app) loadScoreCategories() {
	st := a.scores
	if st == nil || st.categoriesLoading {
		return
	}
	st.categoriesSeq++
	seq := st.categoriesSeq
	st.categoriesLoading = true
	st.categoriesErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		cats, err := a.client.fetchScoreCategories(ctx)
		a.publish(func() {
			if a.scores != st || st.categoriesSeq != seq {
				return
			}
			st.categoriesLoading = false
			st.categoriesLoaded = true
			if err != nil {
				st.categoriesErr = friendlyError(err)
				return
			}
			st.categories = cats
			st.categoriesErr = ""
			// 当前选中的分类已经不在字典里：退回「全部分类」并重拉。
			if st.category != scoreCategoryAll && !containsString(cats, st.category) {
				st.category = scoreCategoryAll
				st.lastCategory = scoreCategoryAll
				a.reloadScores()
			}
			a.redrawScoresWindow()
		})
	}()
}

// loadScores 拉一页评分。page==1 时替换，page>1 时按评分 id 去重后追加。
func (a *app) loadScores(page int) {
	st := a.scores
	if st == nil || st.loading || page < 1 {
		return
	}
	if page == 1 {
		st.seq++
		st.items = nil
		st.loaded = false
		st.noMore = false
		st.page = 0
		st.totalPages = 0
		st.err = ""
		st.moreErr = ""
		st.list.ScrollTo(0, ui.Start)
	}
	seq := st.seq
	st.loading = true
	if page == 1 {
		st.err = ""
	} else {
		st.moreErr = ""
	}
	sort := scoreSortValue(st.sort)
	category := st.category
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchScores(ctx, sort, category, page)
		a.publish(func() {
			if a.scores != st || st.seq != seq {
				return
			}
			st.loading = false
			st.loaded = true
			if err != nil {
				if page == 1 {
					st.err = friendlyError(err)
				} else {
					st.moreErr = friendlyError(err)
				}
				return
			}
			if page == 1 {
				st.items = res.Items
			} else {
				seen := make(map[string]bool, len(st.items))
				for _, it := range st.items {
					seen[it.ID] = true
				}
				added := 0
				for _, it := range res.Items {
					if !seen[it.ID] {
						st.items = append(st.items, it)
						seen[it.ID] = true
						added++
					}
				}
				if added == 0 {
					st.noMore = true
				}
			}
			st.page = res.Page
			st.total = res.Total
			if res.TotalPages > 0 {
				st.totalPages = res.TotalPages
			}
			if len(res.Items) == 0 || (st.totalPages > 0 && st.page >= st.totalPages) {
				st.noMore = true
			}
			st.err = ""
			st.moreErr = ""
			for _, it := range res.Items {
				a.prefetchAvatars(it.Avatar)
			}
		})
	}()
}

func (a *app) reloadScores() {
	st := a.scores
	if st == nil {
		return
	}
	// 取消在途请求：换排序 / 分类时旧结果不能写进新筛选。
	st.seq++
	st.loading = false
	st.loaded = false
	st.items = nil
	st.err = ""
	st.moreErr = ""
	a.loadScores(1)
}

/* ------------------------------------------------------------------
   界面（Lobe UI）
--------------------------------------------------------------- */

func (a *app) scoresWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	c.OnShortcut(0, ui.KeyEscape, a.closeScoresWindow)

	st := a.scores
	if st == nil {
		return
	}
	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、最小化与关闭，画法与其它窗口一致。图标仍用
		// 评分榜的柱状图，文字走 display.Text，配色走 Lobe token。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				display.Icon(c, scoresIcon, display.IconProps{Size: display.IconSmall}).
					TextColor(tk.Primary)
				display.Text(c, "评分榜", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				if st.loaded {
					display.Text(c, fmt.Sprintf("共 %d 条", st.total), display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.scoresWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeScoresWindow)
		})
		layout.Divider(c, layout.DividerProps{})

		a.scoresFilters(c, st)
		layout.Divider(c, layout.DividerProps{})

		// 排序 / 分类变了：丢掉旧页，重新拉第 1 页。
		if st.sort != st.lastSort {
			st.lastSort = st.sort
			a.reloadScores()
		}
		if st.category != st.lastCategory {
			st.lastCategory = st.category
			a.reloadScores()
		}

		ui.Column(c).Grow(1).MinHeight(0).Padding(8, 14).Children(func() {
			a.scoresBody(c, st)
		})
	})
	a.ensureScoresLoaded()
}

// scoreCategoryLabelMax 是下拉选项展示文案的长度上限。Lobe 的 Select 按最长
// 选项决定弹层宽度且没有 MaxWidth，分类名太长会把弹层撑出窗口；Value 仍保留
// 完整分类名，只是显示文案截断。
const scoreCategoryLabelMax = 10

// scoreCategoryOptions 构造分类下拉的选项：显示文案限长，筛选值保持原样。
func scoreCategoryOptions(st *scoresState) []field.SelectOption {
	options := make([]field.SelectOption, 0, len(st.categories)+1)
	for _, cat := range st.categoryOptions() {
		options = append(options, field.SelectOption{
			Value: cat, Label: truncate(cat, scoreCategoryLabelMax),
		})
	}
	return options
}

// scoreCategorySelect 画评分分类下拉：触发器和弹层走 Lobe 的 overlay.Popover，
// 选项放进限高的滚动层（field.Select 的弹层没有滚动层，分类一多就溢出）。
// 触发器保留焦点，上下键移动高亮，回车 / 空格由按钮的 Clicked 关掉气泡并选中。
func scoreCategorySelect(c *ui.Context, selected *string, active *int, options []field.SelectOption) ui.Element {
	tk := lobetheme.TokensOf(c)
	c.Key("scores-category-select")
	tr := ui.ButtonBase(c).MinHeight(24).Padding(0, 8).Gap(8).
		Radius(tk.BorderRadiusSM).FontSize(12).AlignItems(ui.Center).TextColor(tk.Text).
		Cursor(ui.CursorPointer).Unselectable()
	open := ui.Local(tr, "scoreCategoryOpen", func() bool { return false })
	border := tk.BorderSecondary
	if tr.Hovered() && !tr.Focused() && !*open {
		border = tk.Border
	}
	if *open {
		border = tk.Primary
		tr.Shadow(0, 0, 0, 2, tk.PrimaryBg)
	}
	tr.Background(tk.BgContainer).Border(1, border)

	label := ""
	choice := 0
	for i, o := range options {
		if o.Value == *selected {
			label, choice = o.Label, i
			break
		}
	}
	if !*open {
		*active = choice
	}
	tr.Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).SingleLine()
		})
		turn := float32(0)
		if *open {
			turn = 180
		}
		ui.Icon(c, lucide.ChevronDown()).FontSize(16).TextColor(tk.TextSecondary).
			Shrink(0).Rotate(turn)
	})

	move := func(d int) {
		if len(options) == 0 {
			return
		}
		if !*open {
			*open, *active = true, choice
			return
		}
		for k := 0; k < len(options); k++ {
			*active = (*active + d + len(options)) % len(options)
			if !options[*active].Disabled {
				break
			}
		}
	}
	tr.OnShortcut(0, ui.KeyDown, func() { move(1) })
	tr.OnShortcut(0, ui.KeyUp, func() { move(-1) })
	// Lobe 的气泡读 anchor.Clicked 来开合；按钮在回车 / 空格上也报 Clicked，
	// 所以气泡关掉的这一帧正好把高亮项选上。
	tr.OnClick(func() {
		if *open || len(options) == 0 || options[*active].Disabled {
			return
		}
		*selected = options[*active].Value
	})

	lobeoverlay.Popover(c, tr, open, lobeoverlay.PopoverProps{Placement: lobeoverlay.Bottom}, func() {
		ui.Column(c).Width(max(200, tr.Bounds().W-24)).Children(func() {
			ui.Scroll(c).MaxHeight(256).Children(func() {
				for i, o := range options {
					item := ui.Row(c.Key(o.Value)).Disabled(o.Disabled).Padding(6, 12).MinHeight(32).
						Gap(8).AlignItems(ui.Center).Radius(tk.BorderRadiusSM).
						AlignSelf(ui.Stretch).FontSize(14).TextColor(tk.Text)
					if i == *active || item.Hovered() {
						item.Background(tk.FillTertiary).Cursor(ui.CursorPointer)
					} else if !o.Disabled {
						item.Cursor(ui.CursorPointer)
					}
					item.Children(func() {
						ui.Text(c, o.Label).Grow(1).SingleLine()
						if o.Value == *selected {
							ui.Icon(c, lucide.Check()).FontSize(14).TextColor(tk.Primary).
								Margin(0, 0, 0, 8)
						}
					})
					if item.Clicked() && !o.Disabled {
						*selected = o.Value
						*open = false
					}
				}
			})
		})
	})
	return tr
}

// scoresFilters 用 Lobe 的 Segmented / Select / Button 画筛选条：排序直接绑定
// st.sort，分类下拉绑定 st.category，改动由 view 里的 lastSort / lastCategory
// 比对接住并重拉第一页。分类字典单独读，失败不影响评分列表。
func (a *app) scoresFilters(c *ui.Context, st *scoresState) {
	card := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
		FillWidth().Padding(10, 14)
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			// 窗口缩到最小宽度时，排序、标签、下拉与刷新挤在一行会把「评分分类」
			// 截成「评…」。标签与下拉都不参与压缩，下拉宽度也按最小窗口留够余量，
			// 让四个控件在 460 DIP 下仍能完整排在同一行。
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				action.Segmented(c, &st.sort, scoreSortLabels, action.SegmentedProps{
					Size: lobetheme.Small,
				})
				display.Text(c, "评分分类", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				}).Shrink(0)
				scoreCategorySelect(c, &st.category, &st.categoryActive, scoreCategoryOptions(st)).
					Label("评分分类").Tooltip("按分类筛选评分").Width(150).Shrink(0)
				ui.Spacer(c).Grow(1)
				a.smallRefreshButton(c, st.loading, a.reloadScores)
			})
			if st.categoriesErr != "" {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					display.Text(c, "分类暂时不可用："+st.categoriesErr, display.TextProps{
						Type: display.TextDanger, FontSize: 11, Rows: 2,
					}).Grow(1).MinWidth(0)
					a.smallAction(c, "重试", st.categoriesLoading, a.loadScoreCategories)
				})
			}
		})
	})
}

func (a *app) scoresBody(c *ui.Context, st *scoresState) {
	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 26})
			display.Text(c, "正在读取评分…", display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
		return
	case st.err != "" && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "评分榜加载失败", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: st.err, Selectable: true,
			})
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.loadScores(1)
			}
		})
		return
	case st.loaded && len(st.items) == 0:
		display.Empty(c, display.EmptyProps{
			Icon:  lucide.ChartNoAxesColumn(),
			Title: "这个筛选下还没有评分。",
		})
		return
	}

	st.list.Key = func(i int) any {
		if i >= 0 && i < len(st.items) {
			return st.items[i].ID
		}
		return i
	}
	ui.List(c, &st.list, len(st.items), func(i int) {
		a.scoreRow(c, st.items[i])
	}).Grow(1).MinHeight(0).Gap(8)
	if a.autoLoad && len(st.items) > 0 && !st.noMore && st.page < st.totalPages && st.list.AtEnd() {
		a.loadScores(st.page + 1)
	}
	switch {
	case st.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "加载更多失败："+st.moreErr, display.TextProps{
				Type: display.TextDanger, FontSize: 11, Rows: 2,
			}).Grow(1).MinWidth(0)
			a.smallAction(c, "重试", false, func() { a.loadScores(st.page + 1) })
		})
	case st.loading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 13})
			display.Text(c, "正在加载更多…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		})
	}
}

// scoreRow 是可点的 Lobe 描边卡片：左侧分值块，右侧对象名、分类、短评与评分人。
// 无障碍名沿用「评分 <对象名>」，点击仍走 openScorePost 打开对应帖子。
func (a *app) scoreRow(c *ui.Context, e scoreEntry) {
	row := layout.Block(c, layout.BlockProps{
		Variant: layout.BlockOutlined, Clickable: true,
	}).FillWidth().Padding(10, 12)
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			a.scoreBadge(c, e)
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					display.Text(c, e.Target, display.TextProps{
						Strong: true, FontSize: 14, Rows: 2,
					}).Grow(1).MinWidth(0)
					if e.Category != "" {
						display.Tag(c, e.Category, display.TagProps{Size: lobetheme.Small})
					}
				})
				if comment := strings.TrimSpace(e.Comment); comment != "" {
					display.Text(c, comment, display.TextProps{FontSize: 12, Rows: 2})
				}
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					if e.Author != "" {
						a.userTag(c, userTagSpec{
							Name: e.Author, Avatar: e.Avatar, Badges: e.Badges, Size: 11,
							Pro: e.IsPro,
						})
					}
					if when := relativeTime(e.CreatedAt); when != "" {
						display.Text(c, when, display.TextProps{
							Type: display.TextSecondary, FontSize: 10, Rows: 1,
						})
					}
					ui.Spacer(c).Grow(1)
				})
			})
		})
	})
	row.Label("评分 " + e.Target).Tooltip("打开评分对应的帖子")
	if e.ShortID != "" && row.Clicked() {
		a.openScorePost(e)
	}
}

// scoreBadge 是左侧的分值块：高分绿、低分红、中间黄；缺失时不编 0 分。
func (a *app) scoreBadge(c *ui.Context, e scoreEntry) {
	tk := lobetheme.TokensOf(c)
	ink := tk.TextDescription
	if e.ScoreKnown {
		switch {
		case e.Score >= 8:
			ink = tk.Success
		case e.Score <= 4:
			ink = tk.Error
		default:
			ink = tk.Warning
		}
	}
	ui.Box(c).Width(46).Shrink(0).Padding(6, 4).Radius(tk.BorderRadius).
		Background(tk.FillTertiary).Border(1, tk.BorderSecondary).Center().Children(func() {
		ui.Column(c).Gap(0).AlignItems(ui.Center).Children(func() {
			display.Text(c, scoreText(e), display.TextProps{
				Strong: true, FontSize: 16, Rows: 1,
			}).TextColor(ink)
			if e.ScoreKnown {
				display.Text(c, "分", display.TextProps{
					Type: display.TextSecondary, FontSize: 9, Rows: 1,
				})
			}
		})
	})
}

func (a *app) openScorePost(e scoreEntry) {
	if e.ShortID == "" {
		return
	}
	a.openDetail(Item{ShortID: e.ShortID, URL: topicURL(e.ShortID, e.NodeSlug)})
	a.showMainWindowAfterDetail()
}
