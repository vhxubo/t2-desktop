package main

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
)

// reloadProxyDraft 从已保存的配置复制一份草稿，供设置窗口编辑。系统/环境
// 代理可能在两次打开之间被其它工具改掉，所以顺便重新探测一次。
func (a *app) reloadProxyDraft() {
	a.proxy.Reload()
	cfg, _ := a.proxy.Snapshot()
	a.proxyMode = cfg.Mode
	a.proxyURL = cfg.URL
	a.proxyUser = cfg.Username
	a.proxyPass = cfg.Password
	a.proxyNoProxy = cfg.NoProxy
}

// draftProxyConfig 是设置窗口此刻填着的代理草稿，可能还没保存。
func (a *app) draftProxyConfig() proxyConfig {
	return proxyConfig{
		Mode:     a.proxyMode,
		URL:      a.proxyURL,
		Username: a.proxyUser,
		Password: a.proxyPass,
		NoProxy:  a.proxyNoProxy,
	}.normalized()
}

// proxyCheckStatus 只记「正在测试」的进度：结果（成功或失败）由窗口内
// toast 报告，页面里不留结论文字。发布结果的协程与主线程可能同时碰它，
// 所以加锁（无头测试里 publish 会直接在协程里执行）。
type proxyCheckStatus struct {
	mu    sync.Mutex
	gen   uint64
	busy  bool
	label string
}

// begin 占住测试状态；已经有测试在跑就返回 false，不并发发请求。
func (s *proxyCheckStatus) begin(label string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return 0, false
	}
	s.gen++
	s.busy = true
	s.label = label
	return s.gen, true
}

// finish 只让最新一次测试交回结果，迟到的结果不覆盖新测试。
func (s *proxyCheckStatus) finish(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || !s.busy {
		return false
	}
	s.busy = false
	s.label = ""
	return true
}

func (s *proxyCheckStatus) snapshot() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy, s.label
}

// testProxyConnectivity 用草稿里的配置（不必先保存）发一次公开读接口请求，
// 确认这条线路能不能连到 2Libra。请求在后台跑，进度留在面板里，结论用
// toast 说明；同一时刻只测一次。
func (a *app) testProxyConnectivity() {
	cfg := a.draftProxyConfig()
	if err := cfg.validate(); err != nil {
		a.toast(toastSettings, "代理配置无效："+err.Error())
		return
	}
	_, via := resolveProxy(cfg)
	gen, ok := a.proxyCheck.begin(via)
	if !ok {
		return
	}
	target := proxyCheckTarget
	go func() {
		result := a.proxy.Check(cfg, target)
		a.publish(func() {
			if a.proxyCheck.finish(gen) {
				a.toast(toastSettings, result.Message())
			}
		})
	}()
}

// proxySettingsSection 是设置窗口里的「网络代理」一块。它原来是一个弹窗；
// 现在和检查更新、图片缓存放在同一个设置窗口里，改完保存即生效，不关窗口。
// 保存与重新检测的结果用窗口内 toast 报告，面板里不留提示文字。
func (a *app) proxySettingsSection(c *ui.Context) {
	_, status := a.proxy.Snapshot()
	busy, via := a.proxyCheck.snapshot()

	settingsSection(c, "网络代理", func() {
		settingsDescription(c, "API、通知长连接、图片与登录请求都使用这里的代理；"+
			"填完可以点「测试连通性」当场验证，不必先保存。", 2)

		// 模式切换立即改变下面的字段：一整行的 Lobe 分段选择。
		a.proxyMode = settingsChoice(c, "代理模式",
			[]string{"跟随系统", "手动代理", "直连"}, a.proxyMode)

		switch a.proxyMode {
		case proxyManual:
			a.manualProxyFields(c)
		case proxyDirect:
			settingsDescription(c, "所有请求都直接连接，不使用系统代理或手动代理。", 2)
		default:
			settingsDescription(c, "优先按 HTTP(S)_PROXY / ALL_PROXY 环境变量；否则读取系统代理（Windows 的 Internet 设置、macOS 的网络设置或 Linux 的 GIO），含 PAC / 自动检测。", 3)
			ui.Row(c).Children(func() {
				if action.Button(c, "重新检测", action.ButtonProps{}).Clicked() {
					a.proxy.Reload()
					a.toast(toastSettings, "已重新检测系统代理")
				}
			})
		}

		display.Text(c, "当前生效："+status, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 2,
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "测试连通性", action.ButtonProps{}).Disabled(busy).
				Tooltip("用上面填写的代理访问 2Libra 公开接口，确认线路可用；不会改动已保存的配置").
				Clicked() {
				a.testProxyConnectivity()
			}
			// 网络调试：打开进程级的请求记录窗口，排查「发了什么、回了什么」。
			if action.Button(c, "网络调试", action.ButtonProps{}).
				Tooltip("打开网络调试窗口，查看应用发出的每个请求的参数与响应").
				Clicked() {
				a.showNetLog()
			}
			ui.Spacer(c).Grow(1)
			if action.Button(c, "保存代理设置", action.ButtonProps{Variant: action.Primary}).Clicked() {
				a.saveProxySettings()
			}
		})
		// 结果走 toast，面板里只留「正在做」的进度。
		if busy {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{CustomSize: 12})
				display.Text(c, "正在测试「"+via+"」到 2Libra 的连通性…", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 2,
				})
			})
		}
	})
}

// manualProxyFields 只画手动模式需要的输入框，切换模式时整块消失。字段用 Lobe
// 的 field.Input：可见标签由 settingsField 画，控件的 Label 供读屏与测试命中。
func (a *app) manualProxyFields(c *ui.Context) {
	settingsField(c, "代理地址", func() ui.Element {
		return field.Input(c, &a.proxyURL, field.InputProps{
			Placeholder: "http://127.0.0.1:7890 或 socks5://127.0.0.1:1080",
		}).Label("代理地址")
	})
	ui.Row(c).Gap(8).Children(func() {
		settingsHalfField(c, "用户名（可选）", func() ui.Element {
			return field.Input(c, &a.proxyUser, field.InputProps{}).Label("用户名（可选）")
		})
		settingsHalfField(c, "密码（可选）", func() ui.Element {
			return field.InputPassword(c, &a.proxyPass, field.InputProps{}).Label("密码（可选）")
		})
	})
	settingsField(c, "不走代理的地址", func() ui.Element {
		return field.Input(c, &a.proxyNoProxy, field.InputProps{
			Placeholder: "localhost, .corp.example.com",
		}).Label("不走代理的地址")
	})
	display.Text(c, "多个地址用逗号分隔；省略协议时按 http:// 处理。",
		display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
}

// saveProxySettings 校验、落盘并立即生效。成功后不关窗口：让用户看到
// 「当前生效」已经变成新地址，确认后再关闭设置。地址不合法时同样不关窗口，
// 用 toast 说明原因，草稿原样留在输入框里等用户改。
func (a *app) saveProxySettings() {
	if err := a.proxy.Save(a.draftProxyConfig()); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.toast(toastSettings, "已保存，新请求立即生效")
	// 通知长连接按建立时的代理走；重启一次才会换到新线路。
	if a.auth.loggedIn() {
		a.stopNotificationWatch()
		a.startNotificationWatch()
	}
}
