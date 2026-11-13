package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 设置窗口里的「图库（图床）」一块。
//
// 内置预设（官方 / SM.MS / Imgur / 兰空 / 自定义）默认摆在这里，可以直接改
// 字段、停用或启用，但不能删除；用户自己加的项可以改也可以删。所有编辑直接
// 落在 a.hosts 上（同一份内存配置也是编辑器和上传队列读的），点「保存」才
// 校验并写入 localKV 的 imagehosts:v1，没保存就关窗口不会生效到下次启动。

// imageHostsSection 一次只展开一条图库的字段：设置窗口只有 520 宽，把五条
// 预设的所有输入框全铺开会变成一屏填不完的表单。
func (a *app) imageHostsSection(c *ui.Context) {
	t := c.Theme()
	settingsSection(c, "图库（图床）", func() {
		ui.Text(c, "写帖子或评论时拖入、选择、粘贴的图片会传到「当前使用」的图库。"+
			"官方图床用 2Libra 登录态；第三方图床填接口地址、文件字段名与 Token 即可。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)
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
		ui.Text(c, "2Libra 官方图床：登录即可使用，需要 upload 权限，每人每天 20 张，"+
			"单张不超过 10 MB。缺权限时应用会自动弹出授权。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
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
	if h.Note != "" {
		ui.Text(c, h.Note).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
	}
	if h.Builtin {
		ui.Text(c, "内置预设：字段可改、可停用，不能删除。").
			FontSize(11).TextColor(t.TextMuted)
	} else if ui.Button(c, "删除此图库").Clicked() {
		a.removeImageHost(h.ID)
	}
	if !a.hostReady(h) {
		ui.Text(c, "还不能用："+a.hostNotReadyReason(h)).
			FontSize(11).TextColor(t.Danger).MaxLines(2)
	}
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
			a.hosts.Active = "2libra"
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
