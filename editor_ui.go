package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 发帖窗口与评论输入框共用的编辑器界面：一条 Markdown 工具条、两个互斥的
// 工具面板（表情 / 图片），以及按文章正文方式渲染的预览。
//
// 界面本身不保存任何 ui.Element 或 *ui.Context；它只读写调用方传进来的
// mdEditor 状态与正文指针。两边各自负责把输入框、拖放区和预览容器摆好，
// 再把同一个 body 交给这里。

// replyPreviewHeight 是评论输入框里预览区的高度：评论区比正文紧凑，
// 预览不用占满一屏，滚动即可看全。
const replyPreviewHeight = 180

// editorToolbar 是两边共用的 Markdown 工具条。原有的标记按钮一字不改：
// 工具条窄时 MyGo 从末尾把放不下的按钮收进「»」菜单，所以表情 / 图片
// 入口不放在这里，否则默认窗口宽度下这两项恰好是最先被藏起来的。
func (a *app) editorToolbar(c *ui.Context, e *mdEditor, body *string) {
	ui.Toolbar(c, func() {
		for _, b := range markdownButtons {
			if ui.Button(c, b.label).Tooltip(b.hint).Clicked() {
				e.applyMarkdown(body, b.format)
			}
		}
	}).Label("编辑工具").FillWidth()
}

// editorPanelButtons 放在「编辑 / 预览」那一行，永远可见。emojiLabel /
// imageLabel 由调用方给：详情页正文里已经有一个「表情」表态按钮，评论
// 工具条改用别的名字，免得读屏与点击测试分不清两个入口。
func (a *app) editorPanelButtons(c *ui.Context, e *mdEditor, emojiLabel, imageLabel string) {
	if ui.Button(c, emojiLabel).Tooltip("插入社区表情").Clicked() {
		e.togglePanel("emoji")
	}
	if ui.Button(c, imageLabel).Tooltip("拖入、选择或粘贴图片").Clicked() {
		e.togglePanel("image")
	}
}

// editorPanels 画当前打开的那个面板。两个面板互斥，由 togglePanel 保证。
func (a *app) editorPanels(c *ui.Context, t uploadTarget, e *mdEditor,
	body *string, extra map[string]string) {
	if e.emojiOpen {
		a.editorEmojiPanel(c, e, body, extra)
		return
	}
	if e.imageOpen {
		a.editorImagePanel(c, t)
	}
}

// ------------------------------------------------------------------
// 表情面板
// ------------------------------------------------------------------

// editorEmojiPanel 合并全局表情表与当前帖子自带的社区表情，按短码排序后
// 分批展示。插入的是 :code: 短码而不是图片链接：服务端正文认短码，预览与
// 详情页正文会用同一套 replaceEmoji 把它换成行内图片。
func (a *app) editorEmojiPanel(c *ui.Context, e *mdEditor, body *string,
	extra map[string]string) {
	t := c.Theme()
	list := editorEmojis(a.emojis, extra)
	community := editorCommunityCodes(extra)
	filtered := editorFilteredEmojis(list, e.emojiQuery, e.emojiOnlyPost, community)
	shown := filtered
	if len(shown) > e.emojiShown {
		shown = shown[:e.emojiShown]
	}

	ui.Column(c).FillWidth().Padding(8, 10).Gap(6).
		Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &e.emojiQuery).Label("搜索表情").
				Placeholder("短码或中文名，如 smile / 微笑").Grow(1).MinWidth(0)
			ui.Checkbox(c, &e.emojiOnlyPost, "只看本帖表情")
		})
		if len(shown) == 0 {
			ui.Text(c, "没有匹配的表情。清空搜索词，或关掉「只看本帖表情」再看。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		}
		for start := 0; start < len(shown); start += emojiColumns {
			row := shown[start:min(start+emojiColumns, len(shown))]
			ui.Row(c).FillWidth().Gap(4).Children(func() {
				for _, em := range row {
					cell := ui.Row(c).Gap(4).AlignItems(ui.Center).
						Padding(3, 5).Radius(6).Background(ui.Transparent).Grow(1).MinWidth(0)
					if cell.Hovered() {
						cell.Background(t.SurfaceHover)
					}
					cell.Children(func() {
						// 缩略图走全应用共用的图片缓存：没下载好时只显示短码，
						// 下载完成后缓存会触发重绘，不会每帧发请求。
						if src := a.articleImage(em.ImgURL, imageCacheStable); src != nil {
							ui.Image(c, src).Size(18, 18).Fit(ui.Contain)
						}
						ui.Text(c, ":"+em.Code+":").FontSize(11).
							TextColor(t.Text).SingleLine()
					})
					cell.Label(em.Name).Tooltip("插入 :" + em.Code + ":").
						OnClick(func() { e.insertText(body, ":"+em.Code+":") })
				}
			})
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "共 "+strconv.Itoa(len(filtered))+" 个表情，显示 "+
				strconv.Itoa(len(shown))+" 个").
				FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
			if len(filtered) > e.emojiShown {
				if ui.Button(c, "显示更多表情").Clicked() {
					e.emojiShown += emojiPageSize
				}
			}
			if ui.Button(c, "收起表情").Clicked() {
				e.emojiOpen = false
			}
		})
	})
}

