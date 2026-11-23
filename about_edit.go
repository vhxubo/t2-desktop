package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
)

// 编辑个人介绍（api-economy.md 的「个人介绍」）
//
//	POST /v1/me/about 🔑   scope write:economy，需开通 + user.edit.readme
//
// 请求体两个字段都可省略：content（≤20000 字符，空串是清空）与 is_public。
// 成功返回与 GET /v1/users/:name/about 完全相同的结构，直接替换本地状态。
// 500 金币是**异步**扣的：请求只校验余额并保存，审核通过后才扣费；因此成功后
// 不能声称已经扣款，toast 里要写清楚。入口只给本人，且介绍必须先读到，避免用
// 空草稿覆盖原内容。

const (
	myAboutPath = "/v1/me/about"
	// userAboutMaxChars 是文档给的内容上限。按字符（rune）计，中文一个字算一个。
	userAboutMaxChars = 20000
)

// aboutUpdateBody 是 POST /v1/me/about 的请求体。字段名照抄文档。
type aboutUpdateBody struct {
	Content  string `json:"content"`
	IsPublic bool   `json:"is_public"`
}

// updateMyAbout 提交个人介绍。这个端点标了 🔑：同一份内容的超时重试与授权后
// 重放必须复用同一个键；内容或公开状态变了才换新键。
func (c *Client) updateMyAbout(ctx context.Context, key, content string, isPublic bool) (userAbout, error) {
	return withAuthRetry(c, ctx, func(token string) (userAbout, error) {
		d, err := c.postJSON(ctx, c.apiURL(myAboutPath), token, key,
			aboutUpdateBody{Content: content, IsPublic: isPublic})
		if err != nil {
			return userAbout{}, err
		}
		return decodeUserAbout(d), nil
	})
}

// userCardSelf 报告资料卡上的人是不是当前登录账号。昵称大小写不敏感，
// 与站点「username 大小写不敏感」的约定一致。
func (a *app) userCardSelf() bool {
	cs := a.userCard
	if cs == nil || cs.username == "" || !a.auth.loggedIn() {
		return false
	}
	return strings.EqualFold(a.auth.username(), cs.username)
}

// openUserCardAboutEditor 打开编辑框。介绍没读到（加载中 / 失败）时不允许
// 编辑：草稿会从空开始，保存等于清空原介绍。
func (a *app) openUserCardAboutEditor() {
	cs := a.userCard
	if cs == nil || !a.userCardSelf() || !cs.aboutLoaded || cs.aboutErr != "" || cs.aboutBusy {
		return
	}
	cs.aboutEditOpen = true
	cs.aboutDraft = cs.about.Content
	cs.aboutTpl.reset()
	cs.aboutPublic = true
	if cs.about.PublicKnown {
		cs.aboutPublic = cs.about.IsPublic
	}
	cs.aboutErrEdit = ""
}

// submitUserCardAbout 保存个人介绍。成功后用服务端清理过的正文替换本地状态；
// 付费是异步的，只提示审核后会扣 500 金币，不在这里假装余额已经变了。
func (a *app) submitUserCardAbout() {
	cs := a.userCard
	if cs == nil || !cs.aboutEditOpen || cs.aboutBusy || !a.userCardSelf() {
		return
	}
	content := cs.aboutDraft
	if utf8.RuneCountInString(content) > userAboutMaxChars {
		cs.aboutErrEdit = fmt.Sprintf("个人介绍不能超过 %d 个字符", userAboutMaxChars)
		return
	}
	isPublic := cs.aboutPublic
	sig := fmt.Sprintf("%t|%s", isPublic, content)
	if cs.aboutKey == "" || cs.aboutSig != sig {
		cs.aboutKey = randomURLSafe(24)
		cs.aboutSig = sig
	}
	key := cs.aboutKey
	cs.aboutBusy = true
	cs.aboutErrEdit = ""
	cs.aboutGen++
	gen := cs.aboutGen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		about, err := a.client.updateMyAbout(ctx, key, content, isPublic)
		a.publish(func() {
			if a.userCard != cs || cs.aboutGen != gen {
				return
			}
			cs.aboutBusy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastUserCard, err) {
					return
				}
				cs.aboutErrEdit = friendlyError(err)
				return
			}
			cs.about = about
			cs.aboutLoaded = true
			cs.aboutErr = ""
			cs.user.HasReadme = strings.TrimSpace(about.Content) != ""
			cs.aboutEditOpen = false
			cs.aboutKey = ""
			cs.aboutSig = ""
			cs.aboutErrEdit = ""
			a.toast(toastUserCard, fmt.Sprintf("已保存，审核后扣除 %d 金币", aboutEditCost))
		})
	}()
}

