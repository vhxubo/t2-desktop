package main

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 设置窗口的尺寸：比代理弹窗宽一些，放得下「通用 / 网络 / 图片 / 阅读与评论 /
// 通知 / 快捷键」几页。
const (
	settingsWindowWidth  = 520
	settingsWindowHeight = 700
	settingsWindowMinW   = 440
	settingsWindowMinH   = 420
)

// 设置窗口按大类分页：只有一两个配置的小页合并到同一类里，别让它们各占一个
// 标签。同一页里各块仍用区块标题分开，配置项与持久化都保持原样。标签固定在
// 内容区上方；每页的草稿都绑在 app 字段上，切页不会丢。默认打开「通用」。
const (
	settingsTabGeneral = iota
	settingsTabNetwork
	settingsTabImages
	settingsTabReading
	settingsTabNotify
	settingsTabShortcuts
)

var settingsTabLabels = []string{"通用", "网络", "图片", "阅读与评论", "通知", "快捷键"}

/* ------------------------------------------------------------------
   设置窗口：全局入口、显示与关闭
---------------------------------------------------------------- */

// showSettings 是设置窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程，窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showSettings() {
	if a.onSettingsOpen != nil {
		a.onSettingsOpen()
	}
	a.publish(a.showSettingsWindow)
}

// showSettingsWindow 在主线程上真正打开设置窗口。打开时重新读一次代理草稿，
// 并开始统计图片缓存大小，首帧里显示的就是当前状态。
func (a *app) showSettingsWindow() {
	a.reloadProxyDraft()
	// 通知偏好在别处也可能被改（通知窗口的快捷开关、另一个账号窗口），打开
	// 设置时同步一次输入框草稿，别把上次没保存的半截文字带回来。
	a.notifyKeywordDraft = a.notifPrefs().keywordText()
	// 打开设置就把缓存大小重新统计一遍；统计在后台跑，结果回到主线程。
	a.startCacheStats()
	// 无头测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，所以只把
	// 状态准备好，测试直接渲染 settingsWindowView。
	if a.win == nil {
		return
	}
	if win := a.settingsWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	a.reloadImageHostSettings()
	// 重新开窗口时回到第一页；已经开着的窗口只带到前台，保留用户当前所在页。
	a.settingsTab = settingsTabGeneral
	a.resetConfirmOpen = false
	win := a.newAppWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("设置"),
		Width:             settingsWindowWidth,
		Height:            settingsWindowHeight,
		MinWidth:          settingsWindowMinW,
		MinHeight:         settingsWindowMinH,
		StateKey:          "settings",
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.settingsWindowView)),
	}, toastSettings)
	a.settingsWin = win
	win.OnClosed(func() {
		a.settingsWin = nil
		a.authGateWindowClosed(toastSettings)
		a.accountWindowClosed(win)
	})
}

// reloadImageHostSettings 在真正新开设置窗口时重读图库配置，并清掉上一次
// 留下的「展开哪一条」「MB 草稿绑到哪一条」。已经开着的窗口只带到前台，
// 不重读：用户可能正填到一半，重新加载会把没保存的编辑抹掉。
func (a *app) reloadImageHostSettings() {
	a.loadImageHosts()
	a.hostEditLabel = ""
	a.hostMaxFor = ""
}

// closeSettingsWindow 关掉设置窗口：主窗口关闭时调用，别留下一个没有主窗口
// 的孤岛。只在主线程调用。
func (a *app) closeSettingsWindow() {
	win := a.settingsWin
	if win == nil {
		return
	}
	a.settingsWin = nil
	win.Close()
}

// redrawSettingsWindow 让设置窗口按最新状态重建。只在主线程调用。
func (a *app) redrawSettingsWindow() {
	if win := a.settingsWin; win != nil {
		win.Invalidate()
	}
}

// checkForUpdates 是设置窗口里的「检查更新」：真实应用里交给 updater 插件，
// 它会打开更新窗口说明结果；无头测试与未装插件的构建只触发测试钩子。
func (a *app) checkForUpdates() {
	if a.onUpdateCheck != nil {
		a.onUpdateCheck()
	}
	if a.win == nil || !a.updates {
		return
	}
	updater.CheckForUpdates()
}

/* ------------------------------------------------------------------
   设置窗口的界面

   整套界面走 Lobe UI：颜色取 TokensOf(c)，标签 / 按钮 / 分段 / 字段 / 提示
   分别由 display.Text、action.Button、action.Segmented、field 与 feedback 提供，
   与主窗口及各附属窗口用同一套设计语言。文案与动作保持不变，只换观感。
---------------------------------------------------------------- */

