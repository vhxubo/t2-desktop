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

// 搜索匹配英文别名；分类下拉用中文标签，要翻成分类 ID 再过滤。
func TestFilteredSystemEmojis(t *testing.T) {
	got := filteredSystemEmojis("grinning", systemEmojiAll)
	if len(got) == 0 || got[0].Char != "😀" {
		t.Errorf("英文搜索 grin 没找到 grinning face：%+v", got)
	}
	if all := filteredSystemEmojis("", systemEmojiAll); len(all) != systemEmojiCount {
		t.Errorf("全部数量不对：%d", len(all))
	}
	flags := filteredSystemEmojis("", "旗帜")
	if len(flags) == 0 {
		t.Fatal("旗帜分类为空")
	}
	for _, e := range flags {
		if e.Category != "flags" {
			t.Errorf("旗帜分类里混进了 %q (%s)", e.Char, e.Category)
		}
	}
	opts := systemEmojiOptions()
	if len(opts) != len(systemEmojiCategories)+1 || opts[0] != systemEmojiAll {
		t.Errorf("分类下拉选项不对：%v", opts)
	}
}

// 编辑器面板：切到「系统表情」后搜到并用鼠标插入 Unicode 字符，正文里不
// 应该出现 :code: 短码——系统表情是字符本身，不是服务端表情表的短码。
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
	if err := tt.Click("系统表情"); err != nil {
		t.Fatalf("找不到系统表情页签：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if a.compose.emojiTab != 1 {
		t.Fatalf("页签没切过去：%d", a.compose.emojiTab)
	}
	if err := tt.Click("搜索表情"); err != nil {
		t.Fatalf("找不到系统表情搜索框：%v：%q", err, tt.Texts())
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
