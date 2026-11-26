package main

import (
	// 应用界面是简体中文，MyGo 自带控件、对话框与菜单的文案也要说中文。
	// 没有这份引入时，MyGo 自带文案会退回库里的英文来源，即使窗口的
	// locale 已是 zh-Hans。mygo generate 给应用目录生成的 catalog 文件
	// 也是用这个空引入把自带文案的译文加进来的（见 lobe_text.go 注册的
	// 应用 catalog：它只覆盖 Lobe 的文案，不覆盖 MyGo 自己的）。
	_ "github.com/egoist/mygo/locales/zh-Hans"
)