// settingsWindowView 是设置窗口的 view。窗口自己带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) settingsWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	c.Root().Background(tk.BgLayout)
	// 保存代理、清理缓存的结果以 toast 显示在这个窗口里。
	defer a.drainToasts(c, a.claimsToasts(toastSettings))
	c.OnShortcut(0, ui.KeyEscape, func() {
		if a.settingsTab == settingsTabShortcuts && a.shortcutRecordingID != "" {
			a.stopShortcutRecording()
			return
		}
		a.closeSettingsWindow()
	})

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角是最小化与关闭。图标换成 Lucide 的
		// 齿轮，文字走 display.Text，配色走 Lobe token，按钮与主窗口同一套画法。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.Settings()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "设置", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.settingsWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", func() { a.closeSettingsWindow() })
		})
		layout.Divider(c, layout.DividerProps{})

		// 分类标签固定在内容区上方：切页只换下面的 section，页面高度跟着
		// 当前这块内容走，不再由六块加起来决定。
		ui.Row(c).FillWidth().Padding(2, 12).Children(func() {
			c.Key("settings-tabs")
			items := make([]nav.TabItem, 0, len(settingsTabLabels))
			for _, label := range settingsTabLabels {
				items = append(items, nav.TabItem{Label: label})
			}
			sel := a.settingsTab
			nav.Tabs(c, &sel, nav.TabsProps{
				Items:   items,
				Variant: nav.TabsSquare,
				Size:    lobetheme.Small,
			}, nil)
			if sel != a.settingsTab && sel >= 0 && sel < len(settingsTabLabels) {
				a.settingsTab = sel
			}
		})
		layout.Divider(c, layout.DividerProps{})

		// 每页给一个独立的 key，避免不同分类复用彼此的输入框 / 分段控件状态。
		c.Key(fmt.Sprintf("settings-page-%d", a.settingsTab))
		// ScrollArea 用 Lobe 的细滚动条与边缘渐隐，不再用 mygo 原生 Scroll。
		list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			ui.Column(c).FillWidth().Padding(18, 20).Gap(10).Children(func() {
				switch a.settingsTab {
				case settingsTabNetwork:
					a.proxySettingsSection(c)
				case settingsTabImages:
					a.imageSettingsSection(c)
					a.imageCacheSection(c)
					a.imageHostsSection(c)
				case settingsTabReading:
					a.commentSettingsSection(c)
					a.readingSettingsSection(c)
				case settingsTabNotify:
					a.notificationSettingsSection(c)
				case settingsTabShortcuts:
					a.shortcutSettingsSection(c)
				default:
					a.appearanceSettingsSection(c)
					a.windowSettingsSection(c)
					a.autoSignSettingsSection(c)
					a.updateSettingsSection(c)
					a.resetSettingsSection(c)
				}
			})
		})
		list.Grow(1).MinHeight(0)
	})
	// 设置里也有带令牌的请求（例如官方图床测试）：缺权限时弹在这里。
	a.authDialogFor(c, toastSettings)
}

// settingsSection 画一块带标题的平面区块，各分类共用同一套版式。区块之间
// 只留一条细分隔线，不再用圆角描边卡片把设置项框起来。
func settingsSection(c *ui.Context, title string, body func()) {
	card := popupRow(c, false).FillWidth().Padding(14, 14)
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			display.Text(c, title, display.TextProps{Strong: true, FontSize: 13, Rows: 1})
			body()
		})
	})
}

/* ------------------------------------------------------------------
   Lobe 字段的小工具
---------------------------------------------------------------- */

// settingsField 在整行上画一个带可见标签的 Lobe 字段：field 的输入控件返回的是
// 内层输入，外层那一行默认按内容收缩，这里借 Column 的 stretch 把外层撑满，
// 再把可以继续配置（Label / Bind）的内层控件交回调用方。可见文字由 display.Text
// 画，控件的 Label 供读屏与无头测试命中。
func settingsField(c *ui.Context, label string, build func() ui.Element) ui.Element {
	var el ui.Element
	ui.Column(c).FillWidth().Gap(5).AlignItems(ui.Stretch).Children(func() {
		if label != "" {
			display.Text(c, label, display.TextProps{FontSize: 12, Rows: 1})
		}
		el = build()
	})
	return el
}