// aboutEditorParams 是编辑模态框的输入。草稿与公开开关用指针绑到调用方的
// 持久状态上，两处窗口（资料卡与「我的」）共用同一套画法。about 提供「个人
// 常量」下拉的取值，tpl 保存快捷模板的开合与选区状态。
type aboutEditorParams struct {
	open    *bool
	draft   *string
	public  *bool
	busy    bool
	errText string
	about   userAbout
	tpl     *aboutTemplateState
}

// aboutEditorPreviewHeight 是介绍预览区的固定高度。模态框整体已经能滚，
// 预览再给一个固定高度，编辑 / 预览来回切时布局不会一下撑满整窗。
const aboutEditorPreviewHeight float32 = 220

// aboutEditor 画编辑个人介绍的模态框：正文用与发帖同一套 Markdown 编辑器
// （工具条、编辑 / 预览切换、表情与 Base64），上面是官网同款的快捷模板下拉，
// 下面是 Lobe 公开开关、字数、保存进度与审核规则小字。费用只写在按钮上（每次
// 500 金币）。返回是否点了确认，关窗由调用方按 open 处理。
func (a *app) aboutEditor(c *ui.Context, p aboutEditorParams) bool {
	t := c.Theme()
	count := utf8.RuneCountInString(*p.draft)
	countColor := t.TextMuted
	if count > userAboutMaxChars {
		countColor = t.Danger
	}
	ok := feedback.Modal(c, p.open, feedback.ModalProps{
		Title:      "编辑个人介绍",
		Width:      560,
		OkText:     "保存（500 金币）",
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			a.aboutEditorMarkdown(c, p)
			// 是否公开换成 Lobe 开关：开关本体与文字都能切换。
			composeToggle(c, p.public, "公开个人介绍")
			ui.Textf(c, "%d / %d 字符", count, userAboutMaxChars).
				FontSize(10).TextColor(countColor)
			if p.errText != "" {
				ui.Text(c, p.errText).FontSize(11).TextColor(t.Danger).MaxLines(2)
			}
			if p.busy {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{CustomSize: 13})
					ui.Text(c, "正在保存…").FontSize(11).TextColor(t.TextMuted)
				})
			}
			aboutEditorGuidelines(c)
		})
	})
	return ok
}

