package main

import (
	"encoding/binary"
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

// fontTable 返回 TrueType/OpenType 字体某个表（4 字节 tag）在文件里的偏移，
// 没有该表时返回 0。
func fontTable(data []byte, tag string) int {
	if len(data) < 12 {
		return 0
	}
	n := int(binary.BigEndian.Uint16(data[4:6]))
	for i := 0; i < n; i++ {
		p := 12 + i*16
		if p+16 > len(data) {
			return 0
		}
		if string(data[p:p+4]) == tag {
			return int(binary.BigEndian.Uint32(data[p+8 : p+12]))
		}
	}
	return 0
}

// fontHasTable 报告 TrueType/OpenType 字体是否带有某个表（4 字节 tag）。
func fontHasTable(data []byte, tag string) bool {
	return fontTable(data, tag) != 0
}

// fontWeight 读取 OS/2 表的 usWeightClass（400 常规、700 加粗）。
func fontWeight(data []byte) int {
	off := fontTable(data, "OS/2")
	if off == 0 || off+6 > len(data) {
		return 0
	}
	return int(binary.BigEndian.Uint16(data[off+4 : off+6]))
}

// maxEmbeddedHarmonySans 是两个嵌入字体文件的总体积上限。常规与加粗各约
// 8MB，合计约 16MB；换回可变字体、再加字重或塞入 Medium 会让测试失败，
// 避免包体回归。
const maxEmbeddedHarmonySans = 18 << 20

// 打包的字体必须是有效的 TrueType 静态字体，覆盖常规(400)与加粗(700)两档，
// 并确实注册成应用字体族。检查 fvar 表是为了防止重新引入可变字体。
// macOS 用系统苹方，不嵌入字体数据。
func TestEmbeddedHarmonySansFont(t *testing.T) {
	if !usesHarmonySans() {
		if len(harmonySansSCRegularData) != 0 || len(harmonySansSCBoldData) != 0 {
			t.Fatalf("macOS 不应嵌入鸿蒙字体，实际嵌入了 %d 字节",
				len(harmonySansSCRegularData)+len(harmonySansSCBoldData))
		}
		t.Skip("macOS 使用系统苹方，不打包注册鸿蒙字体")
	}
	fonts := []struct {
		file   string
		data   []byte
		weight int
	}{
		{harmonySansSCRegularFile, harmonySansSCRegularData, 400},
		{harmonySansSCBoldFile, harmonySansSCBoldData, 700},
	}
	total := 0
	for _, f := range fonts {
		if len(f.data) == 0 {
			t.Fatalf("%s 没有嵌入", f.file)
		}
		total += len(f.data)
		if len(f.data) < 4 {
			t.Fatalf("%s 只有 %d 字节，不是有效的字体文件", f.file, len(f.data))
		}
		if f.data[0] != 0 || f.data[1] != 1 || f.data[2] != 0 || f.data[3] != 0 {
			t.Errorf("%s 不是有效的 TrueType 字体（文件头 % x）", f.file, f.data[:4])
		}
		if fontHasTable(f.data, "fvar") {
			t.Errorf("%s 是可变字体，应该换成静态文件", f.file)
		}
		if got := fontWeight(f.data); got != f.weight {
			t.Errorf("%s 的字重是 %d，应该是 %d", f.file, got, f.weight)
		}
	}
	if total > maxEmbeddedHarmonySans {
		t.Errorf("嵌入字体共 %d 字节，超过 %d 字节的上限", total, maxEmbeddedHarmonySans)
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
