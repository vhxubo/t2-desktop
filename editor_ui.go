package main

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 发帖窗口与评论输入框共用的编辑器界面：一条 Markdown 工具条、贴着入口的表情
// Popover、盖在整页上的 Lobe Modal（插入帖子 / Base64 编码）、一个内联的图片面板，
// 以及按文章正文方式渲染的预览。
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
	// topicPanelMaxH 是插入帖子列表的高度上限：20 行一次铺满会盖住正文，
	// 超过就在这个高度里滚。
	topicPanelMaxH float32 = 220
	// topicDialogWidth 是「插入帖子」Modal 的宽度：行里带节点名与发帖日期，
	// 留够位置；窄窗口里 Modal 自己会收到窗口宽减 32。
	topicDialogWidth float32 = 560
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
		// Base64 编码不走 applyMarkdown：它要先在 Lobe Modal 里输入原文，
		// 确认后才把编码串插进正文（见 editorBase64Dialog）。打开时若正文
		// 里有选区，直接拿选区当原文，省得再抄一遍。
		if a.editorIconButton(c, base64Icon, "Base64 编码",
			"把选中的文字或输入的原文编成 Base64 片段").Clicked() {
			e.base64Input = editorSelectedText(e, *body)
			e.togglePanel("base64")
		}
	}).Label("编辑工具").Grow(1).MinWidth(0).KeepFocus()
}

// editorSelectedText 返回当前选区里的文字，没有选区时返回空串。选区按 rune 存，
// 这里按 rune 切，别把中文截成半个字符。
func editorSelectedText(e *mdEditor, body string) string {
	rs, a, b := e.runeSel(body)
	if b <= a {
		return ""
	}
	return string(rs[a:b])
}

// editorPanelButtons 是「表情 / 插入帖子 / 图片」三个入口，由调用方放在工具条
// 之外，永远可见。表情是贴着入口的 Lobe Popover（轻，点开就能挑），插入帖子是
// Lobe Modal（搜索列表需要地方），图片仍是内联面板。body / extra 是表情要插入
// 的正文与当前帖子的社区表情表；emojiLabel / imageLabel 由调用方给：详情页正文
// 里已经有一个「表情」表态按钮，评论工具条改用别的名字，免得分不清两个入口。
func (a *app) editorPanelButtons(c *ui.Context, e *mdEditor, body *string,
	extra map[string]string, emojiLabel, imageLabel string) {
	emojiBtn := a.editorIconButton(c, reactionIcon, emojiLabel, "插入社区表情")
	// 气泡关着时每个编辑器都把 Popover 建出来，否则那一次点击传不进去；
	// 开着时也只画这一个，开合由 Lobe 自己管。
	lobeoverlay.Popover(c, emojiBtn, &e.emojiOpen, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		a.editorEmojiPanel(c, e, body, extra)
	})
	if emojiBtn.Clicked() {
		// Lobe 在构建期就切了 emojiOpen，这里只收起其它面板。
		e.imageOpen = false
		e.topicOpen = false
		e.base64Open = false
		e.emojiShown = emojiPageSize
	}
	if a.editorIconButton(c, myTopicIcon, "插入帖子", "从自己发过的帖子里选一篇插入链接").Clicked() {
		e.togglePanel("topic")
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

// editorPanels 画当前打开的那个面板。「插入帖子」与 Base64 编码是盖在整页上的
// Lobe Modal，图片是展开在输入框下方的内联面板；表情走贴着入口的 Popover（见
// editorPanelButtons），不在这里画。四者互斥，由 togglePanel 保证。
func (a *app) editorPanels(c *ui.Context, t uploadTarget, e *mdEditor,
	body *string, extra map[string]string) {
	if e.imageOpen {
		a.editorImagePanel(c, t, e)
		return
	}
	if e.topicOpen {
		a.ensureMyTopicPicker(t, e)
		a.editorTopicDialog(c, t, e, body)
		return
	}
	if e.base64Open {
		a.editorBase64Dialog(c, e, body)
	}
}

// editorBase64Dialog 是工具条「Base64 编码」的 Lobe Modal：输入框里填原文，
// 下面实时给出将要插入的编码串。点「编码并插入」才写进正文；取消、
// Esc 或点窗外都只关窗，不动正文。打开时若带进了选区，确认后编码串替换选区，
// 光标落在片段末尾（insertText 的行为）。
func (a *app) editorBase64Dialog(c *ui.Context, e *mdEditor, body *string) {
	ok := feedback.Modal(c, &e.base64Open, feedback.ModalProps{
		Title:      "Base64 编码",
		Width:      base64DialogWidth,
		OkText:     "编码并插入",
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			display.Text(c, base64HintText, display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 2,
			})
			ui.TextInput(c, &e.base64Input).Label("原文").
				Placeholder("输入要编码的文字，或先在正文里选中一段").Grow(1).MinWidth(0)
			a.base64DialogPreview(c, e.base64Input)
		})
	})
	if !ok {
		return
	}
	if strings.TrimSpace(e.base64Input) != "" {
		e.insertText(body, encodeBase64Text(e.base64Input))
	}
	e.base64Input = ""
	e.base64Open = false
}

