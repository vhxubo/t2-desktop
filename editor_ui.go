package main

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 发帖窗口与评论输入框共用的编辑器界面：一条 Markdown 工具条、两个互斥的
// 工具面板（表情 / 图片），以及按文章正文方式渲染的预览。
//
// 编辑器里所有入口都只画图标，说明交给悬浮提示：工具条上十二个标记按钮排成
// 一行也不会互相挤，表情 / 图片 / 编辑预览这些入口也不会因为文案长短不一而
// 参差。图标按钮的无障碍名仍是文字（Label），读屏与点击测试照旧按名字找得到；
// 平时给一层悬停底色，鼠标扫过就知道哪些地方能点。
//
// 界面本身不保存任何 ui.Element 或 *ui.Context；它只读写调用方传进来的
// mdEditor 状态与正文指针。两边各自负责把输入框、拖放区和预览容器摆好，
// 再把同一个 body 交给这里。

// 评论输入框里预览区的高度范围：评论区比正文紧凑，预览按草稿长度在这个
// 范围里伸缩（见 replyPreviewFit），不占满一屏。
const (
	replyPreviewMin = 120
	replyPreviewMax = 300
)

// 编辑器图标按钮的尺寸：图标 15 DIP、内边距 3 / 4。发帖窗口与评论输入框
// 共用这一套尺寸，两个编辑器的工具条看起来是同一个东西；评论区一行也放得下
// 全部标记，输入框不会比评论列表还重。
const (
	editorIconSize   float32 = 15
	editorIconPadY   float32 = 3
	editorIconPadX   float32 = 4
	editorIconRadius float32 = 5
)

// editorToolbar 是两边共用的 Markdown 工具条：每个标记一个图标按钮，悬浮
// 提示写「名字：插入的标记」（例如「加粗：**加粗**」）——只画图标时它是唯一
// 的说明来源，所以把会插进正文的标记一并带上。
//
// 表情 / 图片入口不放在这里：工具条窄时 MyGo 从末尾把放不下的按钮收进「»」
// 菜单，这两项恰好会是最先被藏起来的（见 editorPanelButtons）。
//
// 带 KeepFocus：按标记按钮时焦点仍留在输入框里，选区不动，插入的标记落在
// 光标处，插完可以直接接着打字；评论输入框也不会因为失焦而缩回一行。
func (a *app) editorToolbar(c *ui.Context, e *mdEditor, body *string) {
	ui.Toolbar(c, func() {
		for _, b := range markdownButtons {
			if a.editorIconButton(c, b.icon, b.label, b.label+"："+b.hint).Clicked() {
				e.applyMarkdown(body, b.format)
			}
		}
	}).Label("编辑工具").Grow(1).MinWidth(0).KeepFocus()
}

// editorPanelButtons 是「表情 / 图片」两个面板的入口，由调用方放在工具条
// 之外，永远可见。emojiLabel / imageLabel 由调用方给：详情页正文里已经有一个
// 「表情」表态按钮，评论工具条改用别的名字，免得读屏与点击测试分不清两个
// 入口。
func (a *app) editorPanelButtons(c *ui.Context, e *mdEditor, emojiLabel, imageLabel string) {
	if a.editorIconButton(c, reactionIcon, emojiLabel, "插入社区表情").Clicked() {
		e.togglePanel("emoji")
	}
	if a.editorIconButton(c, imageIcon, imageLabel, "拖入、选择或粘贴图片").Clicked() {
		e.togglePanel("image")
	}
}

// editorIconButton 画一个「图标 + 无障碍名 + 悬浮提示」的按钮，编辑器里所有
// 只显示图标的入口都走它：平时只有图标，鼠标扫过时给一层底色并弹出说明。
// label 同时作无障碍名与点击测试的命中名，tip 是悬浮时给出的说明。调用方
// 照常用返回值的 Clicked() 判断点击。
func (a *app) editorIconButton(c *ui.Context, icon *ui.SVG, label, tip string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "").
		Border(0, t.Border).
		TextColor(t.Text).
		FontSize(12).
		Padding(editorIconPadY, editorIconPadX).
		Radius(editorIconRadius).
		KeepFocus()
	// 悬停底色要自己给：按钮自带的 hover 样式只在背景没被改过时才生效，
	// 而这里要的是「没有边框、只有一圈浅底」的工具栏外观。
	b.Background(ui.Transparent)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(editorIconSize, editorIconSize).TextColor(t.Text)
	})
	return b.Label(label).Tooltip(tip)
}

