package main

import "testing"

// 更新插件在 app 建好之后才装上：main 里先 newMainApp，再 mygo.Use，最后才把
// 进程级的 updates 标记立起来。设置页点「检查更新」必须读到这个最新标记，
// 不能拿构造 app 时的旧快照，否则真机上点了没反应。
func TestCheckForUpdatesAfterPluginInstalled(t *testing.T) {
	old := checkForUpdatesForApp
	t.Cleanup(func() { checkForUpdatesForApp = old })
	var called int
	checkForUpdatesForApp = func() { called++ }

	d := newDesktop(t.TempDir())
	a := newMainApp(d)
	a.checkForUpdates()
	if called != 0 {
		t.Fatal("更新插件还没装，不该打开更新窗口")
	}

	// 模拟 main 在 mygo.Use 之后立标记：此时 app 早已建好。
	d.updates = true
	a.checkForUpdates()
	if called != 1 {
		t.Fatalf("装好插件后点「检查更新」没有调用更新插件：%d", called)
	}
}
