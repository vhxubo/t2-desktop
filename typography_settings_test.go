package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 阅读与评论页顶部有四项排版选择与恢复入口，默认全部落在标准档。
func TestReadingTypographySectionDefaultsAndLabels(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabReading
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()
	for _, label := range []string{
		"阅读排版", "正文字号", "行距", "页宽", "列表密度", "恢复默认排版",
	} {
		if !st.HasText(label) {
			t.Fatalf("阅读与评论页缺少 %q：%q", label, st.Texts())
		}
	}
	for _, label := range []string{"小", "标准", "大", "特大", "紧凑", "宽松", "窄", "宽"} {
		if !st.HasText(label) {
			t.Fatalf("排版档位缺少 %q：%q", label, st.Texts())
		}
	}
	if got := a.typePrefs.snapshot(); got != defaultTypographyConfig() {
		t.Errorf("默认排版被改动：%+v", got)
	}
}

// 数字输入框能填非档位值：提交后落盘、高亮回到无档位，详情页正文按新字号画。
func TestTypographyNumericInputAppliesExactValue(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabReading
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()
	for _, label := range []string{
		"正文字号数值", "行距数值", "页宽数值", "行留白数值", "行内间距数值",
	} {
		if _, ok := st.Access(label); !ok {
			t.Fatalf("缺少精确数值输入框 %q：%q", label, st.Texts())
		}
	}
	base := detailBodyItemHeight(t, 16)

	if err := st.Click("正文字号数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("17")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.bodySize(); got != 17 {
		t.Fatalf("输入 17 后正文字号 = %d，想要 17", got)
	}
	if got := typographyOptionIndex(typographyBodySizes, 17); got != -1 {
		t.Errorf("精确数值 17 不该命中档位：%d", got)
	}
	if info, ok := st.Access("标准"); ok && info.Checked {
		t.Error("精确数值下「标准」仍高亮")
	}
	if got := detailBodyItemHeight(t, 17); got <= base {
		t.Errorf("字号 17 的正文高度 %g 没有超过 16 档 %g", got, base)
	}

	if err := st.Click("行距数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("1.6")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.lineHeight(); got != 16 {
		t.Errorf("输入 1.6 后行距 = %d，想要 16", got)
	}
	if err := st.Click("页宽数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("777")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.pageWidth(); got != 777 {
		t.Errorf("输入 777 后页宽 = %d，想要 777", got)
	}
	if err := st.Click("行留白数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("8")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.rowPadY(); got != 8 {
		t.Errorf("输入 8 后行留白 = %d，想要 8", got)
	}
	if err := st.Click("行内间距数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("3")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.rowGap(); got != 3 {
		t.Errorf("输入 3 后行内间距 = %d，想要 3", got)
	}
	var cfg typographyConfig
	if err := readJSONFile(filepath.Join(a.draftDir, typographyFileName), &cfg); err != nil {
		t.Fatal(err)
	}
	if want := (typographyConfig{17, 16, 777, 8, 3}); cfg != want {
		t.Errorf("精确数值没有落盘：%+v，想要 %+v", cfg, want)
	}
}

// 数字超出合法区间时输入框自己夹到边界，不会把越界值写进配置。
func TestTypographyNumericInputClamps(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabReading
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()
	if err := st.Click("正文字号数值"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Ctrl, ui.KeyA)
	st.Type("99")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if got := a.typePrefs.bodySize(); got != typographyBodyMax {
		t.Errorf("越界字号没有被夹到 %d：%d", typographyBodyMax, got)
	}
}