// aboutEditorMarkdown 把发帖那套 Markdown 编辑器接进介绍编辑框：工具条只画
// 图标，编辑 / 预览切换、表情面板与 Base64 都和发帖共用。介绍里的 {{KEY}}
// 占位符与社区表情在预览时按展示时的样子展开（见 aboutEditorPreview）。
// 快捷模板下拉仍在输入框之前，点选插入时输入框能在同一帧读到待恢复的选区。
func (a *app) aboutEditorMarkdown(c *ui.Context, p aboutEditorParams) {
	ed := &p.tpl.ed
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			display.Text(c, "正文", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			}).Grow(1)
			ui.Row(c).Gap(2).AlignItems(ui.Center).KeepFocus().
				Label("介绍视图").Children(func() {
				a.editorIconToggle(c, ed.preview == 0, editIcon, "编辑", "编辑模式",
					func() { ed.preview = 0 })
				a.editorIconToggle(c, ed.preview == 1, eyeIcon, "预览", "预览模式",
					func() { ed.preview = 1 })
			})
			// 表情入口贴着视图切换；图片上传与插入帖子在介绍里用不上，
			// 图片改由工具条的 ![说明](url) 与展示组件里的图片地址插入。
			emojiBtn := a.editorIconButton(c, reactionIcon, "插入表情", "插入社区表情")
			panel := lobeoverlay.Popover(c, emojiBtn, &ed.emojiOpen,
				lobeoverlay.PopoverProps{Placement: lobeoverlay.Bottom}, func() {
					a.editorEmojiPanel(c, ed, p.draft, p.about.Emojis)
				})
			panel.Padding(4)
			if emojiBtn.Clicked() {
				ed.imageOpen = false
				ed.topicOpen = false
				ed.base64Open = false
			}
		})
		zone := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
			FillWidth().Radius(10)
		zone.Children(func() {
			ui.Row(c).FillWidth().Padding(2, 4).Children(func() {
				a.editorToolbar(c, ed, p.draft)
			})
			layout.Divider(c, layout.DividerProps{})
			// Base64 从工具条进来，这里把它的 Modal 挂上；不接 uploadTarget，
			// 所以不会误用发帖的上传队列。
			if ed.base64Open {
				a.editorBase64Dialog(c, ed, p.draft)
			}
			if ed.preview == 1 {
				a.aboutEditorPreview(c, p)
				return
			}
			aboutTemplateToolbar(c, p.tpl, p.about, p.draft)
			c.Key("about-body-area")
			area := composeTextArea(c, p.draft, field.TextAreaProps{
				Placeholder: "支持 Markdown：**加粗**、# 标题、> 引用、- 列表、`代码`、[链接](url)、![图片](url) 与 :smile: 表情；也可从上方插入个人常量与展示组件",
				MinRows:     8,
				MaxRows:     14,
				Variant:     field.InputBorderless,
			})
			area.Label("个人介绍")
			ed.syncSelection(area)
		})
	})
}

// aboutEditorPreview 把草稿按展示时的样子画出来：先展开 {{KEY}} 统计占位符，
// 再把表情短码换成行内图片，最后交给与资料卡正文同一套 Markdown 选项。
func (a *app) aboutEditorPreview(c *ui.Context, p aboutEditorParams) {
	ui.Scroll(c).FillWidth().Height(aboutEditorPreviewHeight).Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14).Children(func() {
			if strings.TrimSpace(*p.draft) == "" {
				ui.Text(c, "还没有正文。切回「编辑」写点什么，这里会按展示时的样子显示。").
					FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(3)
				return
			}
			emojis := mergeEmojiMaps(a.emojis, p.about.Emojis)
			src := replaceEmoji(expandAboutStats(*p.draft, p.about.Stats), emojis, true)
			a.markdownView(c, src, a.bioMd)
		})
	})
}

// aboutEditorGuidelines 是编辑框底部的小字：公开范围、禁止 / 允许内容，以及
// 违规后果。文案来自站点对介绍内容的约束，提醒用户别把介绍写成广告位。
func aboutEditorGuidelines(c *ui.Context) {
	t := c.Theme()
	layout.Divider(c, layout.DividerProps{})
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		ui.Text(c, "若设置为不公开，未登录用户将无法查看到你的介绍信息。").
			FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		ui.Text(c, "禁止内容：不得发布商业广告、违法违规、政治敏感、人身攻击等违规内容。").
			FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		ui.Text(c, "允许内容：个人以及个人产品介绍、项目推广、作品展示、开源资源、技术博客等合理分享。违规内容将被清空，且一段时间内无法再次更新。").
			FontSize(10).TextColor(t.TextMuted).MaxLines(3)
	})
}

// 保存一次个人介绍消耗 500 金币，由服务端审核通过后异步扣除；请求本身只
// 校验余额并保存，所以成功提示只说「将扣除」，不假装余额已经变了。
const aboutEditCost = 500

