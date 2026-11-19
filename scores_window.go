package main

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 评分榜窗口：主页 FAB 的「评分榜」入口。数据与解码在 scores.go。
//
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

	category     string
	lastCategory string
	sort         int
	lastSort     int

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
		Content:           ui.View(a.scoresWindowView),
	})
	a.scoresWin = win
	win.OnClosed(func() { a.scoresWin = nil })
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
   界面
--------------------------------------------------------------- */

func (a *app) scoresWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	c.OnShortcut(0, ui.KeyEscape, a.closeScoresWindow)

	st := a.scores
	if st == nil {
		return
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, scoresIcon).Size(15, 15).TextColor(t.Accent)
				ui.Text(c, "评分榜").FontSize(13).Bold().SingleLine()
				if st.loaded {
					ui.Textf(c, "共 %d 条", st.total).FontSize(11).TextColor(t.TextMuted).SingleLine()
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
		ui.Divider(c)

		a.scoresFilters(c, st)
		ui.Divider(c)

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

func (a *app) scoresFilters(c *ui.Context, st *scoresState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(10, 14).Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c, &st.sort, scoreSortLabels...)
			ui.Select(c, &st.category, st.categoryOptions()).
				Label("评分分类").Width(170)
			ui.Spacer(c).Grow(1)
			a.smallAction(c, "刷新", st.loading, a.reloadScores)
		})
		if st.categoriesErr != "" {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "分类暂时不可用："+st.categoriesErr).FontSize(11).
					TextColor(t.Danger).MaxLines(2).Grow(1)
				a.smallAction(c, "重试", st.categoriesLoading, a.loadScoreCategories)
			})
		}
	})
}

func (a *app) scoresBody(c *ui.Context, st *scoresState) {
	t := c.Theme()
	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(26, 26)
			ui.Text(c, "正在读取评分…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case st.err != "" && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
			ui.Text(c, "评分榜加载失败").FontSize(14).Bold()
			ui.Text(c, st.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadScores(1)
			}
		})
		return
	case st.loaded && len(st.items) == 0:
		ui.Column(c).Grow(1).Center().Gap(6).Children(func() {
			ui.Text(c, "这个筛选下还没有评分。").FontSize(13).TextColor(t.TextMuted)
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
	}).Grow(1).Gap(8)
	if a.autoLoad && len(st.items) > 0 && !st.noMore && st.page < st.totalPages && st.list.AtEnd() {
		a.loadScores(st.page + 1)
	}
	switch {
	case st.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+st.moreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadScores(st.page + 1) })
		})
	case st.loading:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(13, 13)
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	}
}

func (a *app) scoreRow(c *ui.Context, e scoreEntry) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(10, 12).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			a.scoreBadge(c, e)
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, e.Target).FontSize(14).Bold().MaxLines(2).Grow(1).MinWidth(0)
					if e.Category != "" {
						userCardChip(c, e.Category, t.TextMuted)
					}
				})
				if comment := strings.TrimSpace(e.Comment); comment != "" {
					ui.Text(c, comment).FontSize(12).TextColor(t.Text).MaxLines(2)
				}
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					if e.Author != "" {
						a.userTag(c, userTagSpec{
							Name: e.Author, Avatar: e.Avatar, Badges: e.Badges, Size: 11,
						})
					}
					if e.IsPro {
						userCardChip(c, "PRO", t.Accent)
					}
					if when := relativeTime(e.CreatedAt); when != "" {
						ui.Text(c, when).FontSize(10).TextColor(t.TextMuted).SingleLine()
					}
					ui.Spacer(c).Grow(1)
				})
			})
		})
	})
	row.Label("评分 " + e.Target).Tooltip("打开评分对应的帖子")
	if e.ShortID != "" {
		row.OnClick(func() { a.openScorePost(e) })
	}
}

// scoreBadge 是左侧的分值块：高分绿、低分红、中间黄；缺失时不编 0 分。
func (a *app) scoreBadge(c *ui.Context, e scoreEntry) {
	t := c.Theme()
	ink := t.TextMuted
	if e.ScoreKnown {
		switch {
		case e.Score >= 8:
			ink = t.Success
		case e.Score <= 4:
			ink = t.Danger
		default:
			ink = t.Warning
		}
	}
	ui.Box(c).Width(46).Shrink(0).Padding(6, 4).Radius(8).
		Background(t.Surface).Border(1, t.Border).Center().Children(func() {
		ui.Column(c).Gap(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, scoreText(e)).FontSize(16).Bold().TextColor(ink).SingleLine()
			if e.ScoreKnown {
				ui.Text(c, "分").FontSize(9).TextColor(t.TextMuted).SingleLine()
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