// settingsHalfField 同上，但外层只占一行的一半，用于成对的输入框。
func settingsHalfField(c *ui.Context, label string, build func() ui.Element) ui.Element {
	var el ui.Element
	ui.Column(c).Grow(1).MinWidth(0).Gap(5).AlignItems(ui.Stretch).Children(func() {
		display.Text(c, label, display.TextProps{FontSize: 12, Rows: 1})
		el = build()
	})
	return el
}

// settingsDescription 是每页开头的说明文字。
func settingsDescription(c *ui.Context, text string, rows int) {
	display.Text(c, text, display.TextProps{
		Type: display.TextSecondary, FontSize: 12, Rows: rows,
	})
}

// settingsChoice 画一整行的 Lobe 分段选择，返回这一帧选中的序号。用的是
// choice.ToggleGroup 的 Segmented 样式而不是 action.Segmented：后者切换时有
// 一枚游标滑过 240ms，紧接着点另一档会落在旧游标上（无头测试里尤其明显），
// 而设置里的几档只是普通选项，按钮自己的高亮就够了。
func settingsChoice(c *ui.Context, label string, options []string, index int) int {
	if index < 0 || index >= len(options) {
		index = 0
	}
	selected := []string{options[index]}
	items := make([]choice.ToggleItem, 0, len(options))
	for _, option := range options {
		items = append(items, choice.ToggleItem{Value: option, Label: option})
	}
	choice.ToggleGroup(c, &selected, choice.ToggleGroupProps{
		Items: items, Variant: choice.ToggleSegmented, Size: lobetheme.Small,
	}).Label(label)
	if len(selected) > 0 {
		for i, option := range options {
			if selected[0] == option {
				return i
			}
		}
	}
	return index
}

// settingsCheckbox 画一整行的 Lobe 开关，返回这一帧勾选的状态。绑定在局部值上，
// 点击带来的变化在构造这一帧就已经写进去了（和 settingsChoice 同一套写法），
// 由调用方据此立即落盘；下一帧再按偏好重建，开关显示的就是新状态。
func settingsCheckbox(c *ui.Context, label string, checked bool) bool {
	v := checked
	choice.Checkbox(c, &v, label, choice.CheckboxProps{})
	return v
}

/* ------------------------------------------------------------------
   更新一块：检查更新
---------------------------------------------------------------- */

// updateMirrorSelectOptions 是「下载加速」下拉的完整选项：首项「不使用（走
// 代理）」的 Value 是空串，选中它就清空加速入口——选过镜像后随时可以改回
// 不经过第三方中转、只用「网络代理」里配置的代理直连 GitHub。
func updateMirrorSelectOptions() []field.SelectOption {
	options := make([]field.SelectOption, 0, len(updateMirrorOptions)+1)
	options = append(options, field.SelectOption{Value: "", Label: updateMirrorNoChoice})
	for _, m := range updateMirrorOptions {
		options = append(options, field.SelectOption{Value: m, Label: m})
	}
	return options
}

// updateSettingsSection 是「更新」一块：应用每天在后台自动检查一次，这里给
// 想立即确认版本的人一个入口；「接收测试版」默认关，打开后检查更新也提供
// GitHub 上的 prerelease；下载加速入口默认不选，选了镜像后检查更新与更新包
// 下载都会经它转发（测试版同样适用），选回首项即清空。
func (a *app) updateSettingsSection(c *ui.Context) {
	settingsSection(c, "更新", func() {
		settingsDescription(c, "应用每天自动检查一次 GitHub Release；检查更新和下载更新包都会走「网络代理」里设置的代理。GitHub 连不上或下载太慢时可以选一个加速入口，选完立即生效。", 3)
		if v := settingsCheckbox(c, "接收测试版", a.updateChannel.prerelease()); v != a.updateChannel.prerelease() {
			a.setUpdatePrereleases(v)
		}
		settingsDescription(c, "打开后检查更新也会提供 Beta / Pre-release 版本；"+
			"测试版可能不稳定，遇到问题关掉即可，下一次正式版发布后照常升级。", 2)
		mirror := ""
		if a.updateMirror != nil {
			mirror = a.updateMirror.current()
		}
		var sel ui.Element
		settingsField(c, "下载加速", func() ui.Element {
			sel = lobeSelect(c, &mirror, field.SelectProps{
				Options: updateMirrorSelectOptions(), Size: lobetheme.Small,
			})
			sel.Label("下载加速").Tooltip("仅作用于检查更新与更新包下载").Width(220)
			return sel
		})
		if sel.Changed() {
			a.setUpdateMirror(mirror)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "检查更新", action.ButtonProps{}).Clicked() {
				a.checkForUpdates()
			}
			ui.Spacer(c).Grow(1)
		})
	})
}

