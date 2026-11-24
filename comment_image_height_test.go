package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestImageBoxWidth(t *testing.T) {
	cases := []struct {
		name       string
		w, h, maxH float32
		want       float32
	}{
		{"横图按高度收窄", 800, 400, 240, 480},
		{"竖图按高度收窄", 400, 1200, 240, 80},
		{"不设上限", 800, 400, 0, 0},
		{"未知宽度", 0, 400, 240, 0},
		{"未知高度", 800, 0, 240, 0},
		{"负上限", 400, 1200, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageBoxWidth(tc.w, tc.h, tc.maxH); got != tc.want {
				t.Errorf("imageBoxWidth(%g, %g, %g) = %g，想要 %g", tc.w, tc.h, tc.maxH, got, tc.want)
			}
		})
	}
}

func TestCommentImageStyleLimit(t *testing.T) {
	if got := commentTextStyle.imageMaxH; got != commentImageMaxH {
		t.Errorf("评论图片上限 = %g，想要 %g", got, commentImageMaxH)
	}
	if got := postTextStyle.imageMaxH; got != 0 {
		t.Errorf("正文不应限制图片高度，实际 = %g", got)
	}
	if got := bioTextStyle.imageMaxH; got != 0 {
		t.Errorf("简介不应限制图片高度，实际 = %g", got)
	}
}

// commentImageApp 造一个带一条评论的详情，评论正文由调用方给。
func commentImageApp(t *testing.T, content string) *app {
	t.Helper()
	a := testApp(t)
	t.Cleanup(func() { waitForImageWorkers(t, a.images) })
	a.openDetail(Item{ShortID: "CIMG001", URL: "https://2libra.com/post/work/CIMG001"})
	a.detail.topic = &topicDetail{
		ShortID: "CIMG001", Title: "评论图片", Author: "jimmy",
		Content: "正文", URL: "https://2libra.com/post/work/CIMG001",
	}
	a.detail.replyList = []replyItem{{ID: "c1", Author: "alice", Floor: 1, Content: content}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true
	return a
}

// commentPicture 把 PIC 换成一张已经进内存缓存的测试图，界面第一帧就能画出来。
func commentPicture(t *testing.T, w, h int, content string) (*app, *ui.Tester) {
	t.Helper()
	const picURL = "https://example.com/comment-pic.png"
	a := commentImageApp(t, strings.ReplaceAll(content, "PIC", picURL))
	a.images.put(picURL, solidBitmap(w, h, marker))
	return a, ui.NewTester(a.view, 1100, 760)
}

// bodyPicture 与 commentPicture 对照：同一张图放在正文里不加高度上限。
func bodyPicture(t *testing.T, w, h int, content string) (*app, *ui.Tester) {
	t.Helper()
	const picURL = "https://example.com/body-pic.png"
	a := testApp(t)
	t.Cleanup(func() { waitForImageWorkers(t, a.images) })
	a.openDetail(Item{ShortID: "BIMG001", URL: "https://2libra.com/post/work/BIMG001"})
	a.detail.topic = &topicDetail{
		ShortID: "BIMG001", Title: "正文图片", Author: "jimmy",
		Content: strings.ReplaceAll(content, "PIC", picURL),
		URL:     "https://2libra.com/post/work/BIMG001",
	}
	a.detail.replyLoaded = true
	a.images.put(picURL, solidBitmap(w, h, marker))
	return a, ui.NewTester(a.view, 1400, 900)
}

// 竖图在评论里按高度收窄，比例保持，下面的文字还在。
func TestCommentPictureHeightLimited(t *testing.T) {
	_, tt := commentPicture(t, 400, 1200, "看图：\n\n![配图](PIC)\n\n结尾标记XYZ。")
	r, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("找不到评论配图：%q", tt.Texts())
	}
	if r.Bounds.H > commentImageMaxH {
		t.Errorf("评论配图高 %g，超过上限 %g：%+v", r.Bounds.H, commentImageMaxH, r.Bounds)
	}
	if r.Bounds.H < commentImageMaxH-2 {
		t.Errorf("评论配图高 %g，没有用满可用高度 %g：%+v", r.Bounds.H, commentImageMaxH, r.Bounds)
	}
	wantW := commentImageMaxH * 400 / 1200
	if r.Bounds.W < wantW-3 || r.Bounds.W > wantW+3 {
		t.Errorf("评论配图宽 %g，按原比例应约 %g：%+v", r.Bounds.W, wantW, r.Bounds)
	}
	if !tt.HasText("结尾标记XYZ。") {
		t.Errorf("配图后面的评论文字丢了：%q", tt.Texts())
	}
}

// 文字与图片写在同一段时也走同一个限高逻辑。
func TestCommentMixedPictureHeightLimited(t *testing.T) {
	_, tt := commentPicture(t, 400, 1200, "前文\n![配图](PIC)\n后文")
	r, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("找不到段内配图：%q", tt.Texts())
	}
	if r.Bounds.H > commentImageMaxH {
		t.Errorf("段内配图高 %g，超过上限 %g：%+v", r.Bounds.H, commentImageMaxH, r.Bounds)
	}
	if !tt.HasText("前文") || !tt.HasText("后文") {
		t.Errorf("配图前后的文字丢了：%q", tt.Texts())
	}
}

