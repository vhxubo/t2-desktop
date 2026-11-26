package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 表格单元格里只有一张图片时，要显示图片而不是 [说明]。
func TestArticleTableBlockImageRenders(t *testing.T) {
	a := pictureApp(t, "| 说明 | 预览 |\n| --- | --- |\n| 截图 | ![配图](PIC) |\n")
	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "表格里的配图")
	if !tt.HasText("截图") {
		t.Errorf("表格里的文字丢了：%q", tt.Texts())
	}
	if tt.HasText("[配图]") {
		t.Errorf("表格里的图片仍显示成说明文字：%q", tt.Texts())
	}
}

// 表格单元格里文字与图片写在同一格时也要显示图片。
func TestArticleTableMixedParagraphImageRenders(t *testing.T) {
	a := pictureApp(t, "| 说明 | 预览 |\n| --- | --- |\n| 截图 | 前文 ![配图](PIC) 后文 |\n")
	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "表格单元格里的配图")
	if !tt.HasText("前文") || !tt.HasText("后文") {
		t.Errorf("单元格里配图前后的文字丢了：%q", tt.Texts())
	}
}

// 行内 :sigh: 表情在表格单元格里也要画成行内图片：表格单元格不经过
// Markdown 的 Options.Block，含表情的表格同样要整张接管。
func TestArticleTableEmojiRendersInline(t *testing.T) {
	a, _ := emojiDetailApp(t, "| 列 | 说明 |\n| --- | --- |\n| 表情 | :sigh: |\n")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("表格里的表情没有成为行内图片：%q", tt.Texts())
	}
	if tt.HasText("[叹气]") {
		t.Errorf("表格里的表情仍显示为文字占位：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 || n > 1200 {
		t.Errorf("表格里表情的绿色像素 = %d，不像 1em 的行内图片", n)
	}
}

// 接管后仍是一张真表格：表格、表头与单元格的无障碍角色都在，配图落在表格
// 内部、表头下方的那一行里。
func TestArticleTableKeepsStructure(t *testing.T) {
	a := pictureApp(t, "| 名称 | 预览 |\n| --- | --- |\n| 截图 | ![配图](PIC) |\n")
	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "表格里的配图")
	table, ok := tt.AccessByRole(ui.RoleTable, "")
	if !ok {
		t.Fatalf("表格没有 RoleTable：%q", tt.Texts())
	}
	head, ok := tt.AccessByRole(ui.RoleColumnHeader, "名称")
	if !ok {
		t.Fatalf("表头单元格没有列标题角色：%q", tt.Texts())
	}
	// 「预览」是第二列的表头：配图应当落在这一列里，而不是第一列。
	preview, ok := tt.AccessByRole(ui.RoleColumnHeader, "预览")
	if !ok {
		t.Fatalf("第二列的表头没有列标题角色：%q", tt.Texts())
	}
	img := markerBounds(tt.Image())
	if img.Empty() {
		t.Fatal("配图没有画出来")
	}
	tb := table.Bounds
	if float32(img.Min.X) < tb.X || float32(img.Max.X) > tb.X+tb.W ||
		float32(img.Min.Y) < tb.Y || float32(img.Max.Y) > tb.Y+tb.H {
		t.Errorf("配图 %v 不在表格 %v 里面", img, tb)
	}
	if float32(img.Min.X) < preview.Bounds.X {
		t.Errorf("配图 %v 不在「预览」那一列里（该列表头 %v）", img, preview.Bounds)
	}
	if float32(img.Min.Y) < head.Bounds.Y+head.Bounds.H-1 {
		t.Errorf("配图 %v 跑到了表头上方（表头 %v）", img, head.Bounds)
	}
}

// 没有图片的表格仍交给 Markdown 自己排：文字、表头与列都在，并且不改版式。
func TestArticleTableWithoutImagesUsesDefault(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "TABLE001", URL: "https://2libra.com/post/work/TABLE001"})
	a.detail.topic = &topicDetail{
		ShortID: "TABLE001", Title: "表格渲染", Author: "jimmy",
		Content: "| 名称 | 数量 |\n| --- | ---: |\n| 苹果 | 3 |\n",
		URL:     "https://2libra.com/post/work/TABLE001",
	}
	a.detail.replyLoaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"名称", "数量", "苹果"} {
		if !tt.HasText(want) {
			t.Errorf("表格缺了 %q：%q", want, tt.Texts())
		}
	}
}