// setUpdatePrereleases 开关「接收测试版」：立即落盘并接到更新器上，结果用
// 窗口内 toast 说明。通道是进程级的，publish 让所有窗口下一帧按新状态重建。
func (a *app) setUpdatePrereleases(on bool) {
	if a.updateChannel == nil || a.updateChannel.prerelease() == on {
		return
	}
	if err := a.updateChannel.setPrerelease(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	applyUpdateChannel(on)
	if on {
		a.toast(toastSettings, "检查更新也会提供测试版（Beta）")
	} else {
		a.toast(toastSettings, "检查更新只提供正式版")
	}
	a.publish(func() {})
}

// setUpdateMirror 换下载加速入口：立即落盘并接到更新器上，结果用窗口内 toast
// 说明。镜像选择是进程级的，publish 让所有窗口下一帧按新状态重建。
func (a *app) setUpdateMirror(m string) {
	if a.updateMirror == nil || a.updateMirror.current() == m {
		return
	}
	if err := a.updateMirror.set(m); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	applyUpdateMirror(m)
	if m == "" {
		a.toast(toastSettings, "已清空加速入口，更新改回走「网络代理」直连 GitHub")
	} else {
		a.toast(toastSettings, "检查更新与下载将经 "+m+" 加速")
	}
	a.publish(func() {})
}

/* ------------------------------------------------------------------
   窗口一块：关闭主窗口时退出还是收进托盘
--------------------------------------------------------------- */

// windowSettingsSection 是「窗口」一块：关闭主窗口时的行为。选择立即落盘，
// 不用点保存；结果用窗口内 toast 说明。托盘不可用时「收进托盘」说不通，
// 所以直接说明它会按退出处理。
func (a *app) windowSettingsSection(c *ui.Context) {
	settingsSection(c, "窗口", func() {
		settingsDescription(c, "多账号时每个账号一个窗口：关掉某个账号的窗口只是把它挂回账号面板，"+
			"未读数照样更新、随时点开；所有窗口都关掉才结束应用。"+
			"收进托盘则应用继续在后台接收通知，点托盘图标回到窗口；"+
			"退出应用会在关窗口时结束整个应用。", 4)
		if sel := settingsChoice(c, "关闭主窗口时",
			[]string{"每次询问", "关闭窗口", "收进托盘", "退出应用"}, a.closeMode.mode,
		); sel != a.closeMode.mode {
			a.setCloseMode(sel)
		}
		if !a.trayReady {
			display.Text(c, "系统托盘当前不可用，关闭窗口只能直接关掉。",
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		}
	})
}

/* ------------------------------------------------------------------
   自动签到一块：打开应用时为当前账号签到
--------------------------------------------------------------- */

// autoSignSettingsSection 是「签到」一块：打开应用时自动提交今日签到，默认
// 开启。开关立即落盘，进程级共享；已签到不弹任何提示，新签到才在内容顶部弹
// 一条明显的结果。
func (a *app) autoSignSettingsSection(c *ui.Context) {
	p := a.autoSignPrefs
	settingsSection(c, "签到", func() {
		settingsDescription(c, "打开应用时自动为当前账号签到；已签到不会提示。", 1)
		if v := settingsCheckbox(c, "打开应用时自动签到", p.on()); v != p.on() {
			a.setAutoSignEnabled(v)
		}
	})
}

/* ------------------------------------------------------------------
   外观一块：跟随系统 / 浅色 / 深色
--------------------------------------------------------------- */

// appearanceSettingsSection 是「外观」一块：原来侧边栏底部的三个外观入口收在
// 这里，侧边栏不再单独占三行。选完立即落盘并同步原生外观，结果用窗口内 toast
// 说明；主题是进程级共享状态，所有窗口都会跟着重画。
func (a *app) appearanceSettingsSection(c *ui.Context) {
	settingsSection(c, "外观", func() {
		settingsDescription(c, "跟随系统会随操作系统的深浅色自动切换；固定浅色或深色后，"+
			"即使系统切换外观也保持不变。", 3)
		if sel := settingsChoice(c, "外观",
			[]string{"跟随系统", "浅色", "深色"}, a.theme.mode,
		); sel != a.theme.mode {
			a.theme.mode = sel
			a.saveThemeChoice()
		}
	})
}

/* ------------------------------------------------------------------
   评论一块：金币消耗与徽章切换、发送按钮上的金币数
 --------------------------------------------------------------- */

// commentSettingsSection 是「评论」一块：两枚开关都立即落盘，进程级共享，
// 所有账号窗口的评论框下一帧就按新状态画（见 comment_cost_prefs.go）。
func (a *app) commentSettingsSection(c *ui.Context) {
	settingsSection(c, "评论", func() {
		settingsDescription(c, "控制评论框里的金币消耗提示与徽章切换入口。", 2)
		if v := settingsCheckbox(c, "金币消耗与徽章切换",
			a.commentCostPrefs.visible()); v != a.commentCostPrefs.visible() {
			a.setCommentCostBarEnabled(v)
		}
		if v := settingsCheckbox(c, "在发送按钮上显示消耗金币数",
			a.commentCostPrefs.onSend()); v != a.commentCostPrefs.onSend() {
			a.setCommentCostOnSend(v)
		}
	})
}

/* ------------------------------------------------------------------
   阅读一块：浏览历史与稍后再看
  --------------------------------------------------------------- */

// readingSettingsSection 是「阅读」一块：浏览历史与稍后再看都存在本机、按
// 账号隔离，这里只提供「打开后保留」这一个行为开关。清空入口在稍后再看
// 页的两段顶部，按当前账号清，不和服务端收藏混在一起。
func (a *app) readingSettingsSection(c *ui.Context) {
	p := a.watchLaterPrefs
	settingsSection(c, "阅读记录", func() {
		settingsDescription(c, "浏览历史与稍后再看只存在本机，并按账号隔离。", 1)

		if v := settingsCheckbox(c, "打开后保留在稍后再看", p.keepAfterOpen()); v != p.keepAfterOpen() {
			a.setKeepWatchLater(v)
		}
		settingsDescription(c, "默认打开即移除；打开后保留并区分已读 / 未读。", 1)

		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "打开稍后再看", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				// 稍后再看是主窗口内嵌页：先收起设置窗口，免得新页面被它盖住。
				a.closeSettingsWindow()
				a.showWatchLater()
			}
			ui.Spacer(c).Grow(1)
		})
	})
}

