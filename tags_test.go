package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

const hotTagsJSON = `[
  {"tag_id":"t1","name":"职场","count":5},
  {"tag_id":"t2","name":"金币","count":4}
]`

// 热门标签的路径、周期参数与解码：tag_id / count 是线上字段，post_count 与
// id 也认，未知字段忽略。
func TestFetchHotTags(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		mu.Unlock()
		fmt.Fprint(w, envelopeJSON(hotTagsJSON))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api"}
	tags, err := c.fetchHotTags(context.Background(), "week")
	if err != nil {
		t.Fatalf("读取热门标签失败：%v", err)
	}
	if len(tags) != 2 || tags[0].Name != "职场" || tags[0].Count != 5 || tags[0].ID != "t1" {
		t.Fatalf("热门标签解析不对：%+v", tags)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/api/v1/tags/hot" {
		t.Errorf("请求路径 = %q", gotPath)
	}
	if v, err := url.ParseQuery(gotQuery); err != nil || v.Get("period") != "week" {
		t.Errorf("周期参数 = %q / %v", gotQuery, err)
	}
}

// 周期越界时回到 day；查询参数总是显式带上 period。
func TestHotTagsPeriod(t *testing.T) {
	if got := hotTagPeriod(-1); got != "day" {
		t.Errorf("负下标 = %q，期望 day", got)
	}
	if got := hotTagPeriod(len(hotTagPeriods)); got != "day" {
		t.Errorf("越界下标 = %q，期望 day", got)
	}
	for i, want := range []string{"day", "week", "month"} {
		if got := hotTagPeriod(i); got != want {
			t.Errorf("下标 %d = %q，期望 %q", i, got, want)
		}
	}
	if got := hotTagsQuery(""); got != "period=day" {
		t.Errorf("空周期查询 = %q，期望 period=day", got)
	}
	if got := hotTagsQuery("month"); got != "period=month" {
		t.Errorf("月榜查询 = %q", got)
	}
}

// 空状态显示热门标签；点一枚芯片会把名字填进搜索框并切到「标签」类型。
func TestSearchHotTagsShownAndClicked(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.hotTagsLoaded = true
	st.lastHotTagsSpan = st.hotTagsSpan
	st.hotTags = []hotTag{{ID: "t1", Name: "职场", Count: 5}, {ID: "t2", Name: "金币", Count: 4}}

	tt := ui.NewTester(a.searchWindowView, searchWindowWidth, searchWindowHeight)
	tt.Frame()
	for _, want := range []string{"热门标签", "#职场 5", "#金币 4", "输入关键词开始搜索"} {
		if !tt.HasText(want) {
			t.Errorf("搜索空状态缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("标签 职场"); err != nil {
		t.Fatalf("点热门标签失败：%v", err)
	}
	if st.q != "职场" || st.kind != int(searchTags) {
		t.Errorf("点标签后 q=%q kind=%d，期望职场 / 标签类型", st.q, st.kind)
	}
}

// 热门标签的加载状态：请求回来回填；切换周期时旧结果按 seq 丢弃。
func TestLoadHotTags(t *testing.T) {
	var mu sync.Mutex
	var periods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		periods = append(periods, r.URL.Query().Get("period"))
		mu.Unlock()
		fmt.Fprint(w, envelopeJSON(hotTagsJSON))
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	a.search = newSearchState()
	st := a.search
	a.loadHotTags(st, "week")
	waitProfile(t, "热门标签", func() bool { return st.hotTagsLoaded })

	if len(st.hotTags) != 2 || st.hotTags[0].Name != "职场" {
		t.Fatalf("热门标签没有回填：%+v", st.hotTags)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(periods) != 1 || periods[0] != "week" {
		t.Errorf("请求周期 = %v，期望 week", periods)
	}
}

// 热门标签失败时显示原因与重试，不把整页变成错误。
func TestSearchHotTagsError(t *testing.T) {
	a := testApp(t)
	a.showSearchWindow()
	st := a.search
	st.hotTagsLoaded = true
	st.hotTagsErr = "网络不可用"
	st.lastHotTagsSpan = st.hotTagsSpan

	tt := ui.NewTester(a.searchWindowView, searchWindowWidth, searchWindowHeight)
	tt.Frame()
	for _, want := range []string{"热门标签暂时不可用", "网络不可用", "重试"} {
		if !tt.HasText(want) {
			t.Errorf("热门标签错误缺少 %q：%q", want, tt.Texts())
		}
	}
}
