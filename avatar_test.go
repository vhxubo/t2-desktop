package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 换头像的 multipart 契约：字段名 file、文件名 / Content-Type、鉴权与地址解析。
func TestUploadAvatarRequest(t *testing.T) {
	type request struct {
		filename, mime, auth string
		size                 int
	}
	reqCh := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(avatarMaxBytes); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺少 file 字段：%v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		reqCh <- request{
			filename: header.Filename,
			mime:     header.Header.Get("Content-Type"),
			auth:     r.Header.Get("Authorization"),
			size:     len(data),
		}
		fmt.Fprint(w, envelopeJSON(`{"avatar_url":"https://r2.2libra.com/avatars/new.png"}`))
	}))
	defer srv.Close()

	data := testUploadPNG()
	c := &Client{Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	url, err := c.uploadAvatar(context.Background(),
		uploadSource{Name: "me.png", Mime: "image/png"}, data)
	if err != nil {
		t.Fatalf("上传头像失败：%v", err)
	}
	if url != "https://r2.2libra.com/avatars/new.png" {
		t.Errorf("头像地址 = %q", url)
	}
	req := <-reqCh
	if req.filename != "me.png" || req.mime != "image/png" || req.size != len(data) {
		t.Errorf("文件字段不对：%+v", req)
	}
	if req.auth != "Bearer tok" {
		t.Errorf("鉴权头 = %q", req.auth)
	}
}

// 重置头像：DELETE 同一路径，响应里的 null 解成空串。
func TestDeleteAvatarRequest(t *testing.T) {
	var method, path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, envelopeJSON(`{"avatar_url":null}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	url, err := c.deleteAvatar(context.Background())
	if err != nil {
		t.Fatalf("重置头像失败：%v", err)
	}
	if url != "" {
		t.Errorf("重置后的地址 = %q，期望空串", url)
	}
	if method != http.MethodDelete || path != "/api/v1/me/avatar" || auth != "Bearer tok" {
		t.Errorf("请求 = %s %s auth=%q", method, path, auth)
	}
}

// 选择本地图片后：资料头、会话与账号索引都要换到新地址，并发一次金币事件。
func TestUploadAvatarFromFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/me/avatar" {
			t.Errorf("换头像请求 = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, envelopeJSON(`{"avatar_url":"https://r2.2libra.com/avatars/new.png"}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.profile.user.Avatar = "https://r2.2libra.com/avatars/old.png"
	path := filepath.Join(t.TempDir(), "me.png")
	if err := os.WriteFile(path, testUploadPNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	a.pickImagePaths = func() ([]string, error) { return []string{path}, nil }
	events := make(chan coinEvent, 1)
	a.onCoinsChanged = func(e coinEvent) { events <- e }

	a.pickAvatar(a.profile)
	waitProfile(t, "换头像", func() bool {
		return !a.profile.avatarBusy &&
			a.profile.user.Avatar == "https://r2.2libra.com/avatars/new.png"
	})
	if got := a.auth.avatarURL(); got != "https://r2.2libra.com/avatars/new.png" {
		t.Errorf("会话里的头像 = %q", got)
	}
	select {
	case e := <-events:
		if e != coinEventAvatar {
			t.Errorf("金币事件 = %q，期望 %q", e, coinEventAvatar)
		}
	default:
		t.Error("换头像后没有发出金币事件")
	}
}

// 重置头像后会话里的地址与资料头一起清空，界面回到首字母占位。
func TestResetAvatarFromProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/me/avatar" {
			t.Errorf("重置头像请求 = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, envelopeJSON(`{"avatar_url":null}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.profile.user.Avatar = "https://r2.2libra.com/avatars/old.png"
	a.resetAvatar(a.profile)
	waitProfile(t, "重置头像", func() bool {
		return !a.profile.avatarBusy && a.profile.user.Avatar == ""
	})
	if got := a.auth.avatarURL(); got != "" {
		t.Errorf("重置后会话仍留着头像：%q", got)
	}
}

// 资料头的图标点开气泡：说明、选图与重置入口都在；选图成功后气泡收起。
func TestProfileAvatarPanel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, envelopeJSON(`{"avatar_url":"https://r2.2libra.com/avatars/new.png"}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.profile.user.Avatar = "https://r2.2libra.com/avatars/old.png"
	a.profile.user.Name = "reader"
	path := filepath.Join(t.TempDir(), "me.png")
	if err := os.WriteFile(path, testUploadPNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	a.pickImagePaths = func() ([]string, error) { return []string{path}, nil }

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("更换头像"); err != nil {
		t.Fatalf("点「更换头像」失败：%v", err)
	}
	tt.Frame()
	if !a.profile.avatarOpen {
		t.Fatal("点「更换头像」没有打开气泡")
	}
	for _, want := range []string{
		"换一张头像", "首次设置免费", "选择图片…", "重置为默认头像",
	} {
		if !tt.HasText(want) {
			t.Errorf("换头像气泡缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("选择图片…"); err != nil {
		t.Fatalf("点「选择图片…」失败：%v", err)
	}
	waitProfile(t, "气泡里换头像", func() bool {
		return !a.profile.avatarBusy && !a.profile.avatarOpen &&
			a.profile.user.Avatar == "https://r2.2libra.com/avatars/new.png"
	})
}