// setKeepWatchLater 保存「打开后保留」开关。关掉时顺手清掉已读标记：默认
// 模式没有已读概念，留着会让下次打开开关时冒出上一次的旧状态。
func (a *app) setKeepWatchLater(on bool) {
	if a.watchLaterPrefs == nil || a.watchLaterPrefs.keepAfterOpen() == on {
		return
	}
	if err := a.watchLaterPrefs.setKeepAfterOpen(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	if !on {
		a.clearWatchLaterReadMarks()
	}
	a.redrawWatchLaterPage()
	if on {
		a.toast(toastSettings, "打开后保留在稍后再看，已读与未读分开显示")
	} else {
		a.toast(toastSettings, "打开后从稍后再看移除")
	}
}

/* ------------------------------------------------------------------
   通知一块：桌面通知、提示音、角标、长连接、免打扰、分类、关键词
  --------------------------------------------------------------- */

// notificationSettingsSection 是「通知」一块。开关立即落盘、进程级共享；关键词
// 与免打扰时段分别点「保存关键词」「保存时段」才生效，避免边打字边把半截词写进
// 配置。这些偏好只管桌面弹窗与收消息的方式：消息列表与未读计数始终工作。
func (a *app) notificationSettingsSection(c *ui.Context) {
	p := a.notifPrefs()
	settingsSection(c, "通知", func() {
		settingsDescription(c, "这里只管桌面弹窗：关掉通知后消息仍会进通知窗口、未读数照常更新。"+
			"多账号共用这份设置，改完所有窗口立即生效。", 3)

		if v := settingsCheckbox(c, "桌面通知", p.notificationEnabled()); v != p.notificationEnabled() {
			a.saveNotifyPref(toastSettings, func() error { return p.setEnabled(v) })
		}
		if v := settingsCheckbox(c, "通知提示音", p.soundEnabled()); v != p.soundEnabled() {
			a.saveNotifyPref(toastSettings, func() error { return p.setSound(v) })
		}
		if v := settingsCheckbox(c, "显示未读角标", p.badgeEnabled()); v != p.badgeEnabled() {
			a.saveNotifyPref(toastSettings, func() error { return p.setBadge(v) })
		}
		if v := settingsCheckbox(c, "实时长连接", p.streamEnabled()); v != p.streamEnabled() {
			a.saveNotifyPref(toastSettings, func() error { return p.setStream(v) })
			// 落盘成功后立即切换：开着用 SSE，关掉改用一分钟一次的轮询，
			// 未读数与通知不会停。保存失败时偏好没变，这里不会误切。
			if p.streamEnabled() == v {
				a.applyNotificationStreamPref()
			}
		}
		settingsDescription(c, "实时长连接靠 SSE 推送新消息；关掉后改用定时轮询，"+
			"通知照常到达，只是没有那么即时。", 2)

		display.Text(c, "弹窗提醒的消息类型", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		})
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			for _, item := range notifyCategoryItems {
				enabled := p.categoryEnabled(item.Key)
				if v := settingsCheckbox(c, item.Label, enabled); v != enabled {
					key, on := item.Key, v
					a.saveNotifyPref(toastSettings, func() error { return p.setCategory(key, on) })
				}
			}
		})

		settingsField(c, "提醒关键词（留空则所有消息都提醒）", func() ui.Element {
			in := composeInput(c, &a.notifyKeywordDraft, field.InputProps{
				Placeholder: "按消息内容过滤弹窗", Size: lobetheme.Small,
			})
			in.Label("提醒关键词").MaxLength(notificationKeywordMax)
			return in
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "保存关键词", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.saveNotifyPref(toastSettings, func() error {
					return p.setKeyword(a.notifyKeywordDraft)
				})
			}
			if mode := settingsChoice(c, "关键词匹配",
				[]string{"包含时提醒", "包含时不提醒"}, keywordModeIndex(p.keywordIsBlacklist())); mode != keywordModeIndex(p.keywordIsBlacklist()) {
				a.saveNotifyPref(toastSettings, func() error { return p.setKeywordBlacklist(mode == 1) })
			}
		})

		if choice := settingsChoice(c, "每次事件最多弹",
			notifyBurstLabels(), p.burstChoice()); choice != p.burstChoice() {
			a.saveNotifyPref(toastSettings, func() error { return p.setBurst(choice) })
		}

		display.Text(c, "免打扰时段（只挡弹窗，消息照常收）", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		})
		if v := settingsCheckbox(c, "启用免打扰", p.quietOn()); v != p.quietOn() {
			a.saveNotifyPref(toastSettings, func() error { return p.setQuiet(v) })
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			settingsHalfField(c, "开始时间", func() ui.Element {
				in := composeInput(c, &a.notifyQuietFromDraft, field.InputProps{
					Placeholder: "22:00", Size: lobetheme.Small,
				})
				in.Label("免打扰开始时间").MaxLength(5)
				return in
			})
			settingsHalfField(c, "结束时间", func() ui.Element {
				in := composeInput(c, &a.notifyQuietToDraft, field.InputProps{
					Placeholder: "08:00", Size: lobetheme.Small,
				})
				in.Label("免打扰结束时间").MaxLength(5)
				return in
			})
		})
		if action.Button(c, "保存时段", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			from, to := a.notifyQuietFromDraft, a.notifyQuietToDraft
			switch {
			case !validQuietTime(from):
				a.toast(toastSettings, "开始时间要写成 HH:MM（例如 22:00）")
			case !validQuietTime(to):
				a.toast(toastSettings, "结束时间要写成 HH:MM（例如 08:00）")
			default:
				a.saveNotifyPref(toastSettings, func() error { return p.setQuietWindow(from, to) })
			}
		}
	})
}

