package main

import (
	"github.com/egoist/mygo/ui"
)

// openProxyDialog 从已保存的配置复制一份草稿再打开面板。系统/环境代理可能
// 在两次打开之间被其它工具改掉，所以顺便重新探测一次。
func (a *app) openProxyDialog() {
	a.proxy.Reload()
	cfg, _ := a.proxy.Snapshot()
	a.proxyMode = cfg.Mode
	a.proxyURL = cfg.URL
	a.proxyUser = cfg.Username
	a.proxyPass = cfg.Password
	a.proxyNoProxy = cfg.NoProxy
	a.proxyMsg = ""
	a.proxyMsgErr = false
	a.proxyOpen = true
}

// proxyDialog 是标题栏「代理」按钮打开的弹窗。用 Modal 而不是整页，
// 是因为它只是一个本机网络设置，不该打断正在浏览的列表或正文。
func (a *app) proxyDialog(c *ui.Context) {
	if !a.proxyOpen {
		return
	}
	t := c.Theme()
	_, status := a.proxy.Snapshot()

	ui.Modal(c, &a.proxyOpen, func() {
		ui.Column(c).Width(440).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "网络代理").FontSize(16).Bold()
				ui.Spacer(c).Grow(1)
				a.titleTextButton(c, "关闭面板", func() { a.proxyOpen = false })
			})
			ui.Text(c, "API、通知长连接、图片与登录请求都使用这里的代理。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(2)

			if ui.Segmented(c, &a.proxyMode, "跟随系统", "手动代理", "直连").
				Label("代理模式").FillWidth().Changed() {
				a.proxyMsg = ""
				a.proxyMsgErr = false
			}

			switch a.proxyMode {
			case proxyManual:
				a.manualProxyFields(c)
			case proxyDirect:
				ui.Text(c, "所有请求都直接连接，不使用系统代理或手动代理。").
					FontSize(12).TextColor(t.TextMuted).MaxLines(2)
			default:
				ui.Text(c, "优先按 HTTP(S)_PROXY / ALL_PROXY 环境变量；否则读取系统代理（Windows 的 Internet 设置、macOS 的网络设置或 Linux 的 GIO），含 PAC / 自动检测。").
					FontSize(12).TextColor(t.TextMuted).MaxLines(3)
				if ui.Button(c, "重新检测").Clicked() {
					a.proxy.Reload()
					a.proxyMsg = "已重新检测系统代理"
					a.proxyMsgErr = false
				}
			}

			ui.Text(c, "当前生效："+status).FontSize(11).TextColor(t.TextMuted).MaxLines(2)

			if a.proxyMsg != "" {
				msg := ui.Text(c, a.proxyMsg).FontSize(12).MaxLines(3)
				if a.proxyMsgErr {
					msg.TextColor(t.Danger)
				} else {
					msg.TextColor(t.Success)
				}
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					a.proxyOpen = false
				}
				if ui.PrimaryButton(c, "保存").Clicked() {
					a.saveProxySettings()
				}
			})
		})
	})
}

// manualProxyFields 只画手动模式需要的输入框，切换模式时整块消失。
func (a *app) manualProxyFields(c *ui.Context) {
	t := c.Theme()
	ui.TextInput(c, &a.proxyURL).Label("代理地址").
		Placeholder("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080").FillWidth()
	ui.Row(c).Gap(8).Children(func() {
		ui.TextInput(c, &a.proxyUser).Label("用户名（可选）").Grow(1).MinWidth(0)
		ui.TextInput(c, &a.proxyPass).Label("密码（可选）").Password().Grow(1).MinWidth(0)
	})
	ui.TextInput(c, &a.proxyNoProxy).Label("不走代理的地址").
		Placeholder("localhost, .corp.example.com").FillWidth()
	ui.Text(c, "多个地址用逗号分隔；省略协议时按 http:// 处理。").
		FontSize(11).TextColor(t.TextMuted).MaxLines(2)
}

// saveProxySettings 校验、落盘并立即生效。保存成功后不关弹窗：让用户看到
// 「当前生效」已经变成新地址，确认后再关闭。
func (a *app) saveProxySettings() {
	cfg := proxyConfig{
		Mode:     a.proxyMode,
		URL:      a.proxyURL,
		Username: a.proxyUser,
		Password: a.proxyPass,
		NoProxy:  a.proxyNoProxy,
	}
	if err := a.proxy.Save(cfg); err != nil {
		a.proxyMsg = err.Error()
		a.proxyMsgErr = true
		return
	}
	a.proxyMsg = "已保存，新请求立即生效"
	a.proxyMsgErr = false
	// 通知长连接按建立时的代理走；重启一次才会换到新线路。
	if a.auth.loggedIn() {
		a.stopNotificationWatch()
		a.startNotificationWatch()
	}
}
