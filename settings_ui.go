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
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 设置窗口的尺寸：比代理弹窗宽一些，放得下「代理 / 图库 / 缓存 / 更新 / 窗口 / 外观」几页。
const (
	settingsWindowWidth  = 520
	settingsWindowHeight = 700
	settingsWindowMinW   = 440
	settingsWindowMinH   = 420
)

// 设置窗口按分类分页：每块内容各占一页，不再堆成一条要滚很久的长页面。
// 标签固定在内容区上方；每页的草稿都绑在 app 字段上，切页不会丢。外观页排
// 在最后，原有几页的序号保持不变，默认打开仍是网络代理。
const (
	settingsTabProxy = iota
	settingsTabHosts
	settingsTabCache
	settingsTabUpdates
	settingsTabWindow
	settingsTabAppearance
)

var settingsTabLabels = []string{"网络代理", "图库", "图片缓存", "更新", "窗口", "外观"}

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
	a.settingsTab = settingsTabProxy
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("设置"),
		Width:             settingsWindowWidth,
		Height:            settingsWindowHeight,
		MinWidth:          settingsWindowMinW,
		MinHeight:         settingsWindowMinH,
		StateKey:          "settings",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.settingsWindowView),
	})
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
	a.drainToasts(c, a.claimsToasts(toastSettings))
	// Esc 直接关掉设置窗口，和系统偏好设置的直觉一致。
	c.OnShortcut(0, ui.KeyEscape, a.closeSettingsWindow)

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
				case settingsTabHosts:
					a.imageHostsSection(c)
				case settingsTabCache:
					a.imageCacheSection(c)
				case settingsTabUpdates:
					a.updateSettingsSection(c)
				case settingsTabWindow:
					a.windowSettingsSection(c)
				case settingsTabAppearance:
					a.appearanceSettingsSection(c)
				default:
					a.proxySettingsSection(c)
				}
			})
		})
		list.Grow(1).MinHeight(0)
	})
	// 设置里也有带令牌的请求（例如官方图床测试）：缺权限时弹在这里。
	a.authDialogFor(c, toastSettings)
}

// settingsSection 画一块带标题的 Lobe 卡片，各分类共用同一套版式。描边、圆角
// 与底色来自 layout.Block，标题走 display.Text。
func settingsSection(c *ui.Context, title string, body func()) {
	card := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
		FillWidth().Padding(14, 14)
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

/* ------------------------------------------------------------------
   更新一块：检查更新
---------------------------------------------------------------- */

// updateSettingsSection 是「更新」一块：应用每天在后台自动检查一次，这里给
// 想立即确认版本的人一个入口。
func (a *app) updateSettingsSection(c *ui.Context) {
	settingsSection(c, "更新", func() {
		settingsDescription(c, "应用每天自动检查一次 GitHub Release；检查更新和下载更新包都会走「网络代理」里设置的代理。现在想确认版本可以手动检查，更新窗口会说明结果或列出新版本。", 3)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "检查更新", action.ButtonProps{}).Clicked() {
				a.checkForUpdates()
			}
			ui.Spacer(c).Grow(1)
		})
	})
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
					feedback.Spin(c, feedback.SpinProps{CustomSize: 12}, nil)
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
				feedback.Spin(c, feedback.SpinProps{CustomSize: 12}, nil)
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