func validQuietTime(raw string) bool {
	_, _, ok := parseQuietTime(raw)
	return ok
}

func keywordModeIndex(blacklist bool) int {
	if blacklist {
		return 1
	}
	return 0
}

func notifyBurstLabels() []string {
	labels := make([]string, len(notifyBurstChoices))
	for i, n := range notifyBurstChoices {
		labels[i] = fmt.Sprintf("%d 条", n)
	}
	return labels
}

// setCommentCostBarEnabled 开关评论框上方的费用状态栏。关掉后状态栏不再绘制，
// 也不再读取徽章与佩戴状态；字数退回工具条右端照常显示。
func (a *app) setCommentCostBarEnabled(on bool) {
	if a.commentCostPrefs == nil || a.commentCostPrefs.visible() == on {
		return
	}
	if err := a.commentCostPrefs.setVisible(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	// 开关是进程级共享状态：已经开着的评论框下一帧就据此重建，否则它们
	// 还停在上一帧（状态栏还在画 / 数字还没出现）。
	a.publish(func() {})
	if on {
		a.toast(toastSettings, "已开启金币消耗与徽章切换")
	} else {
		a.toast(toastSettings, "已关闭金币消耗与徽章切换")
	}
}

// setCommentCostOnSend 开关发送按钮上的消耗金币数；默认关。数字与状态栏共用
// 同一份估算，关掉徽章状态栏时也能单独开这一项。
func (a *app) setCommentCostOnSend(on bool) {
	if a.commentCostPrefs == nil || a.commentCostPrefs.onSend() == on {
		return
	}
	if err := a.commentCostPrefs.setOnSend(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	// 同上，让开着的评论框把发送按钮按新状态重建。
	a.publish(func() {})
	if on {
		a.toast(toastSettings, "发送按钮将显示消耗金币数")
	} else {
		a.toast(toastSettings, "发送按钮不再显示消耗金币数")
	}
}

/* ------------------------------------------------------------------
   图片缓存一块：统计、清理与自动过期
---------------------------------------------------------------- */

// cacheStatus 是设置窗口「图片缓存」一块的显示状态。统计与清理在后台跑，
// 结果通过 publish 回到主线程；字段加锁，因为无窗口测试里 publish 可能在
// 统计协程里直接执行。进行中的操作用 progress 说明；结果（成功或失败）改用
// 窗口内 toast 报告，面板里不再留一行结论文字。
type cacheStatus struct {
	mu       sync.Mutex
	gen      uint64 // 只采用最新一次统计的结果
	stats    imageCacheStats
	scanned  bool // 是否已经统计过一次
	busy     bool // 统计或清理正在进行
	progress string
}

// cacheStatusSnapshot 是渲染时读到的一份拷贝，避免持锁绘制。
type cacheStatusSnapshot struct {
	Stats    imageCacheStats
	Scanned  bool
	Busy     bool
	Progress string
}

func (s *cacheStatus) snapshot() cacheStatusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cacheStatusSnapshot{
		Stats:    s.stats,
		Scanned:  s.scanned,
		Busy:     s.busy,
		Progress: s.progress,
	}
}

// startCacheStats 在设置窗口打开时在后台统计一次缓存大小。
func (a *app) startCacheStats() {
	a.cacheStatus.mu.Lock()
	a.cacheStatus.gen++
	gen := a.cacheStatus.gen
	a.cacheStatus.busy = true
	a.cacheStatus.progress = ""
	a.cacheStatus.mu.Unlock()

	go func() {
		stats := a.images.Stats()
		a.publish(func() {
			a.cacheStatus.mu.Lock()
			if gen == a.cacheStatus.gen {
				a.cacheStatus.stats = stats
				a.cacheStatus.scanned = true
				a.cacheStatus.busy = false
			}
			a.cacheStatus.mu.Unlock()
		})
	}()
}

// clearImageCache 清空全部图片缓存：内存与磁盘一起清，下一次浏览时头像、
// 表情与配图都会重新下载。清理完成后刷新大小，结果用 toast 说明。
func (a *app) clearImageCache() {
	a.cacheStatus.mu.Lock()
	if a.cacheStatus.busy {
		a.cacheStatus.mu.Unlock()
		return
	}
	a.cacheStatus.gen++
	gen := a.cacheStatus.gen
	a.cacheStatus.busy = true
	a.cacheStatus.progress = "正在清理…"
	a.cacheStatus.mu.Unlock()

	go func() {
		freed, err := a.images.clear()
		a.publish(func() {
			result := ""
			a.cacheStatus.mu.Lock()
			if gen == a.cacheStatus.gen {
				a.cacheStatus.busy = false
				a.cacheStatus.scanned = true
				a.cacheStatus.stats = imageCacheStats{}
				a.cacheStatus.progress = ""
				switch {
				case err != nil:
					result = "清理缓存失败：" + err.Error()
				case freed.TotalFiles() == 0:
					result = "缓存已经是空的"
				default:
					result = fmt.Sprintf("已清理 %s（%d 个文件）",
						formatBytes(freed.TotalBytes()), freed.TotalFiles())
				}
			}
			a.cacheStatus.mu.Unlock()
			if result != "" {
				a.toast(toastSettings, result)
			}
		})
	}()
}

// imageCacheSection 是「图片缓存」一块：分两类显示占用大小，并提供清理与
// 重新统计按钮。头像与表情内容稳定、长期保留；帖子图片按 TTL 自动清理。
func (a *app) imageCacheSection(c *ui.Context) {
	s := a.cacheStatus.snapshot()

	settingsSection(c, "图片缓存", func() {
		settingsDescription(c, "头像与表情的内容几乎不变，长期缓存在本机；帖子里的外部图片 "+
			formatDurationCN(imageCacheTTL)+"后自动清理。", 3)

		if !s.Scanned {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if s.Busy {
					lobeSpin(c, feedback.SpinProps{CustomSize: 12})
				}
				display.Text(c, "正在统计缓存大小…", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
			})
		} else {
			ui.Column(c).Gap(4).Children(func() {
				cacheRow(c, "头像与表情", fmt.Sprintf("%s（%d 个文件）",
					formatBytes(s.Stats.StableBytes), s.Stats.StableFiles), false)
				cacheRow(c, "帖子图片", fmt.Sprintf("%s（%d 个文件）",
					formatBytes(s.Stats.TempBytes), s.Stats.TempFiles), false)
				cacheRow(c, "合计", formatBytes(s.Stats.TotalBytes()), true)
			})
		}

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "清理缓存", action.ButtonProps{}).Disabled(s.Busy).Clicked() {
				a.clearImageCache()
			}
			if action.Button(c, "重新统计", action.ButtonProps{}).Disabled(s.Busy).Clicked() {
				a.startCacheStats()
			}
			ui.Spacer(c).Grow(1)
		})

		// 结果走 toast，这里只说明正在做什么：进度是当前状态，不是结论。
		if s.Progress != "" {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{CustomSize: 12})
				display.Text(c, s.Progress, display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 2,
				})
			})
		}
	})
}