// userCardAboutEditor 是资料卡上的编辑弹窗。取消、✕、Esc 与点窗外都清掉
// 草稿状态；busy 时 Modal 的确认按钮仍是亮的，submit 自己拦重复提交。
func (a *app) userCardAboutEditor(c *ui.Context) {
	cs := a.userCard
	if cs == nil || !cs.aboutEditOpen {
		return
	}
	ok := a.aboutEditor(c, aboutEditorParams{
		open: &cs.aboutEditOpen, draft: &cs.aboutDraft, public: &cs.aboutPublic,
		busy: cs.aboutBusy, errText: cs.aboutErrEdit,
		about: cs.about, tpl: &cs.aboutTpl,
	})
	if !cs.aboutEditOpen {
		cs.aboutErrEdit = ""
		return
	}
	if ok {
		a.submitUserCardAbout()
	}
}

// openProfileAboutEditor 打开「我的 - 个人简介」的编辑框。正文没读到时不给
// 编辑：草稿会从空开始，保存等于清空原介绍。
func (a *app) openProfileAboutEditor() {
	ps := a.profile
	if ps == nil || !a.auth.loggedIn() || !ps.aboutLoaded || ps.aboutErr != "" || ps.aboutBusy {
		return
	}
	ps.aboutEditOpen = true
	ps.aboutDraft = ps.about.Content
	ps.aboutTpl.reset()
	ps.aboutPublic = true
	if ps.about.PublicKnown {
		ps.aboutPublic = ps.about.IsPublic
	}
	ps.aboutErrEdit = ""
}

// submitProfileAbout 保存「我的 - 个人简介」。请求体、幂等键与 500 金币的
// 异步扣费规则与资料卡完全相同，只是状态与 toast 属于「我的」窗口。
func (a *app) submitProfileAbout() {
	ps := a.profile
	if ps == nil || !ps.aboutEditOpen || ps.aboutBusy || !a.auth.loggedIn() {
		return
	}
	content := ps.aboutDraft
	if utf8.RuneCountInString(content) > userAboutMaxChars {
		ps.aboutErrEdit = fmt.Sprintf("个人介绍不能超过 %d 个字符", userAboutMaxChars)
		return
	}
	isPublic := ps.aboutPublic
	sig := fmt.Sprintf("%t|%s", isPublic, content)
	if ps.aboutKey == "" || ps.aboutSig != sig {
		ps.aboutKey = randomURLSafe(24)
		ps.aboutSig = sig
	}
	key := ps.aboutKey
	ps.aboutBusy = true
	ps.aboutErrEdit = ""
	ps.aboutGen++
	gen := ps.aboutGen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		about, err := a.client.updateMyAbout(ctx, key, content, isPublic)
		a.publish(func() {
			if a.profile != ps || ps.aboutGen != gen {
				return
			}
			ps.aboutBusy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastProfile, err) {
					return
				}
				ps.aboutErrEdit = friendlyError(err)
				return
			}
			ps.about = about
			ps.aboutLoaded = true
			ps.aboutErr = ""
			ps.user.HasReadme = strings.TrimSpace(about.Content) != ""
			ps.aboutEditOpen = false
			ps.aboutKey = ""
			ps.aboutSig = ""
			ps.aboutErrEdit = ""
			a.toast(toastProfile, fmt.Sprintf("已保存，审核后扣除 %d 金币", aboutEditCost))
		})
	}()
}

// profileAboutEditor 是「我的」窗口里的编辑弹窗，画法与资料卡共用。
func (a *app) profileAboutEditor(c *ui.Context) {
	ps := a.profile
	if ps == nil || !ps.aboutEditOpen {
		return
	}
	ok := a.aboutEditor(c, aboutEditorParams{
		open: &ps.aboutEditOpen, draft: &ps.aboutDraft, public: &ps.aboutPublic,
		busy: ps.aboutBusy, errText: ps.aboutErrEdit,
		about: ps.about, tpl: &ps.aboutTpl,
	})
	if !ps.aboutEditOpen {
		ps.aboutErrEdit = ""
		return
	}
	if ok {
		a.submitProfileAbout()
	}
}
