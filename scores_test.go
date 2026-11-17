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

// 主页 FAB 有评分榜入口；showScores 在 headless 下至少准备好状态。
func TestScoresFabAndOpen(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{{Key: "k1", Title: "一条列表"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("评分榜"); !ok {
		t.Fatalf("主页没有评分榜 FAB：%q", tt.Texts())
	}
	a.showScores()
	if a.scores == nil {
		t.Error("showScores 没有准备好评分榜状态")
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