// editorIconToggle 是编辑器里的二选一开关（编辑 / 预览）。选中的一项用底色
// 区分，未选中的一项鼠标扫过也给同样的底色；label 作无障碍名、点击测试的
// 命中名，tip 是悬浮说明。
func (a *app) editorIconToggle(c *ui.Context, on bool, icon *ui.SVG,
	label, tip string, onClick func()) {
	t := c.Theme()
	b := ui.Button(c, "").
		Border(0, t.Border).
		FontSize(12).
		Padding(editorIconPadY, editorIconPadX).
		Radius(editorIconRadius).
		KeepFocus()
	bg, fg := ui.Transparent, t.TextMuted
	if on || b.Hovered() {
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(bg).TextColor(fg)
	b.Children(func() {
		ui.Icon(c, icon).Size(editorIconSize, editorIconSize).TextColor(fg)
	})
	b.Label(label).Tooltip(tip)
	if b.Clicked() {
		onClick()
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
			// 网格带 KeepFocus：挑表情时焦点留在输入框里，插入的短码落在
			// 原来的光标处，挑完接着打字不用再点一次输入框。
			ui.Row(c).FillWidth().Gap(2).KeepFocus().Children(func() {
				for _, em := range row {
					// 格子里只画图，不画短码：短码是给人看进正文里的写法，
					// 摆在图标旁边只是噪声。名字与短码留在悬停提示里。
					// 格子定宽而不是均分整行：只有两三个表情时不会被拉成
					// 半屏一个，图标还是挨在一起好挑。
					cell := ui.Row(c).Size(emojiCellSize, emojiCellSize).
						AlignItems(ui.Center).Center().
						Radius(6).Background(ui.Transparent)
					if cell.Hovered() {
						cell.Background(t.SurfaceHover)
					}
					cell.Children(func() {
						// 缩略图走全应用共用的图片缓存：没下载好时先留一个等大
						// 的空位，下载完成后缓存会触发重绘，不会每帧发请求。
						if src := a.articleImage(em.ImgURL, imageCacheStable); src != nil {
							ui.Image(c, src).Size(20, 20).Fit(ui.Contain)
						} else {
							ui.Box(c).Size(20, 20).Radius(4).Background(t.SurfaceHover)
						}
					})
					cell.Label(em.Name).Tooltip("插入 " + emojiShortcode(em)).
						OnClick(func() { e.insertText(body, emojiShortcode(em)) })
				}
			})
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).KeepFocus().Children(func() {
			ui.Text(c, "共 "+strconv.Itoa(len(filtered))+" 个表情，显示 "+
				strconv.Itoa(len(shown))+" 个").
				FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
			if len(filtered) > e.emojiShown {
				if a.editorIconButton(c, chevronDownIcon, "显示更多表情",
					"再显示 "+strconv.Itoa(emojiPageSize)+" 个表情").Clicked() {
					e.emojiShown += emojiPageSize
				}
			}
			if a.editorIconButton(c, closeIcon, "收起表情", "收起表情面板").Clicked() {
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
// 入口只有图标，说明在悬浮提示里。
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
		ui.Text(c, "把图片直接拖进编辑区或粘贴进来，也可以用下面的按钮。支持 JPEG / PNG / WebP，单张上限在图库设置里改。").
			FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		// 当前图库不能用时先把原因说清楚：否则用户拖进图片只看到一句上传
		// 失败，还得自己去设置里猜是没登录、不是 Pro 还是没填 Token。
		if h := a.activeHost(); h != nil && !a.hostReady(h) {
			ui.Text(c, "当前图库不可用："+a.hostNotReadyReason(h)).
				FontSize(11).TextColor(th.Danger).MaxLines(2)
		}
		ui.Row(c).Gap(8).Wrap().KeepFocus().Children(func() {
			if a.editorIconButton(c, imageIcon, "选择图片",
				"从本地选择图片上传").Clicked() {
				a.pickImages(t)
			}
			if a.editorIconButton(c, clipboardIcon, "粘贴剪贴板图片",
				"把剪贴板里的图片上传并插入正文").Clicked() {
				a.pasteImages(t)
			}
			if a.editorIconButton(c, settingsIcon, "图库设置",
				"打开图库设置，配置图床与单张上限").Clicked() {
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

// editorUploadRow 是上传队列里的一行：文件名、状态，失败时可重试，随时可
// 从正文里移除。两个动作只画图标，悬浮提示带上文件名，一行里不会因为文案
// 长短把文件名挤掉。
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
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).KeepFocus().Children(func() {
		ui.Text(c, j.Name).FontSize(11).TextColor(t.Text).
			SingleLine().Grow(1).MinWidth(0)
		ui.Text(c, state).FontSize(11).TextColor(color).MaxLines(2)
		if j.State == uploadFailed {
			if a.editorIconButton(c, refreshIcon, "重试 "+j.Name,
				"重新上传 "+j.Name).Clicked() {
				a.retryUpload(j.ID)
			}
		}
		if a.editorIconButton(c, trashIcon, "移除 "+j.Name,
			"从正文里移除 "+j.Name).Clicked() {
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
//
// height 大于 0 时预览区固定这么高（页面里的评论框）；小于等于 0 表示由
// 外层决定（独立写评论窗口里与编辑区并排，跟着窗口一起长高）。
func (a *app) editorPreviewView(c *ui.Context, body string,
	emojis map[string]emojiInfo, o *markdown.Options, height float32) {
	t := c.Theme()
	sc := ui.Scroll(c).FillWidth()
	if height > 0 {
		sc.Height(height)
	} else {
		sc.Grow(1).MinHeight(0)
	}
	sc.Children(func() {
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

// replyPreviewFit 按草稿的长短给页面里预览区一个高度：短回复不必占掉
// 180 DIP 的空白，长回复也不会挤在一条缝里看。行数按每行约 34 个汉字估。
func replyPreviewFit(body string) float32 {
	if strings.TrimSpace(body) == "" {
		return replyPreviewMin
	}
	lines := 0
	for _, ln := range strings.Split(body, "\n") {
		lines += max(1, (utf8.RuneCountInString(ln)+33)/34)
	}
	h := float32(lines)*19 + 34
	return max(replyPreviewMin, min(h, replyPreviewMax))
}
