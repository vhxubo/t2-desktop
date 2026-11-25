package main

import (
	"math"
	"path/filepath"
	"testing"
)

func TestTypographyDefaults(t *testing.T) {
	s := newTypographyStore(t.TempDir())
	if got := s.bodySize(); got != 16 {
		t.Errorf("默认正文字号 = %d，想要 16", got)
	}
	if got := s.lineHeight(); got != 17 {
		t.Errorf("默认行距 = %d，想要 17", got)
	}
	if got := s.pageWidth(); got != 760 {
		t.Errorf("默认页宽 = %d，想要 760", got)
	}
	if got := s.density(); got != typographyDensityStandard {
		t.Errorf("默认密度 = %d，想要标准档", got)
	}
}

func TestTypographyPersist(t *testing.T) {
	dir := t.TempDir()
	s := newTypographyStore(dir)
	for _, change := range []func() error{
		func() error { return s.setBodySize(20) },
		func() error { return s.setLineHeight(19) },
		func() error { return s.setPageWidth(880) },
		func() error { return s.setDensity(typographyDensityLoose) },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
	}
	again := newTypographyStore(dir)
	if got := [4]int{again.bodySize(), again.lineHeight(), again.pageWidth(), again.density()}; got != [4]int{20, 19, 880, typographyDensityLoose} {
		t.Errorf("重新读取的排版 = %v，想要 [20 19 880 2]", got)
	}
}

func TestTypographyInvalidFallsBack(t *testing.T) {
	dir := t.TempDir()
	cfg := typographyConfig{BodySize: 99, LineHeight: 0, PageWidth: -1, RowPadY: 99, RowGap: 99}
	if err := writeJSONFile(filepath.Join(dir, typographyFileName), cfg); err != nil {
		t.Fatal(err)
	}
	s := newTypographyStore(dir)
	if got := s.bodySize(); got != 16 {
		t.Errorf("越界字号没有回默认：%d", got)
	}
	if got := s.lineHeight(); got != 17 {
		t.Errorf("越界行距没有回默认：%d", got)
	}
	if got := s.pageWidth(); got != 760 {
		t.Errorf("越界页宽没有回默认：%d", got)
	}
	if got := s.density(); got != typographyDensityStandard {
		t.Errorf("越界密度没有回默认：%d", got)
	}

	// 一个坏值不能把其它合法项一起丢掉。
	cfg = typographyConfig{BodySize: 18, LineHeight: 15, PageWidth: 640, RowPadY: 6, RowGap: 99}
	if err := writeJSONFile(filepath.Join(dir, typographyFileName), cfg); err != nil {
		t.Fatal(err)
	}
	s = newTypographyStore(dir)
	if got := s.bodySize(); got != 18 {
		t.Errorf("合法字号被坏值连带丢掉：%d", got)
	}
	if got := s.lineHeight(); got != 15 {
		t.Errorf("合法行距被坏值连带丢掉：%d", got)
	}
	if got := s.pageWidth(); got != 640 {
		t.Errorf("合法页宽被坏值连带丢掉：%d", got)
	}
	if got := s.rowPadY(); got != 6 {
		t.Errorf("合法行留白被坏值连带丢掉：%d", got)
	}
	if got := s.rowGap(); got != 4 {
		t.Errorf("越界行内间距没有回默认：%d", got)
	}
	if got := s.density(); got != -1 {
		t.Errorf("混合数值不该命中档位：%d", got)
	}
}

func TestTypographyReset(t *testing.T) {
	dir := t.TempDir()
	s := newTypographyStore(dir)
	if err := s.setBodySize(20); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot(); got != defaultTypographyConfig() {
		t.Errorf("Reset 后内存配置 = %+v，想要 %+v", got, defaultTypographyConfig())
	}
	again := newTypographyStore(dir)
	if got := again.snapshot(); got != defaultTypographyConfig() {
		t.Errorf("Reset 后落盘配置 = %+v，想要 %+v", got, defaultTypographyConfig())
	}
}

