package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 设置窗口里的「图库（图床）」一块。
//
// 内置预设（官方 / SM.MS / Imgur / 兰空 / 自定义）默认摆在这里，可以直接改
// 字段、停用或启用，但不能删除；用户自己加的项可以改也可以删。所有编辑直接
// 落在 a.hosts 上（同一份内存配置也是编辑器和上传队列读的），点「保存」才
// 校验并写入 localKV 的 imagehosts:v1，没保存就关窗口不会生效到下次启动。

// imageHostsSection 一次只展开一条图库的字段：设置窗口只有 520 宽，把八条
// 预设的所有输入框全铺开会变成一屏填不完的表单。
func (a *app) imageHostsSection(c *ui.Context) {
	t := c.Theme()
	settingsSection(c, "图库（图床）", func() {
		ui.Text(c, "写帖子或评论时拖入、选择、粘贴的图片会传到「当前使用」的图库。"+
			"Kappa / Catbox 这类图床免注册、免 Token；2Libra 官方图床只对 Pro 会员开放。"+
			"免费图床变动频繁，可以点下面的「测试这个图床」确认还能不能用。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		a.imageHostActivePicker(c)
		ui.Divider(c)
		a.imageHostEditor(c)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "添加自定义图库").Clicked() {
				a.addCustomImageHost()
			}
			if ui.Button(c, "恢复默认").Clicked() {
				a.resetImageHosts()
			}
			if ui.PrimaryButton(c, "保存图库设置").Clicked() {
				if err := a.saveImageHosts(); err != nil {
					a.toast(toastSettings, err.Error())
				} else {
					a.toast(toastSettings, "图库设置已保存")
				}
			}
		})
	})
}

// imageHostActivePicker 只在已启用的图库里选「当前使用」，切换立即生效并
// 落盘（编辑器里的下拉框走的是同一个 setActiveHostByLabel）。
func (a *app) imageHostActivePicker(c *ui.Context) {
	t := c.Theme()
	names := a.hostNames()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "当前使用").FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
		if len(names) == 0 {
			ui.Text(c, "没有启用的图库，请在下面勾选一条").
				FontSize(12).TextColor(t.Danger).SingleLine()
			return
		}
		if ui.Select(c, &a.hostActiveLabel, names).
			Label("当前使用的图库").FillWidth().Changed() {
			a.setActiveHostByLabel(a.hostActiveLabel)
		}
	})
}