// editorTopicDialog 是「插入帖子」选择器：搜索框按标题过滤（GET /v1/me/topics
// 的 q），出来的行点一下就把 Markdown 链接写进正文并关窗。它以 Lobe Modal 盖在
// 整页上，标题栏与 Esc / 点窗外关闭由 Modal 提供。未登录时给一个明确的登录入口，
// 不摆一个点了没反应的空面板。
func (a *app) editorTopicDialog(c *ui.Context, t uploadTarget, e *mdEditor, body *string) {
	feedback.Modal(c, &e.topicOpen, feedback.ModalProps{
		Title:    "插入帖子",
		Width:    topicDialogWidth,
		NoFooter: true,
	}, func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).KeepFocus().Children(func() {
				// 绑定值写回状态后，ensureMyTopicPicker 下一帧会发现搜索词变了并
				// 防抖重拉；这里不用再监听 Changed，免得同一次输入发两次请求。
				ui.TextInput(c, &e.topicQuery).Label("搜索我的帖子").
					Placeholder("按标题找一篇自己发过的帖子").Grow(1).MinWidth(0)
				if a.editorIconButton(c, refreshIcon, "搜索我的帖子",
					"按当前标题关键字重新搜索").Clicked() {
					a.reloadMyTopicPicker(t, e, e.topicQuery)
				}
			})
			if !a.auth.loggedIn() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					display.Text(c, "登录 2Libra 后才能插入自己发过的帖子。", display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 2,
					}).Grow(1)
					if action.Button(c, "登录", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.requestLoginFor(a.editorToastTarget(t), "登录后才能插入自己发过的帖子。", func() {
							e.topicTriedAt = time.Time{}
							a.ensureMyTopicPicker(t, e)
						})
					}
				})
				return
			}
			if e.topicErr != "" {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					display.Text(c, "加载我的帖子失败："+e.topicErr, display.TextProps{
						Type: display.TextWarning, FontSize: 11, Rows: 2,
					}).Grow(1)
					if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.reloadMyTopicPicker(t, e, e.topicQuery)
					}
				})
			}
			if e.topicLoading && len(e.topicItems) == 0 {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
					display.Text(c, "正在加载我的帖子…", display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				})
			}
			if e.topicLoaded && len(e.topicItems) == 0 {
				display.Text(c, "没有找到标题匹配的帖子。换个关键字，或清空搜索看最新的 20 篇。",
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
				return
			}
			if len(e.topicItems) == 0 {
				return
			}
			items := make([]display.ListItem, 0, len(e.topicItems))
			for _, p := range e.topicItems {
				items = append(items, display.ListItem{
					Key:   p.ShortID,
					Label: p.Title,
					Extra: myTopicPickMeta(p),
				})
			}
			// KeepFocus：点帖子行时不把焦点从输入框抢走，选完关窗后光标还在
			// 原来的位置（和表情面板的网格行同一个道理）。
			ui.Scroll(c).MaxHeight(topicPanelMaxH).KeepFocus().Children(func() {
				if key := display.List(c, display.ListProps{
					Items: items, Compact: true,
				}); key != "" {
					for _, p := range e.topicItems {
						if p.ShortID != key {
							continue
						}
						e.insertText(body, myTopicPickLink(p))
						e.topicOpen = false
						break
					}
				}
			})
		})
	})
}

// myTopicPickMeta 是选择器行右侧的次要信息：节点名与发帖日期，省得只看标题
// 分不清两篇同名帖子。字段缺失时只显示剩下的那一段。
func myTopicPickMeta(p myTopicPick) string {
	parts := make([]string, 0, 2)
	if p.NodeName != "" {
		parts = append(parts, p.NodeName)
	}
	if t, ok := profileTime(p.CreatedAt); ok {
		parts = append(parts, t.Local().Format("2006-01-02"))
	}
	return strings.Join(parts, " · ")
}

// ------------------------------------------------------------------
// 表情面板
// ------------------------------------------------------------------

