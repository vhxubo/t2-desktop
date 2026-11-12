package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// solidBitmap 造一张纯色位图，用于在无头渲染里断言图片确实画出来了。
func solidBitmap(w, h int, c color.RGBA) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return ui.NewBitmap(img)
}

func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码测试图片失败：%v", err)
	}
	return buf.Bytes()
}

// marker 是测试图片的纯绿色；白色主题（含关闭按钮的红、强调色蓝）里
// 不会出现这种颜色，便于精确量出图片实际画在哪。
var marker = color.RGBA{G: 220, A: 255}

// countMarker 返回测试图片包围盒的面积，零表示没画出来。
func countMarker(img *image.RGBA) int {
	box := markerBounds(img)
	return box.Dx() * box.Dy()
}

// markerBounds 返回测试图片像素的包围盒，用来断言它的显示尺寸。
func markerBounds(img *image.RGBA) image.Rectangle {
	var box image.Rectangle
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if g>>8 > 150 && r>>8 < 100 && b>>8 < 100 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

func emojiDetailApp(t *testing.T, content string) (*app, *ui.Bitmap) {
	t.Helper()
	a := testApp(t)
	a.openDetail(Item{ShortID: "EMOJI001", URL: "https://2libra.com/post/work/EMOJI001"})
	a.detail.topic = &topicDetail{
		ShortID: "EMOJI001", Title: "表情渲染", Author: "jimmy",
		Content: content, URL: "https://2libra.com/post/work/EMOJI001",
	}
	a.detail.replyLoaded = true
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	bmp := solidBitmap(64, 64, marker)
	a.articleImages = map[string]*articleImageEntry{
		"https://r2.2libra.com/emojis/sigh.png": {src: bmp},
	}
	return a, bmp
}

// 正文段落里的 :sigh: 要变成行内表情图片，而不是 Markdown 默认的
// 「[叹气]」文字占位。
func TestArticleEmojiRendersAsInlineImage(t *testing.T) {
	a, _ := emojiDetailApp(t, "第一段 :sigh: 正文。")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("表情没有作为行内图片对象渲染：%q", tt.Texts())
	}
	if tt.HasText("[叹气]") {
		t.Errorf("表情仍显示为文字占位：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("表情图片没有画到界面上")
	}
}

// 链接、强调把段落切成多个 RichText 后，表情对象仍要落在正确的字符
// 位置并画出来。
func TestArticleEmojiAfterLinkAndStrong(t *testing.T) {
	a, _ := emojiDetailApp(t, "前 [链接](https://example.com) **加粗 :sigh: 文字** 后")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("链接") || !tt.HasText("加粗") {
		t.Errorf("链接或强调文字丢失：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("链接 / 强调后的表情没有画出来")
	}
}

// 链接文字里的表情也要画成图片，且仍能点开链接。
func TestArticleEmojiInsideLink(t *testing.T) {
	a, _ := emojiDetailApp(t, "看 [链接 :sigh:](https://example.com) 结尾")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("链接里的表情没有成为行内图片：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("链接里的表情没有画出来")
	}
}

// 帖子 / 社区自己的 emojis 表里可能有全局表没有的图标；这些社区图标也
// 要按行内表情渲染，而不是当成普通配图或原样留在正文里。
func TestCommunityEmojiRendersInline(t *testing.T) {
	a, _ := emojiDetailApp(t, "社区图标 :community_icon: 结尾")
	a.detail.topic.Emojis = map[string]string{
		":community_icon:": "/emojis/community_icon.png?size=medium",
	}
	u := "https://r2.2libra.com/emojis/community_icon.png?size=medium"
	a.articleImages[u] = &articleImageEntry{src: solidBitmap(64, 64, marker)}

	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("社区图标没有成为行内图片：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 || n > 1200 {
		t.Errorf("社区图标的绿色像素 = %d，不像 1em 的行内图片", n)
	}
}

// 单独一行表情时按行内文字大小画（约 1em），不能被当作整段配图放大。
func TestEmojiOnlyParagraphStaysInlineSized(t *testing.T) {
	a, _ := emojiDetailApp(t, ":sigh:")
	tt := ui.NewTester(a.view, 1100, 760)
	if n := countMarker(tt.Image()); n == 0 || n > 1200 {
		t.Errorf("单行表情的绿色像素 = %d，不像 1em 的行内图片（64×64 原图约 4096）", n)
	}
}

// 评论正文也按 Markdown 渲染：强调、列表、行内代码、引用与链接都变成排版
// 结果，而不是把 ** / - / ` 原样留在评论区；链接仍走 Markdown 的点击处理。
func TestCommentMarkdownRenders(t *testing.T) {
	a, _ := emojiDetailApp(t, "正文")
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", Floor: 1,
		Content: "**重点**\n\n- 列表一\n- 列表二\n\n`代码` 与 [链接](https://example.com)\n\n> 引用一句",
	}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"重点", "列表一", "列表二", "代码", "链接", "引用一句"} {
		if !tt.HasText(want) {
			t.Errorf("评论 Markdown 没有渲染出 %q：%q", want, tt.Texts())
		}
	}
	for _, raw := range []string{"**重点**", "- 列表一", "`代码`", "[链接](https://example.com)"} {
		if tt.HasText(raw) {
			t.Errorf("评论里的 Markdown 标记被原样显示：%q", raw)
		}
	}
	if err := tt.Click("链接"); err != nil {
		t.Fatalf("点击评论里的链接失败：%v（%q）", err, tt.Texts())
	}
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com" {
		t.Errorf("评论里的链接没有交给系统浏览器：%q", got)
	}
	// 悬浮在 Markdown 渲染出来的正文上，整条评论仍算悬浮：右上角出现
	// 「回复」入口，说明评论行的悬浮判定没有被 Markdown 视图挡住。
	if r, ok := tt.Find("引用一句"); ok {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
		if _, ok := tt.Find("回复"); !ok {
			t.Errorf("悬浮评论正文时没有出现回复入口：%q", tt.Texts())
		}
	} else {
		t.Errorf("找不到引用段落用于悬浮：%q", tt.Texts())
	}
}