// imageHostEditor 画出当前选中要编辑的那一条图库的全部字段。
func (a *app) imageHostEditor(c *ui.Context) {
	t := c.Theme()
	all := a.allHostNames()
	if len(all) == 0 {
		ui.Text(c, "还没有图库配置，点「恢复默认」重建预设。").
			FontSize(12).TextColor(t.TextMuted)
		return
	}
	// 名字改过 / 条目删过之后，选中项可能已经不在列表里，退回第一条。
	if !containsString(all, a.hostEditLabel) {
		a.hostEditLabel = all[0]
		a.hostMaxFor = ""
	}
	h := a.hostByLabel(a.hostEditLabel)
	if h == nil {
		return
	}
	if ui.Select(c, &a.hostEditLabel, all).
		Label("要编辑的图库").FillWidth().Changed() {
		a.hostMaxFor = "" // 换条目时重新读这一条的 MB 草稿
	}
	// NumberInput 只认 float64，MaxBytes 是 int64：用一份按 ID 绑定的草稿，
	// 每帧把草稿写回配置，换条目时才重新初始化。
	if a.hostMaxFor != h.ID {
		a.hostMaxFor = h.ID
		a.hostMaxDraft = float64(h.MaxBytes >> 20)
	}
	ui.Checkbox(c, &h.Enabled, "启用（不启用则不出现在编辑器里）")
	ui.TextInput(c, &h.Name).Label("名称").FillWidth()
	if h.Kind == imageHostOfficial {
		a.officialHostStatus(c)
	} else {
		ui.TextInput(c, &h.Endpoint).Label("上传接口地址").
			Placeholder("https://example.com/api/v1/upload").FillWidth()
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.TextInput(c, &h.Field).Label("文件字段名").
				Placeholder("file").Grow(1).MinWidth(0)
			ui.TextInput(c, &h.URLPath).Label("返回 URL 路径").
				Placeholder("data.url，留空自动查找").Grow(1).MinWidth(0)
		})
		ui.TextInput(c, &h.Header).Label("请求头（一行一个）").
			Placeholder("Authorization: Bearer {{token}}").FillWidth()
		ui.TextInput(c, &h.Token).Label("Token（替换上面的 {{token}}）").
			Password().FillWidth()
		ui.TextArea(c, &h.Form).Label("附加表单字段（一行一个 name=value）").
			Placeholder("reqtype=fileupload").FillWidth().Lines(2, 4)
		ui.Checkbox(c, &h.TextResponse, "响应体直接就是图片地址（Catbox / x0.at 这类纯文本接口）")
		ui.Text(c, "响应里图片地址的路径用点分隔，如 data.url；"+
			"留空时会在 JSON 里自动找第一个 http(s) 图片地址。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "单张上限").FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
		ui.NumberInput(c, &a.hostMaxDraft,
			float64(minImageMaxBytes>>20), float64(maxImageMaxBytes>>20), 1).
			Label("单张上限（MB）")
		ui.Text(c, "MB").FontSize(11).TextColor(t.TextMuted).SingleLine()
		ui.Spacer(c).Grow(1)
	})
	mb := int64(a.hostMaxDraft)
	if mb < minImageMaxBytes>>20 {
		mb = minImageMaxBytes >> 20
	}
	if mb > maxImageMaxBytes>>20 {
		mb = maxImageMaxBytes >> 20
	}
	h.MaxBytes = mb << 20
	// 免费图床经常悄悄下线：给一个当场验证的入口，比让用户拖一张真图去试
	// 再回来看报错省事。测试图是 1×1 的 PNG，不会暴露用户自己的照片。
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "测试这个图床").Disabled(a.hostTesting != "").
			Tooltip("上传一张 1×1 的测试图，确认接口地址、字段名与返回解析都对").
			Clicked() {
			a.testImageHost(h)
		}
		if a.hostTesting == h.ID {
			ui.Text(c, "正在上传测试图…").FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		ui.Spacer(c).Grow(1)
	})
	if h.Note != "" {
		ui.Text(c, h.Note).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	}
	if h.Builtin {
		ui.Text(c, "内置预设：字段可改、可停用，不能删除。").
			FontSize(11).TextColor(t.TextMuted)
	} else if ui.Button(c, "删除此图库").Clicked() {
		a.removeImageHost(h.ID)
	}
	// 官方图床的门槛已经在 officialHostStatus 里说过了，这里只给通用图床
	// 补一句「还不能用」的原因，免得同一件事说两遍。
	if !a.hostReady(h) && h.Kind != imageHostOfficial {
		ui.Text(c, "还不能用："+a.hostNotReadyReason(h)).
			FontSize(11).TextColor(t.Danger).MaxLines(2)
	}
}

// officialHostStatus 说明官方图床的门槛：它只对 Pro 会员开放，所以这里要
// 明确告诉用户当前账号是不是 Pro、为什么还不能用，并留一个重新确认的入口
// （登录状态是缓存来的，换会员后不一定马上更新）。
func (a *app) officialHostStatus(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "2Libra 官方图床：需要 upload 权限，每人每天 20 张，单张不超过 10 MB；"+
		"只对 Pro 会员开放，缺权限时应用会自动弹出授权。").
		FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	if a.auth == nil || !a.auth.loggedIn() {
		ui.Text(c, "当前未登录：登录 2Libra 后会自动确认 Pro 状态。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		return
	}
	pro, known := a.proStatus()
	switch {
	case !known:
		ui.Text(c, "还没确认当前账号的 Pro 状态，可以点下面的按钮重新确认。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	case pro:
		ui.Text(c, "当前账号是 Pro，可以直接使用。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	default:
		ui.Text(c, "当前账号不是 Pro，官方图床不可用；请在上面「当前使用」里换用免 Token 图床（Kappa / Catbox）。").
			FontSize(11).TextColor(t.Danger).MaxLines(3)
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "重新确认 Pro 状态").Disabled(a.proChecking).Clicked() {
			a.refreshProStatus(true)
		}
		ui.Spacer(c).Grow(1)
	})
}

