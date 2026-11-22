package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const scoreCategoriesJSON = `{"categories":["书籍","人物","动漫","游戏"]}`

const scoresJSON = `{
  "items":[
    {"post":{"short_id":"6EVPoNO","node":{"slug":"animation-sharing"}},
     "author":{"id":"u1","username":"Friday","avatar_url":"https://r2.2libra.com/a.png","is_pro":false},
     "score":{"id":"s1","target_name":"罗小黑战记","score":10,"comment":"国产动画的标杆",
              "category":"动漫","created_at":"2026-11-11T03:51:14.189Z"}},
    {"post":{"short_id":"cqrloBn","node":{"slug":"tool-sharing"}},
     "author":{"id":"u2","username":"Jimmy","avatar_url":"https://r2.2libra.com/u2.png","is_pro":true},
     "score":{"id":"s2","target_name":"ThinkRail","score":3,"comment":null,
              "category":"应用软件","created_at":"2026-10-29T06:18:53.908Z"}}
  ],
  "total":63,"page":1,"limit":30,"total_pages":3
}`

func TestDecodeScoreCategories(t *testing.T) {
	got := decodeScoreCategories([]byte(scoreCategoriesJSON))
	if len(got) != 4 || got[2] != "动漫" {
		t.Fatalf("分类 = %v", got)
	}
	if decodeScoreCategories([]byte(`{}`)) != nil {
		t.Error("没有 categories 时不该造出空分类")
	}
}

func TestDecodeScores(t *testing.T) {
	p := decodeScores([]byte(scoresJSON))
	if len(p.Items) != 2 || p.Total != 63 || p.TotalPages != 3 {
		t.Fatalf("评分页 = %+v", p)
	}
	e := p.Items[0]
	if e.Target != "罗小黑战记" || !e.ScoreKnown || e.Score != 10 ||
		e.Category != "动漫" || e.ShortID != "6EVPoNO" ||
		e.NodeSlug != "animation-sharing" || e.Author != "Friday" {
		t.Errorf("第一条评分解析不对：%+v", e)
	}
	if e.Comment != "国产动画的标杆" {
		t.Errorf("短评 = %q", e.Comment)
	}
	if p.Items[1].Comment != "" || !p.Items[1].IsPro {
		t.Errorf("第二条解析不对：%+v", p.Items[1])
	}
	// 分值缺失时用破折号，不编 0 分。
	missing := decodeScores([]byte(`{"items":[{"post":{"short_id":"X"},
		"score":{"target_name":"没有分数"}}]}`))
	if len(missing.Items) != 1 || missing.Items[0].ScoreKnown {
		t.Fatalf("缺分数解析不对：%+v", missing.Items)
	}
	if got := scoreText(missing.Items[0]); got != "—" {
		t.Errorf("缺分数文本 = %q", got)
	}
	if got := scoreText(scoreEntry{Score: 8.5, ScoreKnown: true}); got != "8.5" {
		t.Errorf("小数分值 = %q", got)
	}
	if got := scoreText(scoreEntry{Score: 10, ScoreKnown: true}); got != "10" {
		t.Errorf("整数分值 = %q", got)
	}
}

// 参数名照抄文档：sort / category / page / limit，分类为「全部分类」时不传。
func TestScoreQuery(t *testing.T) {
	q := scoreQuery("high", "动漫", 2)
	for _, want := range []string{"sort=high", "category=%E5%8A%A8%E6%BC%AB", "page=2", "limit=30"} {
		if !strings.Contains(q, want) {
			t.Errorf("评分参数 %q 缺少 %q", q, want)
		}
	}
	if got := scoreQuery("all", scoreCategoryAll, 1); strings.Contains(got, "category=") {
		t.Errorf("全部分类不该发 category：%q", got)
	}
}