// waitForMarker 推动测试窗口重绘，直到正文图片画出来或超时。
func waitForMarker(t *testing.T, tt *ui.Tester, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tt.Frame()
		if countMarker(tt.Image()) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s在 3 秒内没有显示：%q", what, tt.Texts())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func pictureApp(t *testing.T, content string) *app {
	t.Helper()
	// 原图远宽于正文栏，验证会按栏宽等比缩小而不是撑破布局。
	pixels := solidPNG(t, 2000, 1000, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	t.Cleanup(srv.Close)

	content = strings.ReplaceAll(content, "PIC", srv.URL+"/pic.png")
	a := testApp(t)
	a.openDetail(Item{ShortID: "IMAGE001", URL: "https://2libra.com/post/work/IMAGE001"})
	a.detail.topic = &topicDetail{
		ShortID: "IMAGE001", Title: "图片渲染", Author: "jimmy",
		Content: content,
		URL:     "https://2libra.com/post/work/IMAGE001",
	}
	a.detail.replyLoaded = true
	a.client.HTTP = srv.Client()
	a.autoLoad = true // 只在这里打开，让正文图片加载走本地测试服务器
	return a
}

// 正文里的独立图片段落要真正下载并显示。
func TestArticleBlockImageRenders(t *testing.T) {
	a := pictureApp(t, "看图：\n\n![配图](PIC)\n\n结束")
	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "正文配图")
	if r := markerBounds(tt.Image()); r.Dx() > 560 {
		t.Errorf("配图没有缩到正文栏宽：宽 %d 像素（窗口宽 1100，左右两栏各约一半）", r.Dx())
	}
}

// 图片和文字写在同一段落（2Libra 的真实正文常见写法）时也要显示图片，
// 而不是只剩 [说明]。
func TestArticleMixedParagraphImageRenders(t *testing.T) {
	a := pictureApp(t, "前文\n![配图](PIC)\n后文")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("前文") || !tt.HasText("后文") {
		t.Errorf("配图前后的文字丢了：%q", tt.Texts())
	}
	waitForMarker(t, tt, "段内配图")
}