// editorEmojiPanel 是表情气泡的内容：顶部切换「社区表情 / 系统表情」，两边共用
// 搜索与分页状态，关闭按钮与 Esc / 点窗外关闭由外层 Popover 管。选中的表情直接
// 插进调用方的正文，面板随即收起，光标留在原处。
func (a *app) editorEmojiPanel(c *ui.Context, e *mdEditor, body *string,
	extra map[string]string) {
	t := c.Theme()
	ui.Column(c).Width(420).Padding(8, 10).Gap(6).
		Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Segmented(c, &e.emojiTab, "社区表情", "系统表情").
				Label("表情类型").Changed() {
				// 两个页签的搜索词与分类互不通用，切过去先清空，免得上一页的
				// 关键词在新页里搜出「没有匹配的表情」。
				e.emojiQuery = ""
				e.emojiCategory = systemEmojiAll
				e.emojiShown = emojiPageSize
			}
			ui.Spacer(c).Grow(1)
			if a.editorIconButton(c, closeIcon, "收起表情", "收起表情气泡").Clicked() {
				e.emojiOpen = false
			}
		})
		if e.emojiTab == 0 {
			a.editorCommunityEmojiPanel(c, e, body, extra)
		} else {
			a.editorSystemEmojiPanel(c, e, body)
		}
	})
}

// editorCommunityEmojiPanel 合并全局表情表与当前帖子自带的社区表情，按短码
// 排序后分批展示。插入的是 :code: 短码而不是图片链接：服务端正文认短码，
// 预览与详情页正文会用同一套 replaceEmoji 把它换成行内图片。
func (a *app) editorCommunityEmojiPanel(c *ui.Context, e *mdEditor, body *string,
	extra map[string]string) {
	t := c.Theme()
	list := editorEmojis(a.emojis, extra)
	community := editorCommunityCodes(extra)
	filtered := editorFilteredEmojis(list, e.emojiQuery, e.emojiOnlyPost, community)
	shown := filtered
	if len(shown) > e.emojiShown {
		shown = shown[:e.emojiShown]
	}
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
					OnClick(func() {
						e.insertText(body, emojiShortcode(em))
						e.emojiOpen = false
					})
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
	})
}

// editorSystemEmojiPanel 画内置的 1898 个系统表情：分类筛选 + 英文搜索 +
// 网格点选。点一格就把 Unicode 字符直接插进正文，跟线上 emoji-picker-react
// 的行为一致，不依赖网络，也不走 :code: 短码。
func (a *app) editorSystemEmojiPanel(c *ui.Context, e *mdEditor, body *string) {
	t := c.Theme()
	filtered := filteredSystemEmojis(e.emojiQuery, e.emojiCategory)
	shown := filtered
	if len(shown) > e.emojiShown {
		shown = shown[:e.emojiShown]
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, &e.emojiQuery).Label("搜索表情").
			Placeholder("英文名称，如 smile / grin").Grow(1).MinWidth(0)
		ui.Select(c, &e.emojiCategory, systemEmojiOptions()).Label("分类")
	})
	if len(shown) == 0 {
		ui.Text(c, "没有匹配的表情。换个英文关键词，或把分类切回「全部」。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	}
	for start := 0; start < len(shown); start += emojiColumns {
		row := shown[start:min(start+emojiColumns, len(shown))]
		ui.Row(c).FillWidth().Gap(2).KeepFocus().Children(func() {
			for _, em := range row {
				name := strings.SplitN(em.Names, "|", 2)[0]
				cell := ui.Row(c).Size(emojiCellSize, emojiCellSize).
					AlignItems(ui.Center).Center().
					Radius(6).Background(ui.Transparent)
				if cell.Hovered() {
					cell.Background(t.SurfaceHover)
				}
				cell.Children(func() {
					ui.Text(c, em.Char).FontSize(20).SingleLine()
				})
				cell.Label(name).Tooltip("插入 " + em.Char + " " + name).
					OnClick(func() {
						e.insertText(body, em.Char)
						e.emojiOpen = false
					})
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
	})
}

// ------------------------------------------------------------------
// 图片面板
// ------------------------------------------------------------------

// editorImagePanel 是三条上传入口的说明与任务列表：切图库、选文件、粘贴
// 剪贴板，以及每一张图的排队 / 上传 / 完成 / 失败状态与重试、移除。
// 入口只有图标，说明在悬浮提示里；右上角的叉号与表情面板一致，用来收起面板。
func (a *app) editorImagePanel(c *ui.Context, t uploadTarget, e *mdEditor) {
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
			// 收起面板：与表情面板同一个叉号，两个面板的关闭方式一致。
			if a.editorIconButton(c, closeIcon, "收起图片", "收起图片面板").Clicked() {
				e.imageOpen = false
			}
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