// cacheRow 是缓存统计的一行：左边字段名占固定宽度，右边是大小与文件数。
func cacheRow(c *ui.Context, label, value string, strong bool) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, label, display.TextProps{
			Strong: strong, FontSize: 12, Rows: 1,
		}).Width(90)
		display.Text(c, value, display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		})
	})
}

/* ------------------------------------------------------------------
   图片一块：默认不加载图片、动图自动播放
---------------------------------------------------------------- */

// imageSettingsSection 是「图片」一块：正文与评论里的配图怎么加载、动图放不
// 放。两枚开关都立即落盘、进程级共享，所有账号窗口下一帧就按新状态重建。
func (a *app) imageSettingsSection(c *ui.Context) {
	p := a.imagePrefs
	settingsSection(c, "图片", func() {
		if v := settingsCheckbox(c, "默认不加载图片", p.lazyLoad()); v != p.lazyLoad() {
			a.setLazyImages(v)
		}
		settingsDescription(c, "打开后配图先显示占位、点一下才下载；"+
			"头像、行内表情与徽章不受影响。", 2)
		if v := settingsCheckbox(c, "动图自动播放", p.autoAnimate()); v != p.autoAnimate() {
			a.setAnimateImages(v)
		}
		settingsDescription(c, "关掉后预览里的 gif / apng 只显示首帧。", 1)
	})
}

