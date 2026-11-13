package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 内置协议的测试统一这么搭：把 httptest 的地址塞进 Endpoint，适配器自己会从
// 同一个 origin 拼出「上传页」或「/csrf-token」，所以一个测试服务器就能同时
// 扮演令牌端点与上传端点。

func adapterTestApp(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	a := testApp(t)
	a.client.HTTP = srv.Client()
	return a
}

func uploadTestSource() uploadSource {
	return uploadSource{Name: "photo.png", Mime: "image/png", Data: pngHeadForUpload}
}

// MJJ.Today / ImgBB 跑在 Chevereto 上：先从上上传页扒 auth_token，再带着它
// 提交文件。MJJ 还会把 i.mji.rip 换成国内能解析的 i.mij.rip。
func TestUploadToCheveretoScrapesToken(t *testing.T) {
	var gotFields map[string]string
	var gotFile, gotFilename string
	uploadPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			_, _ = fmt.Fprint(w, `<script>PF.obj.config.auth_token = "abc123";</script>`)
			return
		}
		uploadPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		gotFields = map[string]string{
			"type": r.FormValue("type"), "action": r.FormValue("action"),
			"auth_token": r.FormValue("auth_token"), "nsfw": r.FormValue("nsfw"),
		}
		if f, hdr, err := r.FormFile("source"); err == nil {
			f.Close()
			gotFilename = hdr.Filename
			gotFile = "source"
		}
		_, _ = fmt.Fprint(w, `{"status_code":200,"image":{"url":"https://i.mji.rip/2026/11/a.png"}}`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "mjj", Name: "MJJ.Today", Kind: imageHostGeneric,
		Adapter: adapterMJJ, Endpoint: srv.URL + "/json"}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://i.mij.rip/2026/11/a.png" {
		t.Errorf("图片地址 = %q，期望换成 i.mij.rip", got)
	}
	if uploadPath != "/json" {
		t.Errorf("上传路径 = %q", uploadPath)
	}
	if gotFile != "source" || gotFilename != "photo.png" {
		t.Errorf("文件字段 / 文件名 = %q / %q", gotFile, gotFilename)
	}
	if gotFields["auth_token"] != "abc123" || gotFields["type"] != "file" ||
		gotFields["action"] != "upload" || gotFields["nsfw"] != "0" {
		t.Errorf("提交字段 = %v", gotFields)
	}

	// 上传页里没有令牌（改版 / 被拦截）时要说人话，不能只报「上传失败」。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer bad.Close()
	a.client.HTTP = bad.Client()
	h.Endpoint = bad.URL + "/json"
	_, err = a.uploadImage(context.Background(), h, uploadTestSource())
	if err == nil || !strings.Contains(err.Error(), "auth_token") {
		t.Errorf("缺令牌的提示 = %v", err)
	}
}

