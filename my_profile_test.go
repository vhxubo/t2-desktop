package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 本人的「我的」窗口优先读 GET /v1/me：不需要用户名，字段与公开资料同构。
func TestLoadProfileInfoUsesMeForSelf(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/me" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, envelopeJSON(`{
			"username":"reader","display_name":"读者","avatar_url":"https://r2.2libra.com/a.png",
			"level":7,"is_pro":true,"follower_count":12,"following_count":3,
			"has_readme":true,"show_setting":true
		}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	ps := newProfileState()
	a.profile = ps
	a.loadProfileInfo("reader")
	waitProfile(t, "本人资料", func() bool { return ps.infoLoaded && !ps.infoLoading })

	if len(paths) != 1 || paths[0] != "/v1/me" {
		t.Fatalf("本人资料请求 = %v，期望 /v1/me", paths)
	}
	if ps.user.Name != "reader" || ps.user.DisplayName != "读者" ||
		!ps.user.IsPro || ps.user.Followers != 12 {
		t.Errorf("本人资料解析不对：%+v", ps.user)
	}
	if ps.infoErr != "" {
		t.Errorf("读取成功后仍有错误：%q", ps.infoErr)
	}
}

// /v1/me 失败时退回公开资料端点，页面不因为一次接口问题整块空着。
func TestLoadProfileInfoFallsBackToUserPath(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/me":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","d":null}`)
		case "/v1/users/reader":
			fmt.Fprint(w, envelopeJSON(`{"username":"reader","display_name":"读者"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	ps := newProfileState()
	a.profile = ps
	a.loadProfileInfo("reader")
	waitProfile(t, "退回公开资料", func() bool { return ps.infoLoaded && !ps.infoLoading })

	if len(paths) != 2 || paths[0] != "/v1/me" || paths[1] != "/v1/users/reader" {
		t.Fatalf("请求序列 = %v", paths)
	}
	if ps.user.Name != "reader" {
		t.Errorf("退回后资料没有回填：%+v", ps.user)
	}
}
