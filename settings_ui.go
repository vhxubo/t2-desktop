package main

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
)

// 设置窗口的尺寸：比代理弹窗宽一些，放得下「代理 / 更新 / 图片缓存 / 图库」几块。
const (
	settingsWindowWidth  = 520
	settingsWindowHeight = 700
	settingsWindowMinW   = 440
	settingsWindowMinH   = 420
)

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
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "设置",
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
	win.OnClosed(func() { a.settingsWin = nil })
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
---------------------------------------------------------------- */

// settingsWindowView 是设置窗口的 view。窗口自己带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) settingsWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	c.Root().Background(t.Background)
	// 保存代理、清理缓存的结果以 toast 显示在这个窗口里。
	a.drainToasts(c, a.claimsToasts(toastSettings))
	// Esc 直接关掉设置窗口，和系统偏好设置的直觉一致。
	c.OnShortcut(0, ui.KeyEscape, a.closeSettingsWindow)

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角是最小化与关闭，按钮与主窗口同一套画法。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Text(c, "设置").FontSize(13).Bold().SingleLine().TextColor(t.Text)
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.settingsWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", func() { a.closeSettingsWindow() })
		})
		ui.Divider(c)

		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(18, 20).Gap(16).Children(func() {
				a.proxySettingsSection(c)
				ui.Divider(c)
				a.updateSettingsSection(c)
				ui.Divider(c)
				a.imageCacheSection(c)
				ui.Divider(c)
				a.imageHostsSection(c)
				ui.Divider(c)
				a.windowSettingsSection(c)
				ui.Divider(c)
				a.experimentSettingsSection(c)
			})
		})
	})
}

// settingsSection 画一块带标题的设置区域，三块内容共用同一套版式。
func settingsSection(c *ui.Context, title string, body func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13).Bold().TextColor(t.Text)
		body()
	})
}

// updateSettingsSection 是「更新」一块：应用每天在后台自动检查一次，这里给
// 想立即确认版本的人一个入口。
func (a *app) updateSettingsSection(c *ui.Context) {
	t := c.Theme()
	settingsSection(c, "更新", func() {
		ui.Text(c, "应用每天自动检查一次 GitHub Release。现在想确认版本可以手动检查，更新窗口会说明结果或列出新版本。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "检查更新").Clicked() {
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
	t := c.Theme()
	settingsSection(c, "窗口", func() {
		ui.Text(c, "收进托盘后应用继续在后台接收通知，点托盘图标回到窗口；"+
			"要从托盘菜单的「退出」或系统的退出命令才会真正结束。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)
		if ui.Segmented(c, &a.closeMode.mode, "每次询问", "退出应用", "收进托盘").
			Label("关闭主窗口时").Changed() {
			a.setCloseMode(a.closeMode.mode)
		}
		if !a.trayReady {
			ui.Text(c, "系统托盘当前不可用，关闭主窗口仍会退出应用。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		}
	})
}

/* ------------------------------------------------------------------
   实验功能一块：默认关闭，开启后可能有不稳定
---------------------------------------------------------------- */

// experimentSettingsSection 是「实验功能」一块：每一项都是独立的实验开关，
// 默认关闭。当前只有桌面宠物这一项——开启后它才会出现在托盘菜单里，并按上
// 次的选择放出来。结果用窗口内 toast 说明，这一块只留出「正在做什么」的余地。
func (a *app) experimentSettingsSection(c *ui.Context) {
	t := c.Theme()
	// 每次重建都从实验开关同步一次，保证勾选项的勾选状态跟上落盘的真值。
	a.expPetOn = a.experiment.petOn()
	settingsSection(c, "实验功能", func() {
		ui.Text(c, "以下功能属于实验性质，默认关闭；开启后可能不稳定，仅供提前试用。如有问题，回到这里关掉即可恢复原有行为。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		if ui.Checkbox(c, &a.expPetOn, "桌面宠物（实验功能）").Changed() {
			a.setPetExperiment(a.expPetOn)
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
	t := c.Theme()
	s := a.cacheStatus.snapshot()

	settingsSection(c, "图片缓存", func() {
		ui.Text(c, "头像与表情的内容几乎不变，长期缓存在本机；帖子里的外部图片 "+
			formatDurationCN(imageCacheTTL)+"后自动清理。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)

		if !s.Scanned {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if s.Busy {
					ui.Spinner(c).Size(12, 12)
				}
				ui.Text(c, "正在统计缓存大小…").FontSize(12).TextColor(t.TextMuted)
			})
		} else {
			ui.Column(c).Gap(2).Children(func() {
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, "头像与表情").FontSize(12).Width(90)
					ui.Text(c, fmt.Sprintf("%s（%d 个文件）",
						formatBytes(s.Stats.StableBytes), s.Stats.StableFiles)).
						FontSize(12).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, "帖子图片").FontSize(12).Width(90)
					ui.Text(c, fmt.Sprintf("%s（%d 个文件）",
						formatBytes(s.Stats.TempBytes), s.Stats.TempFiles)).
						FontSize(12).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, "合计").FontSize(12).Width(90).Bold()
					ui.Text(c, formatBytes(s.Stats.TotalBytes())).
						FontSize(12).TextColor(t.TextMuted)
				})
			})
		}

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "清理缓存").Disabled(s.Busy).Clicked() {
				a.clearImageCache()
			}
			if ui.Button(c, "重新统计").Disabled(s.Busy).Clicked() {
				a.startCacheStats()
			}
			ui.Spacer(c).Grow(1)
		})

		// 结果走 toast，这里只说明正在做什么：进度是当前状态，不是结论。
		if s.Progress != "" {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, s.Progress).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
			})
		}
	})
}