// ImgBB 与 MJJ 同一套协议，只是站点不同：确认适配器真的走了传入的地址。
func TestUploadToImgBBUsesOwnEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload":
			_, _ = fmt.Fprint(w, `PF.obj.config.auth_token="tok9"`)
		case "/json":
			gotPath = r.URL.Path
			_ = r.ParseMultipartForm(1 << 20)
			if r.FormValue("auth_token") != "tok9" {
				t.Errorf("auth_token = %q", r.FormValue("auth_token"))
			}
			_, _ = fmt.Fprint(w, `{"status_code":200,"image":{"url":"https://i.ibb.co/x/a.png"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "imgbb", Name: "ImgBB", Kind: imageHostGeneric,
		Adapter: adapterImgBB, Endpoint: srv.URL + "/json"}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://i.ibb.co/x/a.png" || gotPath != "/json" {
		t.Errorf("地址 = %q，路径 = %q", got, gotPath)
	}
}

// Skyimg 要先取 CSRF 令牌，上传时带 origin 与 x-sync-token；地址可能是站内
// 路径，要补成完整地址。WebP 那条走另一个 sync-token 与 ?webp=true。
func TestUploadToSkyimgUsesCSRFAndSyncToken(t *testing.T) {
	for _, c := range []struct {
		name    string
		adapter string
		webp    bool
		want    string
	}{
		{"原图", adapterSkyimg, false, skyimgSyncTokenRaw},
		{"WebP", adapterSkyimgWebp, true, skyimgSyncTokenWebp},
	} {
		t.Run(c.name, func(t *testing.T) {
			var gotToken, gotSync, gotOrigin, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/csrf-token" {
					_, _ = fmt.Fprint(w, `{"csrfToken":"csrf-1"}`)
					return
				}
				gotToken = r.Header.Get("X-Csrf-Token")
				gotSync = r.Header.Get("X-Sync-Token")
				gotOrigin = r.Header.Get("Origin")
				gotQuery = r.URL.RawQuery
				_ = r.ParseMultipartForm(1 << 20)
				_, _ = fmt.Fprint(w, `[{"url":"/i/2026/a.png"}]`)
			}))
			defer srv.Close()

			a := adapterTestApp(t, srv)
			h := imageHost{ID: "skyimg", Name: "Skyimg", Kind: imageHostGeneric,
				Adapter: c.adapter, Endpoint: srv.URL + "/upload"}
			got, err := a.uploadImage(context.Background(), h, uploadTestSource())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got, srv.URL+"/i/2026/a.png") {
				t.Errorf("相对地址没有补全：%q", got)
			}
			if gotToken != "csrf-1" || gotSync != c.want {
				t.Errorf("令牌 = %q / %q，期望 %q", gotToken, gotSync, c.want)
			}
			if gotOrigin != srv.URL {
				t.Errorf("Origin = %q，期望 %q", gotOrigin, srv.URL)
			}
			if c.webp && !strings.Contains(gotQuery, "webp=true") {
				t.Errorf("WebP 变体没有带 webp=true：%q", gotQuery)
			}
			if !c.webp && strings.Contains(gotQuery, "webp=true") {
				t.Errorf("原图不该带 webp=true：%q", gotQuery)
			}
		})
	}
}

// Imgur 的公共 Client-ID 会被限流：一个不成就换下一个，直到有一个能用。
func TestUploadToImgurRotatesClientIDs(t *testing.T) {
	var tried int
	var auths []string
	rateLimited := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tried++
		auths = append(auths, r.Header.Get("Authorization"))
		// 第一次一律限流，逼适配器换一个 Client-ID 重试。
		if rateLimited && tried == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"success":false,"data":{"error":"rate limited"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"success":true,"data":{"link":"https://i.imgur.com/a.png"}}`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "imgur", Name: "Imgur", Kind: imageHostGeneric,
		Adapter: adapterImgur, Endpoint: srv.URL}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://i.imgur.com/a.png" {
		t.Errorf("地址 = %q", got)
	}
	if tried != 2 {
		t.Errorf("被限流后应换一个 Client-ID 重试，实际请求 %d 次", tried)
	}
	if len(auths) == 2 && auths[0] == auths[1] {
		t.Errorf("重试还用了同一个 Client-ID：%q", auths[0])
	}
	// 填了自己的 Client-ID 就只用它。
	rateLimited, tried, auths = false, 0, nil
	h.Token = "mine"
	if _, err := a.uploadImage(context.Background(), h, uploadTestSource()); err != nil {
		t.Fatal(err)
	}
	if tried != 1 || auths[0] != "Client-ID mine" {
		t.Errorf("填了 Client-ID 应只用它：请求 %d 次，%v", tried, auths)
	}
	if ids := rotateIDs("", imgurPublicClientIDs); len(ids) != len(imgurPublicClientIDs) {
		t.Errorf("没填 Client-ID 时应拿到全部内置 ID，实际 %d 个", len(ids))
	}
}

// 111666.best 的令牌在 auth-token 头里，文件字段叫 payload，src 可能是站内路径。
func TestUploadTo111666UsesAuthTokenHeader(t *testing.T) {
	target := best111666PublicTokens[0]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("auth-token") != target {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"ok":false}`)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		if _, _, err := r.FormFile("payload"); err != nil {
			t.Errorf("文件字段不是 payload：%v", err)
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"src":"/img/a.png"}`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "111666_best", Name: "111666.best", Kind: imageHostGeneric,
		Adapter: adapter111666, Endpoint: srv.URL}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/img/a.png" {
		t.Errorf("地址 = %q，期望补成完整地址", got)
	}
}

// Tikolu 只回图片 ID，地址要拼成 <接口>/<ID>。
func TestUploadToTikoluBuildsURLFromID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		if r.FormValue("upload") != "true" {
			t.Errorf("缺少 upload=true：%q", r.FormValue("upload"))
		}
		_, _ = fmt.Fprint(w, `{"status":"uploaded","id":"AbC123"}`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "tikolu", Name: "Tikolu", Kind: imageHostGeneric,
		Adapter: adapterTikolu, Endpoint: srv.URL + "/i/"}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/i/AbC123" {
		t.Errorf("地址 = %q", got)
	}
}

