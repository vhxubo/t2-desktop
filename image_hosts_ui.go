package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 设置窗口里的「图库（图床）」一块。
//
// 内置预设（官方 / Kappa / MJJ.Today / ImgBB / Appinn / Skyimg / StarDots …）
// 默认摆在这里，可以直接改字段、停用或启用，但不能删除；用户自己加的项可以
// 改也可以删。所有编辑直接落在 a.hosts 上（同一份内存配置也是编辑器和上传
// 队列读的），点「保存」才校验并写入 localKV 的 imagehosts:v1，没保存就关
// 窗口不会生效到下次启动。

// imageHostsSection 一次只展开一条图库的字段：设置窗口只有 520 宽，把十几条
// 预设的所有输入框全铺开会变成一屏填不完的表单。
func (a *app) imageHostsSection(c *ui.Context) {
	settingsSection(c, "图库（图床）", func() {
		settingsDescription(c, "写帖子或评论时拖入、选择、粘贴的图片会传到「当前使用」的图库。"+
			"Kappa / MJJ.Today / ImgBB / Appinn / Skyimg 这类图床免注册、免 Token；"+
			"2Libra 官方图床只对 Pro 会员与守护者开放。免费图床变动频繁，可以点下面的"+
			"「测试这个图床」确认还能不能用。", 4)
		a.imageHostActivePicker(c)
		layout.Divider(c, layout.DividerProps{})
		a.imageHostEditor(c)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if action.Button(c, "添加自定义图库", action.ButtonProps{}).Clicked() {
				a.addCustomImageHost()
			}
			if action.Button(c, "恢复默认", action.ButtonProps{}).Clicked() {
				a.resetImageHosts()
			}
			if action.Button(c, "保存图库设置", action.ButtonProps{Variant: action.Primary}).Clicked() {
				if err := a.saveImageHosts(); err != nil {
					a.toast(toastSettings, err.Error())
				} else {
					a.toast(toastSettings, "图库设置已保存")
				}
			}
		})
	})
}

// selectOptions 把一串名字转成 Lobe 下拉框的选项：值与显示文字相同。
func selectOptions(names []string) []field.SelectOption {
	out := make([]field.SelectOption, 0, len(names))
	for _, name := range names {
		out = append(out, field.SelectOption{Value: name, Label: name})
	}
	return out
}

// imageHostActivePicker 只在已启用的图库里选「当前使用」，切换立即生效并
// 落盘（编辑器里的下拉框走的是同一个 setActiveHostByLabel）。
func (a *app) imageHostActivePicker(c *ui.Context) {
	names := a.hostNames()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, "当前使用", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		}).Shrink(0)
		if len(names) == 0 {
			display.Text(c, "没有启用的图库，请在下面勾选一条",
				display.TextProps{Type: display.TextDanger, FontSize: 12, Rows: 1})
			return
		}
		if lobeSelect(c, &a.hostActiveLabel, field.SelectProps{
			Options: selectOptions(names), Size: lobetheme.Small,
		}).Label("当前使用的图库").Changed() {
			a.setActiveHostByLabel(a.hostActiveLabel)
		}
	})
}

