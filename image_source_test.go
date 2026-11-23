package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 图片代理与动画 WebP：2Libra 会把正文里的 gif 改写成 wsrv.nl + output=webp，
// 桌面端解不了动画 WebP，必须在请求前去掉转码参数；真正的动画 WebP 退回首帧。

func TestImageRequestURLStripsForcedWebP(t *testing.T) {
	raw := "https://wsrv.nl/?url=https%3A%2F%2Fi.imgur.com%2FxscGYjP.gif&n=-1&output=webp"
	got, err := url.Parse(imageRequestURL(raw))
	if err != nil {
		t.Fatalf("改写后的地址解析失败：%v", err)
	}
	q := got.Query()
	if q.Has("output") {
		t.Errorf("请求地址仍带 output：%s", got)
	}
	if q.Get("n") != "-1" {
		t.Errorf("无关参数被丢了：%s", got)
	}
	if !strings.Contains(q.Get("url"), "xscGYjP.gif") {
		t.Errorf("目标地址被改坏了：%q", q.Get("url"))
	}

	// 非代理地址不动，避免误改别人的普通图片链接。
	const plain = "https://example.com/a.png?output=webp"
	if got := imageRequestURL(plain); got != plain {
		t.Errorf("非代理地址被改写：%s", got)
	}
}

func TestImageProxyLinkIsPicture(t *testing.T) {
	for _, raw := range []string{
		"https://wsrv.nl/?url=https%3A%2F%2Fi.imgur.com%2FxscGYjP.gif&output=webp",
		"https://wsrv.nl/?url=https%3A%2F%2Fexternal-content.duckduckgo.com%2Fiu%2F%3Fu%3Dhttps%253A%252F%252Fi.imgur.com%252FHfJqq3A.gif&output=webp&default=https%3A%2F%2Fi.imgur.com%2FHfJqq3A.gif",
		"https://images.weserv.nl/?url=i.imgur.com/HfJqq3A.webp",
	} {
		if !isImageLink(raw) {
			t.Errorf("代理图片地址没认成图片：%s", raw)
		}
	}
	for _, raw := range []string{
		"https://wsrv.nl/?url=https%3A%2F%2Fexample.com%2Fpost%2Fabc",
		"https://example.com/?url=https%3A%2F%2Fi.imgur.com%2Fx.gif",
	} {
		if isImageLink(raw) {
			t.Errorf("普通页面被认成图片：%s", raw)
		}
	}
	if got, want := mediaNameFromURL("https://wsrv.nl/?url=https%3A%2F%2Fi.imgur.com%2FHfJqq3A.gif&output=webp"), "HfJqq3A.gif"; got != want {
		t.Errorf("代理图片的名字 = %q，想要 %q", got, want)
	}
}

func TestWebPFirstFrameContainer(t *testing.T) {
	// 构造一个只有一帧的动画 WebP 容器：ANMF 帧头 + 空负载的 VP8 块。
	anmf := make([]byte, 16)
	putLE24(anmf[6:], 3) // 宽 4
	putLE24(anmf[9:], 1) // 高 2
	anmf = append(anmf, []byte("VP8 ")...)
	anmf = append(anmf, 0, 0, 0, 0)
	chunk := make([]byte, 8)
	copy(chunk, "ANMF")
	binary.LittleEndian.PutUint32(chunk[4:], uint32(len(anmf)))
	chunk = append(chunk, anmf...)
	body := append([]byte("WEBP"), chunk...)
	data := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(data[4:], uint32(len(body)))
	data = append(data, body...)

	got := webpFirstFrame(data)
	if got == nil {
		t.Fatalf("动画 WebP 没取到首帧")
	}
	chunks := riffChunks(got[12:])
	if len(chunks) != 1 || chunks[0].id != "VP8 " {
		t.Errorf("首帧容器不对：%+v", chunks)
	}
	if webpFirstFrame([]byte("not a webp")) != nil {
		t.Errorf("非 WebP 输入被当成了动画")
	}
}

// hostRewrite 把发往任意 host 的请求转到本地测试服务器：用来验证上面那个
// wsrv.nl 地址的请求确实在发出前被去掉 output=webp。
type hostRewrite struct{ target *url.URL }

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	u.Scheme, u.Host = h.target.Scheme, h.target.Host
	req := r.Clone(r.Context())
	req.URL = &u
	req.Host = ""
	return http.DefaultTransport.RoundTrip(req)
}

// 代理返回动画 WebP 是因为请求里带了 output=webp。应用去掉这个参数后，服务端
// 按原格式回 gif，正文里的 gif 就能正常显示。
func TestGifLoadsThroughImageProxy(t *testing.T) {
	var (
		hits  atomic.Int32
		asked atomic.Bool
	)
	frames := animatedGIFSize(t, 240, 160, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Has("output") {
			asked.Store(true)
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"))
			return
		}
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(frames)
	}))
	t.Cleanup(srv.Close)

	pic := "https://wsrv.nl/?url=https%3A%2F%2Fi.imgur.com%2FxscGYjP.gif&n=-1&output=webp"
	a := mediaDetailApp(t, "看图：\n\n![动图]("+pic+")")
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.client.HTTP = &http.Client{Transport: hostRewrite{target: target}}
	a.autoLoad = true

	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "代理后的 gif")
	if asked.Load() {
		t.Errorf("图片请求仍带着 output=webp")
	}
	if hits.Load() == 0 {
		t.Errorf("没有发出图片请求")
	}
	if tt.HasText(imageFailedText) {
		t.Errorf("gif 显示成了加载失败：%q", tt.Texts())
	}
}

// 正文里独占一段的代理图片地址也按图片嵌入，不必写成 Markdown 图片语法。
func TestBareImageProxyLinkRendersPicture(t *testing.T) {
	const pic = "https://wsrv.nl/?url=https%3A%2F%2Fi.imgur.com%2FHfJqq3A.gif&output=webp"
	a := mediaDetailApp(t, pic)
	a.images.put(pic, solidBitmap(400, 300, marker))
	tt := ui.NewTester(a.view, 1100, 760)
	if n := countMarker(tt.Image()); n == 0 {
		t.Fatalf("代理图片地址没有画成图片：%q", tt.Texts())
	}
	if err := tt.Click("HfJqq3A.gif"); err != nil {
		t.Fatalf("点代理图片失败：%v（%q）", err, tt.Texts())
	}
	if !a.viewer.open {
		t.Fatalf("点代理图片没有打开预览")
	}
}

// animatedGIFSize 造一张 w×h、两帧同色的动图，用来走完整的下载与解码链路。
func animatedGIFSize(t *testing.T, w, h int, ink color.RGBA) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{}, ink}
	g := &gif.GIF{}
	for i := 0; i < 2; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for k := range frame.Pix {
			frame.Pix[k] = 1
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("编码测试动图失败：%v", err)
	}
	return buf.Bytes()
}
