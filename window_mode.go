package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 关闭主窗口时的行为：默认每次询问，用户可以选「退出应用」或「收进托盘」，
// 并把选择记到配置目录的 window.json 里，下次启动沿用。记住之后不再询问，
// 想改回来就在设置窗口的「窗口」一块里选。
const (
	closeAsk    = iota // 每次关闭都询问
	closeToQuit        // 关闭即退出
	closeToTray        // 收进托盘，应用继续运行
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
		return closeToQuit
	case "tray":
		return closeToTray
	default:
		return closeAsk
	}
}

// modeName 是写进 window.json 的值，和 parseCloseMode 一一对应。
func (s *closeModeStore) modeName() string {
	switch s.mode {
	case closeToQuit:
		return "quit"
	case closeToTray:
		return "tray"
	default:
		return "ask"
	}
}

// label 是给界面看的中文名称。
func (s *closeModeStore) label() string {
	switch s.mode {
	case closeToQuit:
		return "退出应用"
	case closeToTray:
		return "收进托盘"
	default:
		return "每次询问"
	}
}

// Save 记住选择并落盘。写失败时本次运行仍按新选择处理，只把错误交给调用
// 方提示；下次启动会回到旧配置。
func (s *closeModeStore) Save(mode int) error {
	switch mode {
	case closeToQuit, closeToTray:
	default:
		mode = closeAsk
	}
	s.mode = mode
	if err := writeJSONFile(filepath.Join(s.dir, "window.json"), windowConfig{Close: s.modeName()}); err != nil {
		return fmt.Errorf("保存关闭行为失败：%w", err)
	}
	return nil
}

// hideOnClose 报告现在关主窗口该不该直接收进托盘。托盘不可用（创建失败或
// 平台不支持）时不能收：那样窗口会藏起来，留下一个既没有图标也点不回来的
// 进程，所以退回关闭窗口。正在退出时也不能拦：那会把一次真正的退出变成隐藏。
func (a *app) hideOnClose() bool {
	return a.closeMode.mode == closeToTray && a.trayReady && !a.quitting
}

// askOnClose 报告现在关主窗口该不该先问一句。只在托盘可用时问：没有托盘，
// 「收进托盘」这个选项本身就不成立。
func (a *app) askOnClose() bool {
	return a.closeMode.mode == closeAsk && a.trayReady && !a.quitting
}

// onMainWindowClose 挂在主窗口的 OnClose 上：按设置决定拦下这次关闭（收进
// 托盘或先问一句），还是让它照常关掉（最后一个窗口关闭时应用退出）。只在
// 主线程调用。
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

// chooseCloseMode 是关闭提示里的选择：记住与否先落盘，再按选择退出或收起
// 窗口。选择本身用户刚在弹窗里点过，所以不再额外发一条 toast。
func (a *app) chooseCloseMode(mode int) {
	a.closePromptOpen = false
	if a.closeRemember {
		if err := a.closeMode.Save(mode); err != nil {
			a.toast(toastMain, err.Error())
		}
	}
	if mode == closeToTray {
		a.hideMainWindowToTray()
		return
	}
	a.quitApp()
}

// hideMainWindowToTray 收起主窗口与它的附属窗口。主窗口只 Hide 不 Close：
// 窗口对象还在，应用才不会因为「最后一个窗口关了」而退出，点托盘图标就能
// 原样回来。附属窗口是主窗口的一部分，留着它们等于留了一屏孤岛。
//
// 桌面宠物（pet.go）不在这份名单里：它不是主窗口的一部分，而是「主窗口收起
// 之后仍然留在桌面上」的那一个。把它一起收掉，用户选了「收进托盘」就等于
// 连宠物也没了。
func (a *app) hideMainWindowToTray() {
	win := a.win
	if win == nil {
		return
	}
	win.Hide()
	a.closeNotificationWindow()
	a.closeSettingsWindow()
	a.closeProfileWindow()
	a.closeCoinWindow()
	a.closeComposeWindow()
	a.closeUserCardWindow()
	a.closeSearchWindow()
}

// closePromptDialog 是关闭主窗口时的询问框。放在主窗口里而不是另开一个
// 窗口：它只在这次关闭时用一下，开新窗口反而会挡住主窗口的样子。
func (a *app) closePromptDialog(c *ui.Context) {
	if !a.closePromptOpen {
		return
	}
	t := c.Theme()
	ui.Modal(c, &a.closePromptOpen, func() {
		ui.Column(c).Width(430).Gap(10).Children(func() {
			ui.Text(c, "关闭主窗口").FontSize(16).Bold()
			ui.Text(c, "要让 T2 Desktop 继续在系统托盘里运行，还是现在退出？"+
				"收进托盘后应用仍在后台接收通知，点托盘图标就能回到窗口。").
				TextColor(t.TextMuted).MaxLines(3)
			ui.Checkbox(c, &a.closeRemember, "记住我的选择，可在设置里修改")
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "退出应用").Clicked() {
					a.chooseCloseMode(closeToQuit)
				}
				if ui.PrimaryButton(c, "收进托盘").Clicked() {
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
