package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// avatar_url 是不透明地址：新的随机标识与历史形态都原样收下，不解析路径、
// 不改写，也不会因为换了头像就把旧地址当成另一个人。
func TestDecodeAvatarURLOpaqueAddress(t *testing.T) {
	for _, want := range []string{
		`https://r2.2libra.com/avatars/q7/8f3Kx2mQ7pR1tV5z.webp`,
		`https://r2.2libra.com/avatars/12345_1699999999.png`,
		`https://cdn.example.com/a/b.webp?v=2`,
	} {
		raw, err := json.Marshal(map[string]string{"avatar_url": want})
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeAvatarURL(raw); got != want {
			t.Errorf("头像地址 = %q，期望原样返回 %q", got, want)
		}
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

// 选择本地图片后先进裁剪编辑器，确认上传的是 512×512 的 PNG；资料头与会话
// 都要换到新地址。
func TestUploadAvatarFromFile(t *testing.T) {
	type uploaded struct {
		mime string
		w, h int
	}
	gotCh := make(chan uploaded, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/me/avatar" {
			t.Errorf("换头像请求 = %s %s", r.Method, r.URL.Path)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺少 file 字段：%v", err)
		} else {
			data, _ := io.ReadAll(file)
			up := uploaded{mime: header.Header.Get("Content-Type")}
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				up.w, up.h = img.Bounds().Dx(), img.Bounds().Dy()
			}
			gotCh <- up
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

	a.pickAvatar(a.profile)
	waitProfile(t, "读取待裁剪的图片", func() bool { return a.profile.avatarCrop.open })
	a.confirmAvatarCrop(a.profile)
	waitProfile(t, "换头像", func() bool {
		return !a.profile.avatarBusy && !a.profile.avatarCrop.open &&
			a.profile.user.Avatar == "https://r2.2libra.com/avatars/new.png"
	})
	if got := a.auth.avatarURL(); got != "https://r2.2libra.com/avatars/new.png" {
		t.Errorf("会话里的头像 = %q", got)
	}
	up := <-gotCh
	if up.mime != "image/png" {
		t.Errorf("上传的 Content-Type = %q，期望 image/png", up.mime)
	}
	if up.w != avatarCropOutputSide || up.h != avatarCropOutputSide {
		t.Errorf("上传的图片 = %d×%d，期望 %d×%d",
			up.w, up.h, avatarCropOutputSide, avatarCropOutputSide)
	}
}

// 重置头像后会话里的地址与资料头一起清空，界面回到生成的图案。
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

// 资料头的图标点开气泡：说明、选图与重置入口都在；选图后打开裁剪框，
// 确认上传才收起气泡。费用在气泡和裁剪框里都提前写明。
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
		"换一张头像", "500 金币", "选择图片…", "重置为默认头像",
	} {
		if !tt.HasText(want) {
			t.Errorf("换头像气泡缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("选择图片…"); err != nil {
		t.Fatalf("点「选择图片…」失败：%v", err)
	}
	waitProfile(t, "打开裁剪编辑器", func() bool { return a.profile.avatarCrop.open })
	tt.Frame()
	for _, want := range []string{
		"调整头像", "头像裁剪区域", "缩放",
		"确定上传将消耗 500 金币。", "上传（500 金币）",
	} {
		if !tt.HasText(want) {
			t.Errorf("裁剪编辑器缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("上传（500 金币）"); err != nil {
		t.Fatalf("点「上传（500 金币）」失败：%v", err)
	}
	waitProfile(t, "裁剪后上传", func() bool {
		return !a.profile.avatarBusy && !a.profile.avatarOpen &&
			!a.profile.avatarCrop.open &&
			a.profile.user.Avatar == "https://r2.2libra.com/avatars/new.png"
	})
}

// 裁剪框里的交互真的接在界面事件上：滚轮放大，框内拖动移动裁剪框，
// 拖角调整框的大小，框外拖动平移图片，松手后不再跟指针。
func TestAvatarCropWheelAndDrag(t *testing.T) {
	a := profileWindowApp(t)
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	a.profile.avatarCrop = avatarCropState{
		open: true, img: img, bmp: ui.NewBitmap(img),
		iw: 1200, ih: 800, zoom: avatarCropMinZoom,
	}
	a.profile.avatarCrop.resetCrop()

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	stage, ok := tt.Find("头像裁剪区域")
	if !ok {
		t.Fatalf("裁剪预览没画出来：%q", tt.Texts())
	}
	cx, cy := stage.X+stage.W/2, stage.Y+stage.H/2

	tt.Wheel(cx, cy, 0, -1)
	tt.Frame()
	if z := a.profile.avatarCrop.zoom; z <= avatarCropMinZoom {
		t.Errorf("滚轮向上没有放大：zoom = %v", z)
	}

	// 框内拖动：移动裁剪框本身。
	beforeX, beforeY := a.profile.avatarCrop.cropX, a.profile.avatarCrop.cropY
	tt.Press(cx, cy)
	tt.Move(cx+30, cy+20)
	tt.Release(cx+30, cy+20)
	tt.Frame()
	if got := a.profile.avatarCrop.cropX; got <= beforeX {
		t.Errorf("框内向右拖动后 cropX = %v，期望大于 %v", got, beforeX)
	}
	if got := a.profile.avatarCrop.cropY; got <= beforeY {
		t.Errorf("框内向下拖动后 cropY = %v，期望大于 %v", got, beforeY)
	}

	// 拖右下角：右下边跟手，框变大。先把框放回中间，避免它已经被拖到
	// 舞台右边缘、右下角落在元素边界上点不到。
	a.profile.avatarCrop.resetCrop()
	tt.Frame()
	f := a.profile.avatarCrop.cropRect()
	right := stage.X + f.X + f.W
	bottom := stage.Y + f.Y + f.H
	beforeSide := a.profile.avatarCrop.cropSide
	tt.Press(right, bottom)
	tt.Move(right+25, bottom+25)
	tt.Release(right+25, bottom+25)
	tt.Frame()
	if got := a.profile.avatarCrop.cropSide; got <= beforeSide {
		t.Errorf("拖右下角后 cropSide = %v，期望大于 %v", got, beforeSide)
	}

	// 框外拖动：平移整张图片。
	tt.Press(stage.X+2, stage.Y+2)
	tt.Move(stage.X+22, stage.Y+7)
	tt.Release(stage.X+22, stage.Y+7)
	tt.Frame()
	if got := a.profile.avatarCrop.panX; got <= 0 {
		t.Errorf("框外向右拖动后 panX = %v，期望大于 0", got)
	}
	// 松手后再移动指针：平移不再变化。
	pan := a.profile.avatarCrop.panX
	tt.Move(stage.X+120, stage.Y+80)
	tt.Frame()
	if got := a.profile.avatarCrop.panX; got != pan {
		t.Errorf("松手后仍跟着指针平移：panX = %v，期望停在 %v", got, pan)
	}
}

// 裁剪框的命中区域与四边四角的调整：角优先于边、边优先于框内，拖动
// 时对边或对角固定，边长不会小于最小值，也不会越出舞台。
func TestAvatarCropFrameHitAndResize(t *testing.T) {
	c := &avatarCropState{img: image.NewRGBA(image.Rect(0, 0, 800, 400)), iw: 800, ih: 400, zoom: 1}
	c.resetCrop()
	r := c.cropRect()
	for _, tc := range []struct {
		name string
		x, y float32
		want int
	}{
		{"中心", r.X + r.W/2, r.Y + r.H/2, cropDragMove},
		{"左上角", r.X, r.Y, cropDragTopLeft},
		{"右下角", r.X + r.W, r.Y + r.H, cropDragBottomRight},
		{"左边", r.X, r.Y + r.H/2, cropDragLeft},
		{"底边", r.X + r.W/2, r.Y + r.H, cropDragBottom},
		{"框外", 0, 0, cropDragNone},
	} {
		if got := c.hit(tc.x, tc.y); got != tc.want {
			t.Errorf("%s 命中 = %d，期望 %d", tc.name, got, tc.want)
		}
	}

	// 拖左边向右 20：右边固定，边长变小。
	c.drag = cropDragLeft
	c.dragRect = c.cropRect()
	c.dragPointerX, c.dragPointerY = r.X, r.Y+r.H/2
	c.dragTo(r.X+20, r.Y+r.H/2)
	if c.cropSide >= r.W {
		t.Errorf("拖左边后边长 = %v，期望小于 %v", c.cropSide, r.W)
	}
	if got := c.cropRect(); math.Abs(float64(got.X+got.W-(r.X+r.W))) > 0.5 {
		t.Errorf("拖左边后右边移动了：%v，期望 %v", got.X+got.W, r.X+r.W)
	}

	// 拖右下角向左上 20：左上角固定，边长变小。
	c.resetCrop()
	r = c.cropRect()
	c.drag = cropDragBottomRight
	c.dragRect = r
	c.dragPointerX, c.dragPointerY = r.X+r.W, r.Y+r.H
	c.dragTo(r.X+r.W-20, r.Y+r.H-20)
	if c.cropSide >= r.W {
		t.Errorf("拖右下角后边长 = %v，期望小于 %v", c.cropSide, r.W)
	}
	if got := c.cropRect(); math.Abs(float64(got.X-r.X)) > 0.5 || math.Abs(float64(got.Y-r.Y)) > 0.5 {
		t.Errorf("拖右下角后左上角移动了：(%v, %v)，期望 (%v, %v)", got.X, got.Y, r.X, r.Y)
	}

	// 最小值与舞台边界。
	c.setCropRect(0, 0, 1)
	if c.cropSide != avatarCropMinSide {
		t.Errorf("最小边长 = %v，期望 %v", c.cropSide, avatarCropMinSide)
	}
	c.setCropRect(999, 999, 999)
	if f := c.cropRect(); f.W != avatarCropStageSize ||
		f.X+f.W > avatarCropStageSize+0.5 || f.Y+f.H > avatarCropStageSize+0.5 {
		t.Errorf("裁剪框越出舞台：%+v", f)
	}
}

// 指针是一小步一小步来的：连续多次移动必须累计，不能每次都以按下位置
// 为基准，否则裁剪框会粘在原处或回弹。
func TestAvatarCropDragAccumulatesSteps(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	c := &avatarCropState{open: true, img: img, bmp: ui.NewBitmap(img), iw: 1200, ih: 800, zoom: 1}
	c.resetCrop()
	r := c.cropRect()
	cx, cy := r.X+r.W/2, r.Y+r.H/2

	c.input(ui.InputEvent{Kind: ui.InputPointerDown, X: cx, Y: cy})
	for i := 1; i <= 2; i++ {
		c.input(ui.InputEvent{Kind: ui.InputPointerMove, X: cx + float32(i)*8, Y: cy})
	}
	c.input(ui.InputEvent{Kind: ui.InputPointerUp, X: cx + 16, Y: cy})
	if got, want := c.cropX, r.X+16; math.Abs(float64(got-want)) > 0.01 {
		t.Errorf("连续两步移动后 cropX = %v，期望 %v", got, want)
	}

	// 拖右边同样按步累计，不能用最后一步的增量覆盖前面的移动。
	c.resetCrop()
	r = c.cropRect()
	cx, cy = r.X+r.W, r.Y+r.H/2
	c.input(ui.InputEvent{Kind: ui.InputPointerDown, X: cx, Y: cy})
	for i := 1; i <= 2; i++ {
		c.input(ui.InputEvent{Kind: ui.InputPointerMove, X: cx + float32(i)*8, Y: cy})
	}
	c.input(ui.InputEvent{Kind: ui.InputPointerUp, X: cx + 16, Y: cy})
	if got, want := c.cropSide, r.W+16; math.Abs(float64(got-want)) > 0.01 {
		t.Errorf("连续两步拖右边后边长 = %v，期望 %v", got, want)
	}
}

// 缩放滑杆与拖动的边界：最小只到铺满裁剪框，最大 4 倍；平移不能把图片
// 拖到露出裁剪框底色。
func TestAvatarCropZoomAndPanLimits(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	c := &avatarCropState{img: img, iw: 800, ih: 400, zoom: avatarCropMinZoom}
	c.setZoom(0.1)
	if c.zoom != avatarCropMinZoom {
		t.Errorf("缩放下限 = %v，期望 %v", c.zoom, avatarCropMinZoom)
	}
	c.setZoom(99)
	if c.zoom != avatarCropMaxZoom {
		t.Errorf("缩放上限 = %v，期望 %v", c.zoom, avatarCropMaxZoom)
	}

	c.setZoom(1)
	// 800×400 覆盖 200 的方形舞台：显示 400×200，左边最多移出 100，上下没有余量。
	c.panX, c.panY = 999, 50
	c.clampPan()
	if math.Abs(float64(c.panX-100)) > 1e-3 || math.Abs(float64(c.panY)) > 1e-3 {
		t.Errorf("平移未夹紧：panX=%v panY=%v，期望 100 / 0", c.panX, c.panY)
	}
	c.panX, c.panY = -999, -50
	c.clampPan()
	if math.Abs(float64(c.panX+100)) > 1e-3 || math.Abs(float64(c.panY)) > 1e-3 {
		t.Errorf("平移未夹紧：panX=%v panY=%v，期望 -100 / 0", c.panX, c.panY)
	}
}

// 裁剪结果固定是 512×512 的 PNG；源图可以是任意宽高比，缩放后也一样。
func TestAvatarCropRenderSquarePNG(t *testing.T) {
	for _, zoom := range []float64{avatarCropMinZoom, 2, avatarCropMaxZoom} {
		img := image.NewRGBA(image.Rect(0, 0, 900, 300))
		for y := range 300 {
			for x := range 900 {
				img.SetRGBA(x, y, color.RGBA{G: 220, A: 255})
			}
		}
		c := &avatarCropState{img: img, iw: 900, ih: 300, zoom: zoom}
		data, err := c.render()
		if err != nil {
			t.Fatalf("zoom %v 渲染失败：%v", zoom, err)
		}
		got, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("zoom %v 产出的不是 PNG：%v", zoom, err)
		}
		if b := got.Bounds(); b.Dx() != avatarCropOutputSide || b.Dy() != avatarCropOutputSide {
			t.Errorf("zoom %v 输出 = %d×%d，期望 %d×%d", zoom,
				b.Dx(), b.Dy(), avatarCropOutputSide, avatarCropOutputSide)
		}
		if _, _, _, a := got.At(avatarCropOutputSide/2, avatarCropOutputSide/2).RGBA(); a == 0 {
			t.Errorf("zoom %v 输出中心是透明的", zoom)
		}
	}
}
