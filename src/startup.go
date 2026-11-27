package main

import (
	"github.com/egoist/mygo"
)

/* ------------------------------------------------------------------
   开机启动：直接交给 mygo 登记的登录启动项
---------------------------------------------------------------- */

// 开机启动的状态以系统里的登录启动项为准，不再另存一份偏好：mygo 的
// SetOpenAtLogin 在 Windows 写当前用户的 Run 项、在 macOS 注册登录项、在 Linux
// 写 XDG autostart 条目，OpenAtLogin 读回同一处状态。全新安装没有这些条目，
// 所以默认不开启。
//
// 两个变量是测试替身：无头测试没有原生后端和主循环，调用 mygo 的登录项接口
// 会卡在等待主线程上。main 在 Run 之前把它们接到 mygo，测试保持安全的空实现。
var (
	openAtLoginForApp    = func() bool { return false }
	setOpenAtLoginForApp = func(on bool) error { return nil }
)

// wireStartupHooks 在应用启动时把开机启动开关接到 mygo 的原生实现。
func wireStartupHooks() {
	openAtLoginForApp = func() bool { return mygo.App.OpenAtLogin() }
	setOpenAtLoginForApp = func(on bool) error { return mygo.App.SetOpenAtLogin(on) }
}

// setStartupEnabled 切换开机启动：开关立即写到系统登录项，结果用窗口内 toast
// 说明。写失败时只提示，不假装已经生效；界面下一帧按系统的真实状态重建。
func (a *app) setStartupEnabled(on bool) {
	if err := setOpenAtLoginForApp(on); err != nil {
		a.toast(toastSettings, "设置开机启动失败："+err.Error())
		return
	}
	if on {
		a.toast(toastSettings, "已开启开机启动")
	} else {
		a.toast(toastSettings, "已关闭开机启动")
	}
	a.publish(func() {})
}
