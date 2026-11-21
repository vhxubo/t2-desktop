package main

import (
	"testing/fstest"

	"github.com/egoist/mygo/i18n"
)

// 详情页的正文栏用 Lobe 的 layout.DraggablePanel：接缝上的按钮默认说
// 「折叠面板 / 展开面板」，接缝的无障碍名是「调整面板大小」。这里按 Lobe
// 的约定用 lobe.* 键覆盖成项目自己的中文，保留页面原有的说法，也符合
// AGENTS.md「中文界面文案」与测试里的中文命中名。
func init() {
	// 同时给 en 与 zh-Hans 两份：窗口用哪种 locale，接缝按钮都说项目自己的
	// 中文名，不受系统语言影响（Lobe 自带 en 文案是英文，必须覆盖）。
	msgs := []byte(`{
  "lobe.close": "关闭",
  "lobe.ok": "确定",
  "lobe.cancel": "取消",
  "lobe.more": "更多",
  "lobe.panelCollapse": "收起正文",
  "lobe.panelExpand": "展开正文",
  "lobe.panelResize": "调整宽度"
}`)
	cat, err := i18n.NewCatalog(fstest.MapFS{
		"en.json":      &fstest.MapFile{Data: msgs},
		"zh-Hans.json": &fstest.MapFile{Data: msgs},
	}, "en")
	if err == nil {
		i18n.Register(cat)
	}
}
