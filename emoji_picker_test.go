package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 内置表与线上 emoji-picker-react 的数据一致：8 类共 1898 个，常用表情与
// 多码点旗帜都要在，改成自己瞎凑的表会在这里挂。
func TestSystemEmojiData(t *testing.T) {
	if len(systemEmojis) != systemEmojiCount {
		t.Fatalf("内置表情数与常量不符：%d vs %d", len(systemEmojis), systemEmojiCount)
	}
	if systemEmojiCount != 1898 {
		t.Errorf("内置表情数变了：%d，线上当前是 1898", systemEmojiCount)
	}
	if len(systemEmojiCategories) != 8 {
		t.Errorf("分类数不对：%d", len(systemEmojiCategories))
	}
	byChar := make(map[string]systemEmoji, len(systemEmojis))
	for _, e := range systemEmojis {
		byChar[e.Char] = e
	}
	grin, ok := byChar["😀"]
	if !ok || !strings.Contains(grin.Names, "grinning") || grin.Category != "smileys_people" {
		t.Errorf("grinning face 数据不对：%+v", grin)
	}
	flag, ok := byChar["🇨🇳"]
	if !ok || flag.Category != "flags" {
		t.Errorf("多码点旗帜没解出来：%+v", flag)
	}
}

// 搜索匹配英文别名；全局搜索不带分类，按分组平铺时传分类 ID。
func TestFilteredSystemEmojis(t *testing.T) {
	got := filteredSystemEmojis("grinning", "")
	if len(got) == 0 || got[0].Char != "😀" {
		t.Errorf("英文搜索 grin 没找到 grinning face：%+v", got)
	}
	if all := filteredSystemEmojis("", ""); len(all) != systemEmojiCount {
		t.Errorf("全部数量不对：%d", len(all))
	}
	flags := filteredSystemEmojis("", "flags")
	if len(flags) == 0 {
		t.Fatal("旗帜分类为空")
	}
	for _, e := range flags {
		if e.Category != "flags" {
			t.Errorf("旗帜分类里混进了 %q (%s)", e.Char, e.Category)
		}
	}
}

// 编辑器面板里社区与系统表情同屏平铺：直接搜到系统表情并用鼠标插入 Unicode
// 字符，正文里不应该出现 :code: 短码——系统表情是字符本身，不是短码。
func TestEditorSystemEmojiPanelInsertsChar(t *testing.T) {
	a := testApp(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("表情"); err != nil {
		t.Fatalf("找不到表情入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("搜索表情"); err != nil {
		t.Fatalf("找不到表情搜索框：%v：%q", err, tt.Texts())
	}
	tt.Type("grinning")
	tt.Frame()

	if err := tt.Click("grinning"); err != nil {
		t.Fatalf("搜索结果里没有 grinning face：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if a.compose.body != "😀" {
		t.Errorf("点击系统表情应插入字符本身：%q", a.compose.body)
	}
	if strings.Contains(a.compose.body, ":") {
		t.Errorf("系统表情不该写成短码：%q", a.compose.body)
	}
}

// 表情面板改造：社区与系统表情同屏平铺，搜索框全局搜索，系统各分组直接平铺；
// 旧的页签、分类下拉、只看本帖、分页计数与「显示更多」都不再出现。
func TestEmojiPanelFlatLayout(t *testing.T) {
	a := testApp(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("表情"); err != nil {
		t.Fatalf("找不到表情入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()

	// 搜索框 + 社区分区 + 系统八个分组的轻量标题都在同一屏里平铺。
	for _, name := range []string{
		"搜索表情", "社区表情",
		"笑脸与人物", "动物与自然", "食物与饮品", "旅行与地点",
		"活动", "物品", "符号", "旗帜",
	} {
		if _, ok := tt.Find(name); !ok {
			t.Errorf("平铺布局缺少 %q", name)
		}
	}
	for _, name := range []string{
		"只看本帖表情", "分类", "系统表情", "显示更多表情",
	} {
		if _, ok := tt.Find(name); ok {
			t.Errorf("平铺布局不该还有 %q", name)
		}
	}

	// 10 列定宽格子应填满内容宽度：系统分组第一行的最后一格（🙃）右边缘
	// 要贴着分区标题的右边缘，不能留出一条空白。
	grid, okGrid := tt.Find("upside-down face")
	content, okContent := tt.Find("社区表情")
	if !okGrid || !okContent {
		t.Fatalf("找不到网格/分区参照：%q", tt.Texts())
	}
	if gap := (content.X + content.W) - (grid.X + grid.W); gap > 8 {
		t.Errorf("表情网格右侧留白过多：%.1f DIP", gap)
	}

	// 一个搜索框全局搜索：英文命中系统表情，社区区没有命中就整段收起。
	if err := tt.Click("搜索表情"); err != nil {
		t.Fatalf("找不到搜索框：%v", err)
	}
	tt.Type("grinning")
	tt.Frame()
	if !tt.HasText("grinning") || !tt.HasText("😀") {
		t.Errorf("全局搜索没命中系统表情：%q", tt.Texts())
	}
	if _, ok := tt.Find("社区表情"); ok {
		t.Errorf("grinning 没命中社区表情，社区标题应隐藏：%q", tt.Texts())
	}
}
