package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// 关闭主窗口时的行为：默认每次询问，用户可以选「收进托盘」或「退出应用」，
// 并把选择记到配置目录的 window.json 里，下次启动沿用。记住之后不再询问，
// 想改回来就在设置窗口的「窗口」一块里选。
//
// 「收进托盘」把窗口藏起来，应用继续在后台跑；「退出应用」关掉所有窗口并
// 结束整个应用。以前分开的「关闭窗口」与「退出应用」结果一样（只有一个主
// 窗口时关掉它就退出），已合并为「退出应用」。
const (
	closeAsk    = iota // 每次关闭都询问
	closeToTray        // 收进托盘，应用继续运行
	closeApp           // 关窗口即退出整个应用
)

// windowConfig 是持久化到 window.json 的内容。用字符串而不是数字，是为了
// 让配置文件可读、可手改；无法识别的值按「每次询问」处理。
type windowConfig struct {
	Close string `json:"close"`
}

// closeModeStore 保存「关闭主窗口时做什么」这一个偏好。只在主线程读写。
type closeModeStore struct {
	dir  string
	mode int
}

func newCloseModeStore(dir string) *closeModeStore {
	if dir == "" {
		dir = configDir()
	}
	s := &closeModeStore{dir: dir, mode: closeAsk}
	var cfg windowConfig
	if err := readJSONFile(filepath.Join(dir, "window.json"), &cfg); err == nil {
		s.mode = parseCloseMode(cfg.Close)
	}
	return s
}

// parseCloseMode 把配置文件里的值转成模式序号；空值、旧版本残留与拼错的
// 值都退回「每次询问」，让人重新做一次选择，而不是猜一个。
func parseCloseMode(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quit":
		// 旧版本的 "quit" 与合并后的「退出应用」是同一件事，继续认。
		return closeApp
	case "tray":
		return closeToTray
	case "app-quit":
		return closeApp
	default:
		return closeAsk
	}
}

// modeName 是写进 window.json 的值，和 parseCloseMode 一一对应。
func (s *closeModeStore) modeName() string {
	switch s.mode {
	case closeToTray:
		return "tray"
	case closeApp:
		return "app-quit"
	default:
		return "ask"
	}
}

// label 是给界面看的中文名称。
func (s *closeModeStore) label() string {
	switch s.mode {
	case closeToTray:
		return "收进托盘"
	case closeApp:
		return "退出应用"
	default:
		return "每次询问"
	}
}

// Save 记住选择并落盘。写失败时本次运行仍按新选择处理，只把错误交给调用
// 方提示；下次启动会回到旧配置。
func (s *closeModeStore) Save(mode int) error {
	switch mode {
	case closeToTray, closeApp:
	default:
		mode = closeAsk
	}
	s.mode = mode
	if err := writeJSONFile(filepath.Join(s.dir, "window.json"), windowConfig{Close: s.modeName()}); err != nil {
		return fmt.Errorf("保存关闭行为失败：%w", err)
	}
	return nil
}

// Reset 恢复默认关闭行为（每次询问）并落盘。
func (s *closeModeStore) Reset() error { return s.Save(closeAsk) }

// trayIsReady 报告托盘现在到底能不能用。托盘是进程级资源，由 desktop 在
// 应用就绪后才创建（晚于 app 构建），而且退出时会销毁，所以必须实时查进程级
// 单例，不能拿构建时的快照 —— 那样「收进托盘」永远不生效。无头测试没有
// desktop，托盘可用性由测试直接写在 a.trayReady 上。
func (a *app) trayIsReady() bool {
	if a.desktop != nil {
		return a.desktop.trayIsReady()
	}
	return a.trayReady
}

// hideOnClose 报告现在关主窗口该不该直接收进托盘。托盘不可用（创建失败或
// 平台不支持）时不能收：那样窗口会藏起来，留下一个既没有图标也点不回来的
// 进程，所以按普通关闭处理。正在退出时也不能拦：那会把一次真正的退出变成隐藏。
func (a *app) hideOnClose() bool {
	return a.closeMode.mode == closeToTray && a.trayIsReady() && !a.quitting
}

// askOnClose 报告现在关主窗口该不该先问一句。只在托盘可用时问：没有托盘，
// 「收进托盘」这个选项本身就不成立。
func (a *app) askOnClose() bool {
	return a.closeMode.mode == closeAsk && a.trayIsReady() && !a.quitting
}

