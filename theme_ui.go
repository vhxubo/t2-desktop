package main

// saveThemeChoice 把设置窗口「外观」页选好的深浅色落盘、同步原生外观，并让
// 所有还开着的窗口按新主题重建。ui.Segmented 先把选择写进 a.theme.mode，
// 分段控件的 Changed 里值已经变了，这里不再判等，否则 theme.json 与原生
// 外观都得不到更新；写盘失败只影响下次启动的默认外观，本次仍按新选择显示。
func (a *app) saveThemeChoice() {
	err := a.theme.Save(a.theme.mode)
	a.syncNativeAppearance()
	// mygo 的 SetSource 不会触发 Theme.OnUpdated，这里显式换图标；系统与
	// 跟随系统模式下的变化由 startIcons 订阅的事件处理。
	if a.book != nil {
		a.book.refreshIcons()
	}
	// 外观是进程级共享状态：主窗口与附属窗口都要跟着换深浅色，publish 会把
	// 它们重建一遍，否则别的窗口还停在上一帧的主题。
	a.publish(func() {})
	if err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.toast(toastSettings, "外观："+a.theme.label())
}
