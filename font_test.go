package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 所有窗口的主题都取自 lobeCfg 的这两个实例，字体必须同时落到浅色与深色上；
// macOS 保留系统苹方，两个主题都留空。
func TestAppFontTheme(t *testing.T) {
	want := ""
	if usesHarmonySans() {
		want = appFontFamily
	}
	for _, dark := range []bool{false, true} {
		if got := homeTheme(dark).Font; got != want {
			t.Errorf("homeTheme(%v).Font = %q, want %q", dark, got, want)
		}
		if got := newThemeManager(t.TempDir()).theme(dark).Font; got != want {
			t.Errorf("themeManager.theme(%v).Font = %q, want %q", dark, got, want)
		}
	}
}

// 打包的三个字重必须是有效的 TrueType 文件，并确实注册成应用字体族。
func TestEmbeddedHarmonySansFonts(t *testing.T) {
	if !usesHarmonySans() {
		t.Skip("macOS 使用系统苹方，不打包注册鸿蒙字体")
	}
	for _, name := range harmonySansSCFiles {
		data, err := harmonySansSC.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		if len(data) < 4 || data[0] != 0 || data[1] != 1 || data[2] != 0 || data[3] != 0 {
			t.Errorf("%s 不是有效的 TrueType 字体（文件头 % x）", name, data[:4])
		}
	}
	found := false
	for _, f := range ui.FontFamilies() {
		if f.Name == appFontFamily && f.Registered {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("字体族 %q 没有注册成功", appFontFamily)
	}
}