// onMainWindowClose 挂在主窗口的 OnClose 上：按设置决定拦下这次关闭（收进
// 托盘、先问一句，或结束整个应用），还是让它照常关掉。只在主线程调用。
func (a *app) onMainWindowClose(e *mygo.CloseEvent) {
	switch {
	case a.quitting:
		return
	case a.hideOnClose():
		e.PreventDefault()
		a.hideMainWindowToTray()
	case a.askOnClose():
		e.PreventDefault()
		a.openClosePrompt()
	case a.closeMode.mode == closeApp:
		e.PreventDefault()
		a.quitApp()
	}
}

// openClosePrompt 拦住这次关闭，在主窗口里弹出「退出还是收进托盘」。没记住
// 之前每次关闭都会再问一次。
func (a *app) openClosePrompt() {
	a.closePromptOpen = true
	a.closeRemember = false
	if a.win != nil {
		a.win.Invalidate()
	}
}

// chooseCloseMode 是关闭提示里的选择：记住与否先落盘，再按选择收起窗口或
// 退出应用。选择本身用户刚在弹窗里点过，所以不再额外发一条 toast。
func (a *app) chooseCloseMode(mode int) {
	a.closePromptOpen = false
	if a.closeRemember {
		if err := a.closeMode.Save(mode); err != nil {
			a.toast(toastMain, err.Error())
		}
	}
	switch mode {
	case closeToTray:
		a.hideMainWindowToTray()
	case closeApp:
		a.quitApp()
	}
}

// hideNativeWindow 是一扇窗口的原生隐藏动作。单独一层让无头测试只验证窗口
// 选择，不碰 mygo 的主循环。
var hideNativeWindow = func(w *mygo.Window) { w.Hide() }

// hideMainWindowToTray 只收起主窗口。每扇 mygo 窗口两两独立：收起主窗口不
// 关其它窗口，它们照常留在屏幕上；主窗口只 Hide 不 Close，窗口对象还在，
// 应用才不会因为「最后一个窗口关了」而退出，点托盘图标就能原样回来。
func (a *app) hideMainWindowToTray() {
	if win := a.win; win != nil {
		hideNativeWindow(win)
	}
}

// hideAllWindowsToTray 是托盘「关闭全部」：主窗口 Hide，其余每扇窗口都关掉，
// 屏幕上什么都不留，只留一个托盘图标。评分榜与稍后再看是主窗口内嵌页，随主
// 窗口一起收起，没有独立窗口要关。只在主线程调用。
func (a *app) hideAllWindowsToTray() {
	a.hideMainWindowToTray()
	a.closeAuxiliaryWindows()
}

// closeAllWindowsToTray 是托盘「关闭全部」的动作。主窗口只 Hide 不 Destroy，
// 窗口对象还在，应用不会因为「最后一个窗口关了」退出；点托盘图标就能回来。
// 只在主线程调用（托盘菜单的 Click 回调本就在主线程）。
func (b *desktop) closeAllWindowsToTray() {
	a := b.app
	if a == nil {
		return
	}
	// 主窗口关着、只剩详情等附属窗口时，先开一扇主窗口当锚点。否则下面关掉
	// 最后一扇附属窗口就会退出应用 —— 那就和「退出」重复了。
	if a.mainClosed || a.win == nil {
		a.openMainWindow()
	}
	a.hideAllWindowsToTray()
}

// closePromptDialog 是关闭主窗口时的询问框。放在主窗口里而不是另开一个
// 窗口：它只在这次关闭时用一下，开新窗口反而会挡住主窗口的样子。两个选项
// 并列，Lobe Modal 自带的两按钮页脚是固定的确定/取消，所以用 NoFooter 自己
// 排一行按钮。
func (a *app) closePromptDialog(c *ui.Context) {
	if !a.closePromptOpen {
		return
	}
	t := c.Theme()
	feedback.Modal(c, &a.closePromptOpen, feedback.ModalProps{
		Title:    "关闭主窗口",
		Width:    430,
		NoFooter: true,
	}, func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "收进托盘则应用继续在后台接收通知，点托盘图标就能回到窗口；"+
				"退出应用会关掉所有窗口。").
				TextColor(t.TextMuted).MaxLines(3)
			ui.Checkbox(c, &a.closeRemember, "记住我的选择，可在设置里修改")
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if action.Button(c, "退出应用", action.ButtonProps{}).Clicked() {
					a.chooseCloseMode(closeApp)
				}
				if action.Button(c, "收进托盘", action.ButtonProps{
					Variant: action.Primary,
				}).Clicked() {
					a.chooseCloseMode(closeToTray)
				}
			})
		})
	})
}

// setCloseMode 是设置窗口里的选择：记住并落盘，结果用窗口内 toast 说明。
func (a *app) setCloseMode(mode int) {
	if err := a.closeMode.Save(mode); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.toast(toastSettings, "关闭主窗口时："+a.closeMode.label())
}