// StarDots 要 PUT、要带齐四个签名头，sign = upper(md5(timestamp|secret|nonce))。
func TestUploadToStarDotsSignsRequest(t *testing.T) {
	var gotMethod, gotSign, gotSpace, gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		ts, nonce := r.Header.Get("X-Stardots-Timestamp"), r.Header.Get("X-Stardots-Nonce")
		sum := md5.Sum([]byte(ts + "|secret-1|" + nonce))
		gotSign = r.Header.Get("X-Stardots-Sign")
		if want := strings.ToUpper(hex.EncodeToString(sum[:])); gotSign != want {
			t.Errorf("签名 = %q，期望 %q", gotSign, want)
		}
		if r.Header.Get("X-Stardots-Key") != "key-1" {
			t.Errorf("Key = %q", r.Header.Get("X-Stardots-Key"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		gotSpace = r.FormValue("space")
		gotFilename = r.FormValue("filename")
		_, _ = fmt.Fprint(w, `{"success":true,"data":{"url":"https://cdn.stardots.io/a.png"}}`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{ID: "stardots", Name: "StarDots", Kind: imageHostGeneric,
		Adapter: adapterStarDots, Endpoint: srv.URL, Token: "key-1", Secret: "secret-1", Bucket: "bucket-1"}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cdn.stardots.io/a.png" {
		t.Errorf("地址 = %q", got)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("方法 = %q，期望 PUT", gotMethod)
	}
	if gotSpace != "bucket-1" || gotFilename != "photo.png" {
		t.Errorf("space / filename = %q / %q", gotSpace, gotFilename)
	}

	// 缺凭据时给出能照着做的提示，不发请求。
	_, err = a.uploadImage(context.Background(), imageHost{
		ID: "stardots", Name: "StarDots", Kind: imageHostGeneric,
		Adapter: adapterStarDots, Endpoint: srv.URL,
	}, uploadTestSource())
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Errorf("缺凭据的提示 = %v", err)
	}
}

// 通用图床也要能吃下「数组 + 站内路径」的响应（Photo.Lily / Appinn 就是这样），
// 附加字段里的 {{filename}} 要换成这张图的名字。
func TestUploadToGenericResolvesRelativeURL(t *testing.T) {
	var gotFilenameField string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		gotFilenameField = r.FormValue("filename")
		_, _ = fmt.Fprint(w, `[{"src":"/img/2026/11/a.png"}]`)
	}))
	defer srv.Close()

	a := adapterTestApp(t, srv)
	h := imageHost{
		ID: "appinn", Name: "Appinn 图床", Kind: imageHostGeneric, Endpoint: srv.URL,
		Field: "file", Form: "filename={{filename}}", URLPath: "0.src",
		MaxBytes: maxImageMaxBytes, Enabled: true,
	}
	got, err := a.uploadImage(context.Background(), h, uploadTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/img/2026/11/a.png" {
		t.Errorf("地址 = %q，期望补成完整地址", got)
	}
	if gotFilenameField != "photo.png" {
		t.Errorf("filename 字段 = %q", gotFilenameField)
	}
}

// 每家内置协议都要有默认地址，否则「留空用内置地址」无从谈起；上限也要按
// 各家自己的规则夹住。
func TestAdapterDefaults(t *testing.T) {
	for _, adapter := range []string{adapterImgur, adapterTikolu, adapterMJJ,
		adapterImgBB, adapterSkyimg, adapterSkyimgWebp, adapter111666, adapterStarDots} {
		if adapterEndpoint(adapter) == "" {
			t.Errorf("%s 没有默认接口地址", adapter)
		}
	}
	if got := adapterEndpoint(adapterSkyimgWebp); !strings.Contains(got, "webp=true") {
		t.Errorf("Skyimg WebP 的默认地址 = %q", got)
	}
	if got := adapterMaxBytes(adapterTikolu); got != 8<<20 {
		t.Errorf("Tikolu 上限 = %d，期望 8 MB", got)
	}
	if got := clampImageHost(imageHost{
		ID: "t", Name: "Tikolu", Adapter: adapterTikolu, MaxBytes: 20 << 20,
	}).MaxBytes; got != 8<<20 {
		t.Errorf("超限的 Tikolu 上限应夹到 8 MB，实际 %d", got)
	}
	// 内置协议的字段名由适配器决定，不该被补成通用图床的 file。
	if got := clampImageHost(imageHost{
		ID: "m", Name: "MJJ", Adapter: adapterMJJ, MaxBytes: defaultImageMaxBytes,
	}).Field; got != "" {
		t.Errorf("内置协议不该被补字段名：%q", got)
	}
	// 未知协议要拦在保存前。
	if err := validateImageHost(imageHost{
		ID: "x", Name: "未知", Kind: imageHostGeneric, Adapter: "nope", Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "协议") {
		t.Errorf("未知协议的校验结果 = %v", err)
	}
	// 内置协议留空接口地址是合法的；填了但写错仍要拦下。
	if err := validateImageHost(imageHost{
		ID: "m", Name: "MJJ", Kind: imageHostGeneric, Adapter: adapterMJJ, Enabled: true,
	}); err != nil {
		t.Errorf("留空地址的内置协议不该报错：%v", err)
	}
	if err := validateImageHost(imageHost{
		ID: "m", Name: "MJJ", Kind: imageHostGeneric, Adapter: adapterMJJ,
		Endpoint: "ftp://x.test", Enabled: true,
	}); err == nil {
		t.Error("写错的接口地址应被拦下")
	}
}

// 通用图床的 multipart 解析：确认 sendMultipart 真的按给定方法发请求。
func TestSendMultipartUsesGivenMethod(t *testing.T) {
	var gotMethod, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotType = r.Header.Get("Content-Type")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	if _, _, err := c.sendMultipart(context.Background(), http.MethodPut, srv.URL, "",
		nil, nil, "file", "a.png", "image/png", pngHeadForUpload); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("方法 = %q", gotMethod)
	}
	if mt, _, err := mime.ParseMediaType(gotType); err != nil || mt != "multipart/form-data" {
		t.Errorf("Content-Type = %q", gotType)
	}
}