// imageHostEditor 画出当前选中要编辑的那一条图库的全部字段。
func (a *app) imageHostEditor(c *ui.Context) {
	all := a.allHostNames()
	if len(all) == 0 {
		display.Text(c, "还没有图库配置，点「恢复默认」重建预设。",
			display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2})
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
	if settingsField(c, "要编辑的图库", func() ui.Element {
		return lobeSelect(c, &a.hostEditLabel, field.SelectProps{
			Options: selectOptions(all), Size: lobetheme.Small,
		}).Label("要编辑的图库")
	}).Changed() {
		a.hostMaxFor = "" // 换条目时重新读这一条的 MB 草稿
	}
	// InputNumber 只认 float64，MaxBytes 是 int64：用一份按 ID 绑定的草稿，
	// 每帧把草稿写回配置，换条目时才重新初始化。
	if a.hostMaxFor != h.ID {
		a.hostMaxFor = h.ID
		a.hostMaxDraft = float64(h.MaxBytes >> 20)
	}
	choice.Checkbox(c, &h.Enabled, "启用（不启用则不出现在编辑器里）", choice.CheckboxProps{})
	settingsField(c, "名称", func() ui.Element {
		return field.Input(c, &h.Name, field.InputProps{Size: lobetheme.Small}).Label("名称")
	})
	if h.Kind == imageHostOfficial {
		a.officialHostStatus(c)
	} else if h.Adapter != "" {
		a.adapterHostFields(c, h)
	} else {
		settingsField(c, "上传接口地址", func() ui.Element {
			return field.Input(c, &h.Endpoint, field.InputProps{
				Placeholder: "https://example.com/api/v1/upload", Size: lobetheme.Small,
			}).Label("上传接口地址")
		})
		ui.Row(c).Gap(8).Children(func() {
			settingsHalfField(c, "文件字段名", func() ui.Element {
				return field.Input(c, &h.Field, field.InputProps{
					Placeholder: "file", Size: lobetheme.Small,
				}).Label("文件字段名")
			})
			settingsHalfField(c, "返回 URL 路径", func() ui.Element {
				return field.Input(c, &h.URLPath, field.InputProps{
					Placeholder: "data.url，留空自动查找", Size: lobetheme.Small,
				}).Label("返回 URL 路径")
			})
		})
		settingsField(c, "请求头（一行一个）", func() ui.Element {
			return field.Input(c, &h.Header, field.InputProps{
				Placeholder: "Authorization: Bearer {{token}}", Size: lobetheme.Small,
			}).Label("请求头（一行一个）")
		})
		settingsField(c, "Token（替换上面的 {{token}}）", func() ui.Element {
			return field.InputPassword(c, &h.Token, field.InputProps{Size: lobetheme.Small}).
				Label("Token（替换上面的 {{token}}）")
		})
		settingsField(c, "附加表单字段（一行一个 name=value）", func() ui.Element {
			return field.TextArea(c, &h.Form, field.TextAreaProps{
				Placeholder: "reqtype=fileupload", MinRows: 2, MaxRows: 4,
			}).Label("附加表单字段（一行一个 name=value）")
		})
		choice.Checkbox(c, &h.TextResponse,
			"响应体直接就是图片地址（Catbox / x0.at 这类纯文本接口）", choice.CheckboxProps{})
		display.Text(c, "响应里图片地址的路径用点分隔，如 data.url；"+
			"留空时会在 JSON 里自动找第一个 http(s) 图片地址。"+
			"附加字段的值里可以写 {{token}} 与 {{filename}}。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, "单张上限", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		}).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
			field.InputNumber(c, &a.hostMaxDraft, field.InputNumberProps{
				Min:  float64(minImageMaxBytes >> 20),
				Max:  float64(maxImageMaxBytes >> 20),
				Step: 1, Size: lobetheme.Small,
			}).Label("单张上限（MB）")
		})
		display.Text(c, "MB", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		}).Shrink(0)
	})
	mb := int64(a.hostMaxDraft)
	if mb < minImageMaxBytes>>20 {
		mb = minImageMaxBytes >> 20
	}
	if mb > maxImageMaxBytes>>20 {
		mb = maxImageMaxBytes >> 20
	}
	// 各家协议自己的上限更严时按它来，界面上显示的就是真能传的大小。
	if cap := adapterMaxBytes(h.Adapter); cap > 0 && mb<<20 > cap {
		mb = cap >> 20
	}
	h.MaxBytes = mb << 20
	// 免费图床经常悄悄下线：给一个当场验证的入口，比让用户拖一张真图去试
	// 再回来看报错省事。测试图是 1×1 的 PNG，不会暴露用户自己的照片。
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if action.Button(c, "测试这个图床", action.ButtonProps{Size: lobetheme.Small}).
			Disabled(a.hostTesting != "").
			Tooltip("上传一张 1×1 的测试图，确认接口地址、字段名与返回解析都对").
			Clicked() {
			a.testImageHost(h)
		}
		if a.hostTesting == h.ID {
			lobeSpin(c, feedback.SpinProps{CustomSize: 11})
			display.Text(c, "正在上传测试图…", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		}
		ui.Spacer(c).Grow(1)
	})
	if h.Note != "" {
		display.Text(c, h.Note, display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 3,
		})
	}
	if h.Builtin {
		display.Text(c, "内置预设：字段可改、可停用，不能删除。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
	} else if iconActionButton(c, trashIcon, "删除此图库",
		"从图库列表里删掉「"+h.Name+"」，之后上传不再选它").Clicked() {
		a.removeImageHost(h.ID)
	}
	// 官方图床的门槛已经在 officialHostStatus 里说过了，这里只给通用图床
	// 补一句「还不能用」的原因，免得同一件事说两遍。
	if !a.hostReady(h) && h.Kind != imageHostOfficial {
		display.Text(c, "还不能用："+a.hostNotReadyReason(h),
			display.TextProps{Type: display.TextDanger, FontSize: 11, Rows: 2})
	}
}

