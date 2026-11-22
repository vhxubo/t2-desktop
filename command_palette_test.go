package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 点输入框就展开面板，默认是命令模式，列出全部命令。搜索是次要的切换项。
func TestCommandPaletteOpensOnClick(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if !a.palette.open {
		t.Fatalf("点输入框后命令面板没有展开")
	}
	if a.palette.mode != paletteModeCommands {
		t.Errorf("默认应该是命令模式，实际 mode=%d", a.palette.mode)
	}
	for _, label := range []string{"命令 发帖", "命令 设置", "命令 金币", "命令 搜索"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("命令面板里找不到「%s」：%q", label, tt.Texts())
		}
	}
	// 未登录窗口给「登录」，不给「退出登录」。
	if _, ok := tt.Find("命令 登录"); !ok {
		t.Error("未登录时命令面板缺少「登录」")
	}
	if _, ok := tt.Find("命令 退出登录"); ok {
		t.Error("未登录时命令面板不该有「退出登录」")
	}
	// 面板挂在标题栏搜索框下方，不压住标题栏本身。
	if r, ok := tt.Find("命令 发帖"); ok && r.Y < titleBarHeight {
		t.Errorf("命令面板跑到标题栏上面了：%+v", r)
	}
}

// 有内容时输入框右侧出现一键清空按钮；点它清空输入，回到默认的命令模式并
// 继续展示全部命令。
func TestCommandPaletteClearButton(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("设置")
	tt.Frame()

	if _, ok := tt.Find("Clear"); !ok {
		t.Fatalf("有内容时应该出现一键清空按钮：%q", tt.Texts())
	}
	if err := tt.Click("Clear"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if a.palette.text != "" {
		t.Errorf("点清空后输入没清干净：%q", a.palette.text)
	}
	if !a.palette.open || a.palette.mode != paletteModeCommands || a.palette.selected != 0 {
		t.Errorf("清空后没回到默认命令面板：open=%v mode=%d sel=%d",
			a.palette.open, a.palette.mode, a.palette.selected)
	}
	if _, ok := tt.Find("命令 发帖"); !ok {
		t.Error("清空后应该重新列出全部命令")
	}
}

// 输入关键词过滤命令，大小写与拼音都命中。
func TestCommandPaletteFilters(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("shezhi")
	tt.Frame()

	if _, ok := tt.Find("命令 设置"); !ok {
		t.Errorf("拼音 shezhi 没有命中「设置」：%q", tt.Texts())
	}
	if _, ok := tt.Find("命令 发帖"); ok {
		t.Error("过滤后不该还显示「发帖」")
	}
}

// 回车执行高亮的命令：调用已有入口，退出命令模式并清空输入。
func TestCommandPaletteRunsSelectedCommand(t *testing.T) {
	a := testApp(t)
	opened := false
	a.onSettingsOpen = func() { opened = true }
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("设置")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()

	if !opened {
		t.Error("回车没有执行选中的「设置」命令")
	}
	if a.palette.text != "" || a.palette.selected != 0 {
		t.Errorf("执行命令后输入/高亮没复位：text=%q sel=%d",
			a.palette.text, a.palette.selected)
	}
}

// ↑ / ↓ 移动高亮，首尾循环；回车执行移动后的那一项。
func TestCommandPaletteKeyboardNavigation(t *testing.T) {
	a := testApp(t)
	opened := false
	a.onSettingsOpen = func() { opened = true }
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// 面板按分组重排过， ↑↓ 的下标对应显示行而不是注册顺序。
	rows := a.paletteRows("")
	if len(rows) < 3 {
		t.Fatalf("命令太少，测不了导航：%d", len(rows))
	}
	// ↓ 把高亮移到「设置」。
	idx := -1
	for i, row := range rows {
		if row.cmd.id == "settings" {
			idx = i
			break
		}
	}
	for try := 0; a.palette.selected != idx && try < len(rows)+2; try++ {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
	}
	if a.palette.selected != idx {
		t.Fatalf("↓ 没有移到「设置」：selected=%d want=%d", a.palette.selected, idx)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !opened {
		t.Error("回车没有执行高亮的「设置」")
	}

	// 再验证首尾循环：从最后一项按 ↓ 回到第一项。执行命令后光标已随面板一起
	// 隐藏，要先重新点输入框拿回焦点，↓ 才会送进输入框。
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.palette.text = "金币"
	filtered := a.paletteRows("金币")
	if len(filtered) == 0 {
		t.Fatal("「金币」没有匹配到命令")
	}
	a.palette.selected = len(filtered) - 1
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.palette.selected != 0 {
		t.Errorf("末项按 ↓ 没有回到第一项：selected=%d", a.palette.selected)
	}
}

// 搜索是次要入口：切到「搜索」模式后，关键词走原来的搜索窗口，不执行命令。
func TestCommandPaletteSearchMode(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.palette.mode != paletteModeSearch {
		t.Fatalf("没有切到搜索模式：mode=%d", a.palette.mode)
	}
	if !tt.HasText("输入关键词后按回车搜索") {
		t.Errorf("搜索模式没有给出提示：%q", tt.Texts())
	}

	tt.Type("职场")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.search == nil || a.search.q != "职场" {
		q := "<nil>"
		if a.search != nil {
			q = a.search.q
		}
		t.Fatalf("搜索模式回车没有把关键词带进搜索状态：%q", q)
	}
	if a.palette.text != "" {
		t.Errorf("搜索模式回车后标题栏输入没清空：%q", a.palette.text)
	}
}

// 输入框失焦（Tab 到别的控件）时面板自动收起。
func TestCommandPaletteClosesOnBlur(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.palette.open {
		t.Fatal("点输入框后面板没有展开")
	}

	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if a.palette.open {
		t.Error("输入框失焦后面板没有自动收起")
	}
}

// 点到面板外（列表区域）也要收起；很多控件点了不会挪走键盘焦点，光看 FocusLeft 不够。
func TestCommandPaletteClosesOnOutsidePress(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.palette.open {
		t.Fatal("点输入框后面板没有展开")
	}

	// 面板挂在搜索框下方（x 约 418–778），点右侧内容区才算面板外。
	tt.Press(950, 500)
	tt.Release(950, 500)
	tt.Frame()
	if a.palette.open {
		t.Error("点面板外没有收起面板")
	}
}

// 关掉后重新点输入框还能再展开：suppressOpen 只挡住「焦点回到锚点」那一次。
func TestCommandPaletteReopensAfterOutsideClose(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Press(950, 500)
	tt.Release(950, 500)
	tt.Frame()
	if a.palette.open {
		t.Fatal("点面板外没有收起")
	}

	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.palette.open {
		t.Error("重新点输入框没有展开面板")
	}
}

// 执行命令后面板保持收起：即使命令没开新窗口、焦点还在输入框里，也不该重新弹出。
func TestCommandPaletteStaysClosedAfterCommand(t *testing.T) {
	for _, by := range []string{"enter", "click"} {
		t.Run(by, func(t *testing.T) {
			a := testApp(t)
			tt := ui.NewTester(a.view, 1080, 720)
			tt.Frame()
			if err := tt.Click("搜索关键词"); err != nil {
				t.Fatal(err)
			}
			tt.Type("首页")
			tt.Frame()
			if by == "enter" {
				tt.Key(0, ui.KeyEnter)
			} else if err := tt.Click("命令 首页"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()

			if a.palette.open {
				t.Error("执行命令后面板没有保持收起")
			}
			if _, ok := tt.Find("命令 发帖"); ok {
				t.Error("面板收起后不该还画着命令列表")
			}
			// 再点一次输入框要能重新打开，suppressOpen 不能一直挡着。
			if err := tt.Click("搜索关键词"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !a.palette.open {
				t.Error("执行命令后再点输入框没能重新展开")
			}
		})
	}
}

// 没有匹配的命令时回车直接进搜索：这个词就是搜索关键词，不是空等。
func TestCommandPaletteNoMatchFallsBackToSearch(t *testing.T) {
	a := testApp(t)
	const query = "zzz没有这条命令"
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type(query)
	tt.Frame()

	if _, ok := tt.Find("命令 发帖"); ok {
		t.Fatal("不该有命令命中这个过滤词")
	}
	if !tt.HasText("按回车搜索") {
		t.Errorf("空状态没有提示回车可搜索：%q", tt.Texts())
	}

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.search == nil || a.search.q != query {
		q := "<nil>"
		if a.search != nil {
			q = a.search.q
		}
		t.Fatalf("无匹配回车没有进搜索：q=%q want=%q", q, query)
	}
	if a.palette.text != "" {
		t.Errorf("无匹配回车后标题栏输入没清空：%q", a.palette.text)
	}
}

// Esc 清空输入，模式回到默认的命令模式；面板继续留着。
func TestCommandPaletteEscapeClearsInput(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("金币")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	if a.palette.text != "" || a.palette.mode != paletteModeCommands {
		t.Errorf("Esc 没清空输入/回到命令模式：text=%q mode=%d",
			a.palette.text, a.palette.mode)
	}
}

// 输入框前导图标是闪电；空输入时提示随聚焦状态切换：未聚焦是居中的
// 「命令面板」，聚焦后换成左对齐的「搜索命令、帖子和用户...」。失焦时占位
// 文案立刻让位给居中 hint，不残留。
func TestCommandPaletteFocusHint(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	if _, ok := tt.Find("闪电"); !ok {
		t.Errorf("输入框前导图标不是闪电：%q", tt.Texts())
	}
	if !tt.HasText(titleSearchHint) {
		t.Fatal("未聚焦时该显示居中的「命令面板」")
	}
	if tt.HasText(titleSearchPlaceholder) {
		t.Error("未聚焦时不该显示左对齐占位文案")
	}

	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText(titleSearchHint) {
		t.Error("聚焦后该隐藏「命令面板」文案")
	}
	if !tt.HasText(titleSearchPlaceholder) {
		t.Errorf("聚焦后该显示左对齐占位文案：%q", tt.Texts())
	}

	// 点窗外关面板时，占位文案和输入光标都要一起消失。mygo 关闭弹出层会把
	// 焦点还给锚点（输入框），所以这里还要断言输入框确实丢了焦点。
	tt.Press(950, 500)
	tt.Release(950, 500)
	tt.Frame()
	if tt.HasText(titleSearchPlaceholder) {
		t.Error("点窗外关面板后占位文案没有立即隐藏")
	}
	if !tt.HasText(titleSearchHint) {
		t.Error("点窗外关面板后没有恢复居中的「命令面板」")
	}
	if a.palette.focusTarget != 0 {
		t.Error("面板收起后输入框仍有焦点，光标没有跟着隐藏")
	}

	// 再点输入框展开，Tab 让输入框失焦，下一帧同样该恢复居中 hint。
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if tt.HasText(titleSearchPlaceholder) {
		t.Error("失焦后占位文案没有立即隐藏")
	}
	if !tt.HasText(titleSearchHint) {
		t.Error("失焦后没有恢复居中的「命令面板」")
	}
}
