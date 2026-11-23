package main

import (
	"bytes"
	"image/png"
	"reflect"
	"runtime"
	"testing"
)

// 各张图标都得是约定尺寸的 PNG：托盘是 32×32（16 点的两倍，给高分屏），
// 应用图标是 256×256（Windows CreateIconFromResourceEx 能接受的最大尺寸），
// macOS 用的那套是 1024×1024。深浅两版不能是同一张图，否则切外观等于没切。
func TestIconAssets(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		size int
	}{
		{"应用浅色图标", appIcons.light, 256},
		{"应用深色图标", appIcons.dark, 256},
		{"macOS 应用浅色图标", appIconsLarge.light, 1024},
		{"macOS 应用深色图标", appIconsLarge.dark, 1024},
		{"托盘浅色图标", trayIcons.light, 32},
		{"托盘深色图标", trayIcons.dark, 32},
	}
	for _, tc := range cases {
		cfg, err := png.DecodeConfig(bytes.NewReader(tc.data))
		if err != nil {
			t.Errorf("%s 不是 PNG：%v", tc.name, err)
			continue
		}
		if cfg.Width != tc.size || cfg.Height != tc.size {
			t.Errorf("%s 是 %dx%d，期望 %dx%d", tc.name, cfg.Width, cfg.Height, tc.size, tc.size)
		}
	}
	for _, pair := range []struct {
		name        string
		light, dark []byte
	}{
		{"应用图标", appIcons.light, appIcons.dark},
		{"macOS 应用图标", appIconsLarge.light, appIconsLarge.dark},
		{"托盘图标", trayIcons.light, trayIcons.dark},
	} {
		if bytes.Equal(pair.light, pair.dark) {
			t.Errorf("%s的深浅两版是同一张图", pair.name)
		}
	}
}

// 深浅选择按外观走：浅色用 light，深色用 dark。
func TestIconPairForDark(t *testing.T) {
	if !bytes.Equal(appIcons.forDark(false), appIcons.light) {
		t.Error("浅色外观没有选应用浅色图标")
	}
	if !bytes.Equal(appIcons.forDark(true), appIcons.dark) {
		t.Error("深色外观没有选应用深色图标")
	}
	if !bytes.Equal(trayIcons.forDark(false), trayIcons.light) {
		t.Error("浅色外观没有选托盘浅色图标")
	}
	if !bytes.Equal(trayIcons.forDark(true), trayIcons.dark) {
		t.Error("深色外观没有选托盘深色图标")
	}
}

// 按平台挑图标：macOS 用 1024 的那套给 Dock，其余平台用 256 的那套。
func TestAppIconPNGByPlatform(t *testing.T) {
	small, large := iconPair{appIcons.light, appIcons.dark}, appIconsLarge
	for _, dark := range []bool{false, true} {
		want := small.forDark(dark)
		if runtime.GOOS == "darwin" {
			want = large.forDark(dark)
		}
		if got := appIconPNG(dark); !bytes.Equal(got, want) {
			t.Errorf("平台 %s 深色=%v 选错了应用图标", runtime.GOOS, dark)
		}
	}
}

// 外观没变就不重复设置原生图标；变了才应用，而且是先浅后深再回浅。
func TestIconThemeAppliesOnlyOnChange(t *testing.T) {
	m := &iconTheme{}
	var got []bool
	m.apply = func(dark bool) { got = append(got, dark) }
	for _, dark := range []bool{false, false, true, true, false} {
		m.setDark(dark)
	}
	want := []bool{false, true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应用图标的次数或顺序不对：%v，期望 %v", got, want)
	}
}

// 无头测试里应用没有就绪，refresh 不该触碰原生 API（mygo.Theme.IsDark 和
// Dock.SetIcon 都会等一个不存在的主循环）。
func TestIconThemeRefreshWaitsForReadyApp(t *testing.T) {
	m := newIconTheme(testBook(t))
	called := false
	m.apply = func(bool) { called = true }
	m.refresh()
	if called {
		t.Error("应用未就绪时不应该设置图标")
	}
}
