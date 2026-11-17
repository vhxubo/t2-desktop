package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 单节点详情的路径、字段与解码。
func TestFetchNodeDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/work" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, envelopeJSON(`{
			"slug":"work","name":"职场","description":"工作中的大小事",
			"parent_slug":"life","dimension":"work","post_count":42
		}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api"}
	d, err := c.fetchNodeDetail(context.Background(), "work")
	if err != nil {
		t.Fatalf("读取节点详情失败：%v", err)
	}
	if d.Slug != "work" || d.Name != "职场" || d.Description != "工作中的大小事" ||
		d.ParentSlug != "life" || d.PostCount != 42 {
		t.Errorf("节点详情 = %+v", d)
	}
	if _, err := c.fetchNodeDetail(context.Background(), ""); err == nil {
		t.Error("空 slug 应当报错")
	}
}

// 选中节点后请求详情，结果回填到发帖窗口的状态里。
func TestLoadNodeDetail(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, envelopeJSON(`{"slug":"community","name":"社区讨论","description":"聊聊社区"}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	st := composeReady(a)
	a.loadNodeDetail(st, "community")
	waitProfile(t, "节点简介", func() bool {
		return st.nodeDetailSlug == "community" && !st.nodeDetailLoading &&
			st.nodeDetail.Description != ""
	})
	if len(paths) != 1 || paths[0] != "/v1/nodes/community" {
		t.Errorf("请求路径 = %v", paths)
	}
	if st.nodeDetail.Name != "社区讨论" || st.nodeDetailErr != "" {
		t.Errorf("节点详情没有回填：%+v / %q", st.nodeDetail, st.nodeDetailErr)
	}
}

// 发帖窗口在选中节点下方显示简介；读取失败只影响这一行说明。
func TestComposeShowsNodeDescription(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.nodeDetailSlug = "community"
	st.nodeDetail = nodeDetail{Slug: "community", Name: "社区讨论", Description: "聊聊社区里的事"}

	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if !tt.HasText("聊聊社区里的事") {
		t.Errorf("发帖窗口没有显示节点简介：%q", tt.Texts())
	}

	st.nodeDetailErr = "网络不可用"
	tt.Frame()
	if !tt.HasText("节点简介暂时不可用") {
		t.Errorf("简介失败没有说明：%q", tt.Texts())
	}
}