func TestFetchScores(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		switch r.URL.Path {
		case "/api/v1/scores/categories":
			fmt.Fprint(w, envelopeJSON(scoreCategoriesJSON))
		default:
			fmt.Fprint(w, envelopeJSON(scoresJSON))
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api"}
	cats, err := c.fetchScoreCategories(context.Background())
	if err != nil || len(cats) != 4 {
		t.Fatalf("分类请求失败：%v %v", cats, err)
	}
	p, err := c.fetchScores(context.Background(), "high", "动漫", 1)
	if err != nil {
		t.Fatalf("评分请求失败：%v", err)
	}
	if len(p.Items) != 2 || gotPath != "/api/v1/scores" {
		t.Errorf("评分结果 = %+v path=%s", p, gotPath)
	}
	if !strings.Contains(gotQuery, "sort=high") || !strings.Contains(gotQuery, "category=%E5%8A%A8%E6%BC%AB") {
		t.Errorf("评分查询 = %q", gotQuery)
	}
}

// 窗口：筛选、数量、评分行与作者都画出来；点一行打开对应帖子。
func TestScoresWindowRenders(t *testing.T) {
	a := testApp(t)
	a.scores = newScoresState()
	st := a.scores
	st.categories = []string{"书籍", "动漫", "游戏"}
	st.categoriesLoaded = true
	st.loaded = true
	st.total = 63
	st.page = 1
	st.totalPages = 3
	st.items = decodeScores([]byte(scoresJSON)).Items

	tt := ui.NewTester(a.scoresWindowView, scoresWindowWidth, scoresWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"评分榜", "共 63 条", "全部", "高分", "低分", "评分分类",
		"罗小黑战记", "国产动画的标杆", "Friday", "动漫", "10",
		"ThinkRail", "Jimmy",
	} {
		if !tt.HasText(want) {
			t.Errorf("评分榜缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("评分 罗小黑战记"); err != nil {
		t.Fatalf("点评分行失败：%v", err)
	}
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "6EVPoNO" {
		t.Errorf("没有打开评分对应的帖子：page=%q detail=%+v", a.pageName, a.detail)
	}
}

// 分类名很长时，下拉弹层不能按最长项撑出窗口：显示文案限长，筛选值保留原名。
func TestScoreCategoryOptionsBounded(t *testing.T) {
	long := strings.Repeat("很长的分类名", 8)
	st := newScoresState()
	st.categories = []string{"书籍", long}
	opts := scoreCategoryOptions(st)
	if len(opts) != 3 || opts[0].Value != scoreCategoryAll {
		t.Fatalf("分类选项 = %+v", opts)
	}
	if got := []rune(opts[2].Label); len(got) > scoreCategoryLabelMax+1 {
		t.Errorf("选项文案没有限长：%q（上限 %d）", opts[2].Label, scoreCategoryLabelMax)
	}
	if opts[2].Value != long {
		t.Errorf("选项值被截断了：%q", opts[2].Value)
	}
}

// 分类很多时，下拉弹层里的选项只能在限高的滚动层里排布，不能画出弹层盖住
// 下面的评分列表（field.Select 只设 MaxHeight 却没有滚动层，以前会溢出）。
func TestScoresCategoryDropdownNoOverflow(t *testing.T) {
	a := testApp(t)
	a.scores = newScoresState()
	st := a.scores
	cats := make([]string, 30)
	for i := range cats {
		cats[i] = fmt.Sprintf("分类%02d", i)
	}
	st.categories = cats
	st.categoriesLoaded = true
	st.loaded = true

	tt := ui.NewTester(a.scoresWindowView, scoresWinMinWidth, scoresWindowHeight)
	tt.Frame()
	// 点分类下拉的 trigger 打开弹层。位置从实际渲染的 trigger 取，标题栏
	// 变高后 y 会整体下移，写死坐标会点空。
	trigger, ok := tt.Find("全部分类")
	if !ok {
		t.Fatalf("找不到分类下拉 trigger：%q", tt.Texts())
	}
	tt.ClickAt(trigger.X+trigger.W/2, trigger.Y+trigger.H/2)
	tt.Frame()

	first, ok := tt.Find(cats[0])
	if !ok {
		t.Fatalf("下拉没有展开：%q", tt.Texts())
	}
	for _, cat := range cats {
		r, ok := tt.Find(cat)
		if !ok || r.W == 0 {
			continue // 滚动后被裁掉的项不在可视区里
		}
		if r.Y > first.Y+256 {
			t.Errorf("分类 %q 画出弹层滚动区：first=%+v opt=%+v", cat, first, r)
		}
	}
}

// 最小窗口宽度下筛选条排得下：标签不被压缩成「评…」，刷新也没被挤到下一行。
func TestScoresFiltersFitMinWidth(t *testing.T) {
	a := testApp(t)
	a.scores = newScoresState()
	st := a.scores
	st.categories = []string{strings.Repeat("很长的分类名", 8)}
	st.categoriesLoaded = true
	st.loaded = true

	tt := ui.NewTester(a.scoresWindowView, scoresWinMinWidth, scoresWindowHeight)
	tt.Frame()
	label, ok := tt.Find("评分分类")
	if !ok {
		t.Fatalf("分类标签没有完整显示：%q", tt.Texts())
	}
	refresh, ok := tt.Find("刷新")
	if !ok {
		t.Fatalf("筛选条没有刷新按钮：%q", tt.Texts())
	}
	if refresh.Y > label.Y+label.H || refresh.Y+refresh.H < label.Y {
		t.Errorf("刷新被挤到下一行：标签 %+v / 刷新 %+v", label, refresh)
	}
	if refresh.X+refresh.W > scoresWinMinWidth {
		t.Errorf("刷新按钮溢出窗口：%+v", refresh)
	}
}

// 加载：路径 / 查询 / 回填；分类字典单独读，失败不影响评分列表。
func TestLoadScores(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/scores/categories" {
			fmt.Fprint(w, envelopeJSON(scoreCategoriesJSON))
			return
		}
		fmt.Fprint(w, envelopeJSON(scoresJSON))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.scores = newScoresState()
	st := a.scores
	a.loadScores(1)
	waitProfile(t, "评分榜", func() bool { return st.loaded })

	if len(st.items) != 2 || st.total != 63 || st.totalPages != 3 {
		t.Fatalf("评分没有回填：%+v", st)
	}
	if len(paths) != 1 || paths[0] != "/v1/scores" {
		t.Errorf("请求路径 = %v", paths)
	}
}

// 评分榜入口在侧边栏「草稿箱 / 稍后再看」那一组的下方分割线之后：带外链窗口
// 图标与「在新窗口打开」说明；点它走 showScores，headless 下至少准备好状态。
// 主页不再画评分榜 FAB。
func TestScoresSidebarEntryAndOpen(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{{Key: "k1", Title: "一条列表"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	entry, ok := tt.Find("评分榜")
	if !ok {
		t.Fatalf("侧边栏没有评分榜入口：%q", tt.Texts())
	}
	if entry.X+entry.W > sidebarWidth+1 {
		t.Errorf("评分榜入口超出 %d 宽的导航轨：%+v", sidebarWidth, entry)
	}

	// 排在「动态」下面，上面是「草稿箱 / 稍后再看」那一组，组的两头各有
	// 一条分割线，和浏览页、独立窗口入口分开。
	timeline, ok := tt.Find("动态")
	if !ok {
		t.Fatalf("侧边栏没有「动态」：%q", tt.Texts())
	}
	if entry.Y <= timeline.Y {
		t.Errorf("评分榜 y=%.0f，应在「动态」（y=%.0f）下面", entry.Y, timeline.Y)
	}
	items := a.sidebarNavItems()
	scoresAt := -1
	for i, it := range items {
		if it.Key == "scores" {
			scoresAt = i
		}
	}
	if scoresAt < 5 || !items[scoresAt-1].Divider ||
		items[scoresAt-2].Key != "watchlater" ||
		items[scoresAt-3].Key != "drafts" ||
		!items[scoresAt-4].Divider ||
		!strings.HasPrefix(items[scoresAt-5].Label, "动态") {
		t.Errorf("评分榜应在草稿箱/稍后再看一组的分割线之后：%+v", items)
	}

	if err := tt.Click("评分榜"); err != nil {
		t.Fatal(err)
	}
	if a.scores == nil {
		t.Error("点侧边栏评分榜没有准备好评分榜状态")
	}

	// 展开态额外给出「在新窗口打开」的说明，和外链窗口图标一起标明它开的是
	// 独立窗口，不是上面那种页内导航。
	a.navExpanded = true
	expanded := ui.NewTester(a.view, 1100, 760)
	expanded.Frame()
	if !expanded.HasText("在新窗口打开") {
		t.Errorf("评分榜入口没有标出是独立窗口：%q", expanded.Texts())
	}
}

// 分类字典里少掉当前分类时退回「全部分类」并重拉。
func TestScoreCategoryFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/scores/categories" {
			fmt.Fprint(w, envelopeJSON(`{"categories":["动漫"]}`))
			return
		}
		fmt.Fprint(w, envelopeJSON(scoresJSON))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.scores = newScoresState()
	st := a.scores
	st.category = "已删除的分类"
	st.lastCategory = st.category

	a.loadScoreCategories()
	waitProfile(t, "分类回退", func() bool {
		return st.categoriesLoaded && st.category == scoreCategoryAll
	})
	if !st.loaded || len(st.items) != 2 {
		t.Errorf("回退后没有重拉评分：%+v", st)
	}
}