// 正文的同一张图不受评论上限影响。
func TestBodyPictureKeepsFullHeight(t *testing.T) {
	_, tt := bodyPicture(t, 400, 1200, "看图：\n\n![配图](PIC)\n\n结束")
	r, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("找不到正文配图：%q", tt.Texts())
	}
	if r.Bounds.H <= commentImageMaxH {
		t.Errorf("正文配图被评论上限误伤，高 %g：%+v", r.Bounds.H, r.Bounds)
	}
}

// 已知尺寸的加载中占位与成图边界一致，图到达后下方文字不跳。
func TestCommentPicturePlaceholderMatchesLoaded(t *testing.T) {
	const picURL = "https://example.com/pending-comment.png"
	a := commentImageApp(t, "看图：\n\n![配图]("+picURL+")\n\n结尾标记XYZ。")
	if err := a.images.writeDisk(picURL, imageCacheTemporary, a.images.generation(), solidPNG(t, 400, 1200, marker)); err != nil {
		t.Fatalf("写入图片缓存失败：%v", err)
	}
	a.images.begin(picURL)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	ph, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("找不到加载中占位：%q", tt.Texts())
	}
	if ph.Bounds.H > commentImageMaxH {
		t.Errorf("加载中占位高 %g，超过上限 %g：%+v", ph.Bounds.H, commentImageMaxH, ph.Bounds)
	}
	before, ok := tt.Find("结尾标记XYZ。")
	if !ok {
		t.Fatalf("找不到配图后面的段落：%q", tt.Texts())
	}

	a.images.put(picURL, solidBitmap(400, 1200, marker))
	tt.Frame()
	after, ok := tt.Find("结尾标记XYZ。")
	if !ok {
		t.Fatalf("配图到达后找不到段落：%q", tt.Texts())
	}
	shiftY(t, "评论配图加载", before.Y, after.Y)
	loaded, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("配图到达后找不到图片：%q", tt.Texts())
	}
	if loaded.Bounds.H != ph.Bounds.H || loaded.Bounds.W != ph.Bounds.W {
		t.Errorf("占位 %+v 与成图 %+v 边界不一致", ph.Bounds, loaded.Bounds)
	}
}

// 评论里的表格单元格配图也按上限收窄，列宽不按原图预留。
func TestCommentTablePictureWidthLimited(t *testing.T) {
	_, tt := commentPicture(t, 400, 1200, "| 名称 | 预览 |\n| --- | --- |\n| 截图 | ![配图](PIC) |\n")
	r, ok := tt.Access("配图")
	if !ok {
		t.Fatalf("找不到表格里的配图：%q", tt.Texts())
	}
	if r.Bounds.H > commentImageMaxH {
		t.Errorf("表格配图高 %g，超过上限 %g：%+v", r.Bounds.H, commentImageMaxH, r.Bounds)
	}
	head, ok := tt.AccessByRole(ui.RoleColumnHeader, "预览")
	if !ok {
		t.Fatalf("找不到预览表头：%q", tt.Texts())
	}
	if head.Bounds.W > 200 {
		t.Errorf("预览列宽 %g，表格列宽按原图 400 预留了：%+v", head.Bounds.W, head.Bounds)
	}
}

// 评论里的视频卡片降到上限，正文仍按自己的 380。
func TestVideoCardHeightByContext(t *testing.T) {
	const watch = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	label := "打开视频：YouTube · dQw4w9WgXcQ"

	comment := ui.NewTester(commentImageApp(t, watch).view, 1400, 900)
	r, ok := comment.Access(label)
	if !ok {
		t.Fatalf("找不到评论里的视频卡片：%q", comment.Texts())
	}
	if r.Bounds.H > commentImageMaxH {
		t.Errorf("评论视频卡片高 %g，超过上限 %g：%+v", r.Bounds.H, commentImageMaxH, r.Bounds)
	}

	a := testApp(t)
	t.Cleanup(func() { waitForImageWorkers(t, a.images) })
	a.openDetail(Item{ShortID: "BVID001", URL: "https://2libra.com/post/work/BVID001"})
	a.detail.topic = &topicDetail{
		ShortID: "BVID001", Title: "正文视频", Author: "jimmy",
		Content: watch, URL: "https://2libra.com/post/work/BVID001",
	}
	a.detail.replyLoaded = true
	body := ui.NewTester(a.view, 1400, 900)
	br, ok := body.Access(label)
	if !ok {
		t.Fatalf("找不到正文里的视频卡片：%q", body.Texts())
	}
	if br.Bounds.H <= commentImageMaxH {
		t.Errorf("正文视频卡片被评论上限误伤，高 %g：%+v", br.Bounds.H, br.Bounds)
	}
	if br.Bounds.H > imageVideoCardMaxH {
		t.Errorf("正文视频卡片高 %g，超过 %g：%+v", br.Bounds.H, imageVideoCardMaxH, br.Bounds)
	}
}