// adapterHostFields 画内置协议那几家的字段：接口地址可以留空（用内置地址），
// 只有 StarDots 要 Key + Secret + 存储桶，Imgur 可以填自己的 Client-ID。
func (a *app) adapterHostFields(c *ui.Context, h *imageHost) {
	settingsField(c, "上传接口地址（留空用内置地址）", func() ui.Element {
		return field.Input(c, &h.Endpoint, field.InputProps{
			Placeholder: adapterEndpoint(h.Adapter), Size: lobetheme.Small,
		}).Label("上传接口地址（留空用内置地址）")
	})
	switch h.Adapter {
	case adapterStarDots:
		ui.Row(c).Gap(8).Children(func() {
			settingsHalfField(c, "API Key", func() ui.Element {
				return field.Input(c, &h.Token, field.InputProps{Size: lobetheme.Small}).Label("API Key")
			})
			settingsHalfField(c, "API Secret", func() ui.Element {
				return field.InputPassword(c, &h.Secret, field.InputProps{Size: lobetheme.Small}).Label("API Secret")
			})
		})
		settingsField(c, "存储桶（Bucket）", func() ui.Element {
			return field.Input(c, &h.Bucket, field.InputProps{Size: lobetheme.Small}).Label("存储桶（Bucket）")
		})
		display.Text(c, "Key 与 Secret 在 StarDots 控制台的「Key and Secret」页生成；"+
			"上传时应用按官方规则现签一个 MD5 签名。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	case adapterImgur:
		settingsField(c, "Client-ID（留空用内置公共 ID）", func() ui.Element {
			return field.Input(c, &h.Token, field.InputProps{Size: lobetheme.Small}).
				Label("Client-ID（留空用内置公共 ID）")
		})
		display.Text(c, "填自己的 Client-ID 最稳；留空时在内置的几个公共 Client-ID 之间轮换，"+
			"随时可能被限流。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	default:
		display.Text(c, "这条内置协议不用填 Token：令牌、签名、请求头都由应用按站点规则处理，"+
			"接口地址留空就用内置地址。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	}
}

// officialHostStatus 说明官方图床的门槛：它只对 Pro 会员与守护者开放，所以
// 要明确告诉用户当前账号有没有权限、为什么还不能用，并留一个重新确认的入口
// （登录状态是缓存来的，换了会员 / 角色后不一定马上更新）。
func (a *app) officialHostStatus(c *ui.Context) {
	display.Text(c, "2Libra 官方图床：需要 upload 权限，每人每天 20 张，单张不超过 10 MB；"+
		"只对 Pro 会员与守护者开放，缺权限时应用会自动弹出授权。",
		display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
	if a.auth == nil || !a.auth.loggedIn() {
		display.Text(c, "当前未登录：登录 2Libra 后会自动确认账号权限。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		return
	}
	allowed, known := a.officialHostAccess()
	switch {
	case !known:
		display.Text(c, "还没确认当前账号的图库权限，可以点下面的按钮重新确认。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	case allowed:
		display.Text(c, "当前账号是 Pro 会员或守护者，可以直接使用。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	default:
		display.Text(c, "当前账号既不是 Pro 也不是守护者，官方图床不可用；请在上面「当前使用」里换用免 Token 图床（Kappa / Catbox）。",
			display.TextProps{Type: display.TextDanger, FontSize: 11, Rows: 3})
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if action.Button(c, "重新确认图库权限", action.ButtonProps{Size: lobetheme.Small}).
			Disabled(a.proChecking).Clicked() {
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

// restoreImageHosts 把图库配置写回内置默认并落盘，不弹提示；调用方决定怎么报告结果。
func (a *app) restoreImageHosts() error {
	a.hosts = defaultImageHostConfig()
	a.hostActiveLabel = a.hostLabelByID(a.hosts.Active)
	a.hostEditLabel = ""
	a.hostMaxFor = ""
	return a.saveImageHosts()
}

func (a *app) resetImageHosts() {
	if err := a.restoreImageHosts(); err != nil {
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