// ------------------------------------------------------------------
// 图片面板
// ------------------------------------------------------------------

// editorImagePanel 是三条上传入口的说明与任务列表：切图库、选文件、粘贴
// 剪贴板，以及每一张图的排队 / 上传 / 完成 / 失败状态与重试、移除。
func (a *app) editorImagePanel(c *ui.Context, t uploadTarget) {
	th := c.Theme()
	ui.Column(c).FillWidth().Padding(8, 10).Gap(8).
		Background(th.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "当前图库").FontSize(11).TextColor(th.TextMuted).
				SingleLine().Shrink(0)
			names := a.hostNames()
			if len(names) == 0 {
				ui.Text(c, "没有启用的图库，请到设置里开启").
					FontSize(11).TextColor(th.Danger).SingleLine()
			} else {
				if ui.Select(c, &a.hostActiveLabel, names).
					Label("当前图库").Changed() {
					a.setActiveHostByLabel(a.hostActiveLabel)
				}
			}
			ui.Spacer(c).Grow(1)
		})
		ui.Text(c, "把图片直接拖进编辑区，或用下面的按钮。支持 JPEG / PNG / WebP，单张上限在图库设置里改。").
			FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.Button(c, "选择图片").Clicked() {
				a.pickImages(t)
			}
			if ui.Button(c, "粘贴剪贴板图片").Clicked() {
				a.pasteImages(t)
			}
			if ui.Button(c, "图库设置").Clicked() {
				a.showSettings()
			}
		})
		jobs := a.uploadsFor(t)
		if len(jobs) > 0 {
			ui.Divider(c)
			for _, j := range jobs {
				a.editorUploadRow(c, j)
			}
		}
		if a.pendingUploadCount() > 0 {
			ui.Text(c, "还有 "+strconv.Itoa(a.pendingUploadCount())+
				" 张图片未处理完：上传结束后会自动换成图片地址，失败或仍在上传时不能发布 / 发送。").
				FontSize(11).TextColor(th.TextMuted).MaxLines(3)
		}
	})
}

func (a *app) editorUploadRow(c *ui.Context, j *uploadJob) {
	t := c.Theme()
	state, color := "排队中", t.TextMuted
	switch j.State {
	case uploadRunning:
		state, color = "上传中…", t.Accent
	case uploadDone:
		state, color = "已上传", t.Success
	case uploadFailed:
		state, color = "失败："+j.Err, t.Danger
	}
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, j.Name).FontSize(11).TextColor(t.Text).
			SingleLine().Grow(1).MinWidth(0)
		ui.Text(c, state).FontSize(11).TextColor(color).MaxLines(2)
		if j.State == uploadFailed {
			if ui.Button(c, "重试 "+j.Name).Clicked() {
				a.retryUpload(j.ID)
			}
		}
		if ui.Button(c, "移除 "+j.Name).Clicked() {
			a.removeUpload(j.ID)
		}
	})
}

// ------------------------------------------------------------------
// 预览
// ------------------------------------------------------------------

// editorPreviewView 把草稿按发表后的样子画出来：先把未完成的图片占位符
// 换成中文说明（不产生坏链接），再把 :code: 表情换成行内图片，最后交给
// 与详情页正文完全相同的 markdown.View 选项。
func (a *app) editorPreviewView(c *ui.Context, body string,
	emojis map[string]emojiInfo, o *markdown.Options, height float32) {
	t := c.Theme()
	ui.Scroll(c).FillWidth().Height(height).Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14).Children(func() {
			if strings.TrimSpace(body) == "" {
				ui.Text(c, "还没有正文。切回「编辑」写点什么，这里会按发表后的样子显示。").
					FontSize(12).TextColor(t.TextMuted).MaxLines(3)
				return
			}
			a.markdownView(c, a.editorPreviewSource(body, emojis), o)
		})
	})
}