// 点「大」立刻落盘、重建 Markdown 选项，详情页正文随之变大；恢复默认后
// 回到原来的高度。
func TestTypographyBodySizeChangesDetailTextAndResets(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabReading
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()

	base := detailBodyItemHeight(t, 16)
	if err := st.Click("特大"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if got := a.typePrefs.bodySize(); got != 20 {
		t.Fatalf("选「特大」后正文字号 = %d，想要 20", got)
	}
	if !st.HasText("已应用阅读排版") {
		t.Errorf("切换排版没有提示：%q", st.Texts())
	}
	var cfg typographyConfig
	if err := readJSONFile(filepath.Join(a.draftDir, typographyFileName), &cfg); err != nil {
		t.Fatalf("切换后没有写入 typography.json：%v", err)
	}
	if cfg.BodySize != 20 {
		t.Errorf("落盘字号 = %d，想要 20", cfg.BodySize)
	}
	large := detailBodyItemHeight(t, 20)
	if large <= base {
		t.Errorf("大字号正文高度 %g 没有超过标准档 %g", large, base)
	}

	if err := st.Click("恢复默认排版"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("确定恢复默认阅读排版？") {
		t.Fatalf("没有弹出二次确认：%q", st.Texts())
	}
	if err := st.Click("确认恢复"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if got := a.typePrefs.snapshot(); got != defaultTypographyConfig() {
		t.Errorf("恢复默认后排版 = %+v", got)
	}
	if got := detailBodyItemHeight(t, 16); got != base {
		t.Errorf("恢复默认后正文高度 %g，期望 %g", got, base)
	}
}

// detailBodyItemHeight 用给定字号档渲染详情页，返回一条 Markdown 正文的高度。
func detailBodyItemHeight(t *testing.T, bodySize int) float32 {
	t.Helper()
	a := detailAppWithReplies(t)
	if err := a.typePrefs.setBodySize(bodySize); err != nil {
		t.Fatal(err)
	}
	a.rebuildMarkdownViews()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	r, ok := tt.Find("列表项一")
	if !ok {
		t.Fatalf("找不到正文列表项：%q", tt.Texts())
	}
	return r.H
}

// 同一窗口宽度下，页宽越窄列表左缘越靠右。
func TestTypographyPageWidthChangesFeedInset(t *testing.T) {
	widths := []int{640, 760, 880}
	xs := make([]float32, len(widths))
	for i, width := range widths {
		a := testApp(t)
		if err := a.typePrefs.setPageWidth(width); err != nil {
			t.Fatal(err)
		}
		a.tabs[0].items = []Item{{Key: "a", Title: "列宽参照行"}}
		a.tabs[0].loaded = true
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		r, ok := tt.Find("列宽参照行")
		if !ok {
			t.Fatalf("页宽 %d 的列表没渲染：%q", width, tt.Texts())
		}
		xs[i] = r.X
	}
	if !(xs[0] > xs[1] && xs[1] > xs[2]) {
		t.Errorf("窄 / 标准 / 宽的列表左缘 = %v，应依次向左", xs)
	}
}

// 列表密度只改行高与行内间距，不改字号。
func TestTypographyDensityChangesRowHeight(t *testing.T) {
	heights := make([]float32, 3)
	for _, density := range []int{
		typographyDensityCompact, typographyDensityStandard, typographyDensityLoose,
	} {
		a := testApp(t)
		if err := a.typePrefs.setDensity(density); err != nil {
			t.Fatal(err)
		}
		a.tabs[0].items = []Item{
			{Key: "a", Title: "密度参照行一"},
			{Key: "b", Title: "密度参照行二"},
		}
		a.tabs[0].loaded = true
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		first, ok := tt.Find("密度参照行一")
		if !ok {
			t.Fatalf("密度 %d 的列表没渲染：%q", density, tt.Texts())
		}
		second, ok := tt.Find("密度参照行二")
		if !ok {
			t.Fatalf("第二行没渲染：%q", tt.Texts())
		}
		heights[density] = second.Y - first.Y
	}
	if !(heights[typographyDensityCompact] < heights[typographyDensityStandard] &&
		heights[typographyDensityStandard] < heights[typographyDensityLoose]) {
		t.Errorf("紧凑 / 标准 / 宽松的行高 = %v，应依次变大", heights)
	}
}

// 切到别的分类再回来，四项仍显示已保存的档位。
func TestTypographyChoiceSurvivesTabSwitch(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabReading
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()
	if err := st.Click("特大"); err != nil {
		t.Fatal(err)
	}
	st.Frame()

	a.settingsTab = settingsTabGeneral
	st.Frame()
	a.settingsTab = settingsTabReading
	st.Frame()
	if got := a.typePrefs.bodySize(); got != 20 {
		t.Errorf("切页后字号 = %d，想要 20", got)
	}
	if info, ok := st.Access("特大"); !ok || !info.Checked {
		t.Errorf("切页后「特大」没有保持选中：%+v / %v", info, ok)
	}
}

// 偏好是进程级共享的：改一次，另一个账号窗口的 Markdown 选项也跟着重建。
func TestTypographyChangeRebuildsEveryAppWindow(t *testing.T) {
	b := testBook(t)
	acc1 := addTestAccount(t, b, "type-a")
	acc2 := addTestAccount(t, b, "type-b")
	first := newAppWithDeps(b.deps(), acc1)
	second := newAppWithDeps(b.deps(), acc2)
	acc1.app, acc2.app = first, second
	if first.typePrefs != second.typePrefs {
		t.Fatal("两个窗口没有共用同一份排版偏好")
	}
	if err := first.typePrefs.setBodySize(20); err != nil {
		t.Fatal(err)
	}
	first.applyTypographyChange()
	if got := first.md.Theme.FontSize; got != 20 {
		t.Errorf("发起修改的窗口字号 = %g，想要 20", got)
	}
	if got := second.md.Theme.FontSize; got != 20 {
		t.Errorf("另一个账号窗口字号 = %g，想要 20", got)
	}
	if got := second.commentMd.Theme.FontSize; got != 19 {
		t.Errorf("另一个账号窗口评论字号 = %g，想要 19", got)
	}
}