// testImageHost 往这条图床传一张 1×1 的测试图，当场验证接口地址、字段名与
// 响应解析是否都对。结果用 toast 报告：成功时带上拿到的地址，失败时带上
// 图床自己给的原因。同一时刻只测一条。
func (a *app) testImageHost(h *imageHost) {
	if h == nil || a.hostTesting != "" {
		return
	}
	if !a.hostReady(h) {
		a.toast(toastSettings, a.hostNotReadyReason(h))
		return
	}
	a.hostTesting = h.ID
	snapshot := *h
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), imageUploadTimeout+5*time.Second)
		defer cancel()
		u, err := a.uploadImage(ctx, snapshot, uploadSource{
			Name: "t2-test.png", Mime: "image/png", Data: testUploadPNG(),
		})
		a.publish(func() {
			a.hostTesting = ""
			if err != nil {
				a.toast(toastSettings, "「"+snapshot.Name+"」测试失败："+friendlyError(err))
				return
			}
			a.toast(toastSettings, "「"+snapshot.Name+"」测试成功："+u)
		})
	}()
}

// ------------------------------------------------------------------
// 设置窗口的编辑动作
// ------------------------------------------------------------------

func (a *app) allHostNames() []string {
	out := make([]string, 0, len(a.hosts.Hosts))
	for _, h := range a.hosts.Hosts {
		out = append(out, h.Name)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// addCustomImageHost 追加一条空的通用图床。ID 用递增序号，避免和内置项或
// 之前删掉又加回来的项撞车。
func (a *app) addCustomImageHost() {
	n := 1
	for {
		id := "user-" + strconv.Itoa(n)
		if hostByID(a.hosts, id) == nil {
			h := imageHost{
				ID: id, Name: "我的图库 " + strconv.Itoa(n),
				Kind: imageHostGeneric, Field: "file",
				MaxBytes: defaultImageMaxBytes, Enabled: false,
				Builtin: false, Note: "自建 / 第三方图床",
			}
			a.hosts.Hosts = append(a.hosts.Hosts, h)
			a.hostEditLabel = h.Name
			a.hostMaxFor = ""
			return
		}
		n++
	}
}

func (a *app) removeImageHost(id string) {
	for i, h := range a.hosts.Hosts {
		if h.ID != id || h.Builtin {
			continue
		}
		a.hosts.Hosts = append(a.hosts.Hosts[:i], a.hosts.Hosts[i+1:]...)
		if a.hosts.Active == id {
			// 删掉的是当前使用的那条：落到第一条仍启用的图库，别默认跳回
			// 官方图床 —— 非 Pro 账号跳过去也用不了。
			a.hosts.Active = normalizedActiveID(a.hosts)
		}
		a.hostActiveLabel = a.hostLabelByID(a.hosts.Active)
		a.hostEditLabel = ""
		a.hostMaxFor = ""
		a.toast(toastSettings, "已删除「"+h.Name+"」，记得点保存")
		return
	}
}

func (a *app) resetImageHosts() {
	a.hosts = defaultImageHostConfig()
	a.hostActiveLabel = a.hostLabelByID(a.hosts.Active)
	a.hostEditLabel = ""
	a.hostMaxFor = ""
	if err := a.saveImageHosts(); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.toast(toastSettings, "已恢复默认图库配置")
}

// hostConfigSummary 给测试与提示用的一句话概览：当前图库、启用数与总数。
func (a *app) hostConfigSummary() string {
	enabled := 0
	for _, h := range a.hosts.Hosts {
		if h.Enabled {
			enabled++
		}
	}
	return fmt.Sprintf("%s · 启用 %d / %d · %s",
		a.hostActiveLabel, enabled, len(a.hosts.Hosts),
		strings.TrimSpace(strings.Join(a.hostNames(), "、")))
}