// setLazyImages 开关「默认不加载图片」。打开时立刻生效：所有窗口下一帧起把
// 配图画成占位；关掉时把「已点开」的记录清空，之后照常自动加载。
func (a *app) setLazyImages(on bool) {
	if a.imagePrefs == nil || a.imagePrefs.lazyLoad() == on {
		return
	}
	if err := a.imagePrefs.setLazy(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	if !on {
		a.revealedImages = nil
	}
	a.publish(func() {})
	if on {
		a.toast(toastSettings, "配图先显示占位，点一下才加载")
	} else {
		a.toast(toastSettings, "配图恢复自动加载")
	}
}

// setAnimateImages 开关动图自动播放。关掉时顺手关掉预览里已经在放的播放器，
// 免得它们在后台继续解码。
func (a *app) setAnimateImages(on bool) {
	if a.imagePrefs == nil || a.imagePrefs.autoAnimate() == on {
		return
	}
	if err := a.imagePrefs.setAnimate(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	if !on {
		if v := a.viewer; v != nil {
			v.closePlayers()
		}
	}
	a.publish(func() {})
	a.redrawViewerWindow()
	if on {
		a.toast(toastSettings, "动图将在预览里播放")
	} else {
		a.toast(toastSettings, "动图只显示首帧")
	}
}