// 精确数值与档位共用同一份配置：非预设值能落盘，越界值被拒绝且内存不变。
func TestTypographyExactValues(t *testing.T) {
	dir := t.TempDir()
	s := newTypographyStore(dir)
	for _, change := range []func() error{
		func() error { return s.setBodySize(17) },
		func() error { return s.setLineHeight(16) },
		func() error { return s.setPageWidth(777) },
		func() error { return s.setRowPadY(8) },
		func() error { return s.setRowGap(3) },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
	}
	again := newTypographyStore(dir)
	if got := again.snapshot(); got != (typographyConfig{17, 16, 777, 8, 3}) {
		t.Errorf("精确数值没有按原样落盘：%+v", got)
	}
	if got := again.density(); got != -1 {
		t.Errorf("精确行留白 / 间距不该命中档位：%d", got)
	}
	if got := s.bodySize(); got != 17 {
		t.Errorf("精确正文字号没有生效：%d", got)
	}
	if got := densityPresetIndex(10, 4); got != typographyDensityStandard {
		t.Errorf("标准档没有命中：%d", got)
	}

	before := s.snapshot()
	for _, bad := range []func() error{
		func() error { return s.setBodySize(typographyBodyMax + 1) },
		func() error { return s.setLineHeight(typographyLineMin - 1) },
		func() error { return s.setPageWidth(typographyWidthMax + 1) },
		func() error { return s.setRowPadY(typographyPadMax + 1) },
		func() error { return s.setRowGap(typographyGapMin - 1) },
	} {
		if err := bad(); err == nil {
			t.Error("越界数值应该被拒绝")
		}
		if got := s.snapshot(); got != before {
			t.Errorf("越界值改坏了内存配置：%+v", got)
		}
	}
	if err := s.setDensity(typographyDensityCompact); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot(); got != (typographyConfig{17, 16, 777, 6, 2}) {
		t.Errorf("选档位没有覆盖精确数值：%+v", got)
	}
}

func TestTypeSizesScaling(t *testing.T) {
	for _, body := range typographyBodySizes {
		a := testApp(t)
		if err := a.typePrefs.setBodySize(body); err != nil {
			t.Fatal(err)
		}
		ts := a.typeSizes()
		if !(ts.title > ts.body && ts.body > ts.comment && ts.comment > ts.meta && ts.meta > ts.metaSmall) {
			t.Errorf("字号 %d 档阶梯不对：%+v", body, ts)
		}
		for _, v := range []float32{ts.title, ts.body, ts.author, ts.comment, ts.section, ts.meta, ts.metaSmall, ts.notice} {
			if math.Abs(float64(v*2)-math.Round(float64(v*2))) > 1e-6 {
				t.Errorf("字号 %d 档出现非 0.5 DIP 的值 %g：%+v", body, v, ts)
			}
		}
	}

	// 标准档必须逐项等于改造前的常量。
	a := testApp(t)
	if got, want := a.typeSizes(), (typeSizes{26, 16, 14, 15, 16, 13, 12, 12}); got != want {
		t.Errorf("标准档字号 = %+v，想要 %+v", got, want)
	}
}

func TestMdStylesLineHeight(t *testing.T) {
	a := testApp(t)
	for i, line := range typographyLineHeights {
		if err := a.typePrefs.setLineHeight(line); err != nil {
			t.Fatal(err)
		}
		post, comment, bio := a.mdStyles()
		if want := float32(line) / 10; post.line != want {
			t.Errorf("行距档 %d 的正文行高 = %g，想要 %g", line, post.line, want)
		}
		if want := post.line - 0.05; math.Abs(float64(comment.line-want)) > 1e-6 {
			t.Errorf("评论行距应比正文紧 0.05：%g / %g", comment.line, post.line)
		}
		if bio != bioTextStyle {
			t.Errorf("个人简介不应跟随行距变化：%+v", bio)
		}
		if i == 1 {
			if post.size != 16 || comment.size != 15 {
				t.Errorf("标准档 Markdown 字号漂移：%g / %g", post.size, comment.size)
			}
		}
	}
}

func TestTypographyRowDensity(t *testing.T) {
	a := testApp(t)
	wantPad := []float32{6, 10, 14}
	wantGap := []float32{2, 4, 6}
	for density := typographyDensityCompact; density <= typographyDensityLoose; density++ {
		if err := a.typePrefs.setDensity(density); err != nil {
			t.Fatal(err)
		}
		if got := a.rowPadY(); got != wantPad[density] {
			t.Errorf("密度 %d 的行留白 = %g，想要 %g", density, got, wantPad[density])
		}
		if got := a.rowGap(); got != wantGap[density] {
			t.Errorf("密度 %d 的行内间距 = %g，想要 %g", density, got, wantGap[density])
		}
	}
}
