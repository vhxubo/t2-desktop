package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 进入详情页时，正文的排版要尽量一次就位：配图、表情与底部声明在数据陆续
// 到达的过程中不能把已经排好的文字顶来顶去。这些测试逐项锁定这种「刚进入
// 就抖动」的行为。

// shiftY 断言两个元素在纵向上的位置没有变化。
func shiftY(t *testing.T, what string, before, after float32) {
	t.Helper()
	if d := after - before; d > 1 || d < -1 {
		t.Errorf("%s前后文字位置变了：y %.1f -> %.1f (Δ%.1f)", what, before, after, d)
	}
}

// 配图已经在图片磁盘缓存里时，第一帧就按它的真实宽高占位；图片解码完成后
// 原位替换，下面的文字不移动。
func TestDetailImagePlaceholderKeepsLayout(t *testing.T) {
	const picURL = "https://example.com/pic.png"
	a := testApp(t)
	a.openDetail(Item{ShortID: "Image001", URL: "https://2libra.com/post/work/Image001"})
	// 先让磁盘缓存里有这张 800×400 的图（就像上次打开时下载过）。
	if err := a.images.writeDisk(picURL, imageCacheTemporary, a.images.generation(),
		solidPNG(t, 800, 400, marker)); err != nil {
		t.Fatalf("写入图片缓存失败：%v", err)
	}
	a.detail.topic = &topicDetail{
		ShortID: "Image001", Title: "图片排版", Author: "jimmy",
		Content: "看图：\n\n![配图](" + picURL + ")\n\n结尾段落标记XYZ。",
		URL:     "https://2libra.com/post/work/Image001",
	}
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	before, ok := tt.Find("结尾段落标记XYZ。")
	if !ok {
		t.Fatalf("找不到配图后面的段落：%q", tt.Texts())
	}
	// 占位框要已经有配图的大小（800×400 缩到正文栏宽后约 200 高）。
	ph, ok := tt.Find("配图")
	if !ok {
		t.Fatalf("找不到配图占位：%q", tt.Texts())
	}
	if ph.H < 100 {
		t.Errorf("配图占位没有按真实高度留白：%+v", ph)
	}

	// 图片解码完成，正文重排。
	a.images.put(picURL, solidBitmap(800, 400, marker))
	tt.Frame()
	after, ok := tt.Find("结尾段落标记XYZ。")
	if !ok {
		t.Fatalf("配图到达后找不到段落：%q", tt.Texts())
	}
	shiftY(t, "配图加载", before.Y, after.Y)
	if _, ok := tt.Find("配图"); !ok {
		t.Errorf("图片到达后没有画出来：%q", tt.Texts())
	}
}

// 表情图片还没到也要按 1em 的方形对象占住位置：这样图片到达（或表情表到达
// 后短码变成图片）时，行内文字不会重新折行。
func TestArticleEmojiReservesInlineSlotWhileLoading(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "Emoji001", URL: "https://2libra.com/post/work/Emoji001"})
	a.detail.topic = &topicDetail{
		ShortID: "Emoji001", Title: "表情排版", Author: "jimmy",
		Content: "这一句里有一个 :sigh: 表情，后面还跟着一些文字。\n\n结尾段落标记XYZ。",
		URL:     "https://2libra.com/post/work/Emoji001",
	}
	a.detail.replyLoaded = true
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))

	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("表情还没下载时没有占位对象：%q", tt.Texts())
	}
	if tt.HasText("[叹气]") {
		t.Errorf("表情仍以文字占位，图片到达后会重排：%q", tt.Texts())
	}
	before, ok := tt.Find("结尾段落标记XYZ。")
	if !ok {
		t.Fatalf("找不到表情后面的段落：%q", tt.Texts())
	}

	a.images.put("https://r2.2libra.com/emojis/sigh.png", solidBitmap(64, 64, marker))
	tt.Frame()
	after, ok := tt.Find("结尾段落标记XYZ。")
	if !ok {
		t.Fatalf("表情到达后找不到段落：%q", tt.Texts())
	}
	shiftY(t, "表情加载", before.Y, after.Y)
}

// 从缓存打开详情时不再先挂一行「正在刷新」的说明：缓存正文与网络结果几乎
// 同时到，先加一行再拿掉会让正文整块跳一下。刷新失败时仍然给离线说明。
func TestPreloadDetailCacheShowsNoRefreshingNote(t *testing.T) {
	a := testApp(t)
	topic := topicDetail{
		ShortID: "Cache001", Title: "缓存帖", Author: "jimmy",
		Content: "缓存里的正文。", URL: "https://2libra.com/post/work/Cache001",
	}
	a.detail = &detailState{shortID: "Cache001", topic: &topic, replyPage: 1}
	a.saveCachedDetail(a.detail)

	d := &detailState{shortID: "Cache001", replyPage: 1}
	a.preloadDetailCache(d)
	if d.topic == nil {
		t.Fatal("缓存正文没有预加载")
	}
	if d.cacheNote != "" {
		t.Errorf("从缓存打开时顶部应没有提示，却有 %q", d.cacheNote)
	}

	// 刷新失败仍然要说明当前是本地内容。
	a.markDetailOffline(d)
	if !strings.Contains(d.cacheNote, "本地缓存") {
		t.Errorf("离线时没有说明正文来自缓存：%q", d.cacheNote)
	}
	if d.cachedAt.IsZero() {
		t.Error("缓存时间没有记下来")
	}
}

// 详情底部的声明在正文加载中与加载后占同样的位置：两栏卡片不会因为声明
// 突然出现而被压矮一截。
func TestDetailSourceFooterStableWhileLoading(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "Footer01", URL: "https://2libra.com/post/work/Footer01"})
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	before, ok := tt.Find("内容来自 2Libra 用户，版权归原作者所有。")
	if !ok {
		t.Fatalf("进入详情时底部声明就该在：%q", tt.Texts())
	}
	linkBefore, ok := tt.Find("原文链接：")
	if !ok {
		t.Fatalf("正文还没到时也应能打开原文：%q", tt.Texts())
	}

	a.detail.topic = &topicDetail{
		ShortID: "Footer01", Title: "底部声明", Author: "jimmy",
		Content: "正文。", URL: "https://2libra.com/post/work/Footer01",
	}
	tt.Frame()
	after, ok := tt.Find("内容来自 2Libra 用户，版权归原作者所有。")
	if !ok {
		t.Fatalf("正文到达后底部声明不见了：%q", tt.Texts())
	}
	linkAfter, _ := tt.Find("原文链接：")
	shiftY(t, "正文加载", before.Y, after.Y)
	shiftY(t, "原文链接", linkBefore.Y, linkAfter.Y)

	if err := tt.Click("https://2libra.com/post/work/Footer01"); err != nil {
		t.Fatalf("点击原文链接失败：%v", err)
	}
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://2libra.com/post/work/Footer01" {
		t.Errorf("原文链接没有交给系统浏览器：%q", got)
	}
}
