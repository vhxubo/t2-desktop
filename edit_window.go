package main

import (
	"context"
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 「编辑」窗口：改已发表的帖子（标题 + 正文）、改自己的评论、给帖子追加附言。
// 三种用途共用一扇窗口与一套编辑器，差别只有标题栏、有没有标题输入框和底部
// 按钮的文案（见 editState 的 windowTitle / submitLabel / hint）。
//
// 为什么是独立窗口而不是页面里的内联编辑：评论区是虚拟列表，行高变化会让滚动
// 位置乱跳；帖子正文在左栏里还要和右栏的评论对齐。窗口里左边写、右边按发表后
// 的样子预览，和「写评论」窗口是同一套手感，长文也改得下去。
//
// 数据、校验与请求在 edit.go；这里只负责窗口的开关与排版。

const (
	editWindowWidth  = 900
	editWindowHeight = 640
	editWinMinWidth  = 560
	editWinMinHeight = 420
	// editWindowPad 是窗口内容区与标题栏之间的内边距。
	editWindowPad = 10
	// 编辑区的最小 / 最大行数：正文长时输入框跟着长，但不超过这个高度，
	// 再长就在框里滚动。
	editEditorLines   = 14
	editEditorMaxLine = 34
)

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showTopicEdit 打开「编辑帖子」。入口是详情页正文区右下角的操作组，只在作者本人
// 时出现（见 canEditTopic）。
func (a *app) showTopicEdit(d *detailState) {
	if d == nil || d.topic == nil {
		return
	}
	a.publish(func() {
		st := newTopicEditState(d.topic, a.md)
		st.detail = d
		a.openEditWindow(st)
	})
}

// showReplyEdit 打开「编辑评论」。入口是评论底行的编辑图标，只在作者本人
// 时出现（见 canEditReply）。
func (a *app) showReplyEdit(d *detailState, r replyItem) {
	if d == nil || r.ID == "" {
		return
	}
	a.publish(func() {
		st := newReplyEditState(d.shortID, r, a.commentMd)
		st.detail = d
		a.openEditWindow(st)
	})
}

// showPostscript 打开「追加附言」。入口和「编辑帖子」在一起，同样只给作者。
func (a *app) showPostscript(d *detailState) {
	if d == nil || d.topic == nil {
		return
	}
	a.publish(func() {
		st := newPostscriptState(d.shortID, d.topic.Emojis, a.md)
		st.detail = d
		a.openEditWindow(st)
	})
}

// openEditWindow 在主线程上摆好状态并开窗。已经开着的窗口里有没保存的改动时
// 不覆盖它：先让用户保存或取消，免得一次误点丢掉正在改的正文。
func (a *app) openEditWindow(st *editState) {
	if st == nil {
		return
	}
	// a.edit 只在窗口开着时非空，所以「有状态且有改动」就等于「窗口里有没保存
	// 的正文」。
	if cur := a.edit; cur != nil && cur.changed() {
		a.editToast("还有未保存的修改，请先保存或取消")
		if win := a.editWin; win != nil {
			win.Focus()
		}
		return
	}
	a.edit = st
	// headless 测试没有主循环：碰 mygo 的窗口会等一个不存在的处理者，所以
	// 只把状态准备好，测试直接渲染 editWindowView。
	if a.win == nil {
		return
	}
	if win := a.editWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle(st.windowTitle()),
		Width:             editWindowWidth,
		Height:            editWindowHeight,
		MinWidth:          editWinMinWidth,
		MinHeight:         editWinMinHeight,
		StateKey:          "edit",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、发帖窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.editWindowView)),
	})
	a.editWin = win
	win.OnClosed(func() {
		a.editWin = nil
		a.authGateWindowClosed(toastEdit)
		// 窗口关掉就丢掉这份编辑：它是原文的一份快照，留着下次打开会盖掉
		// 页面上已经更新的内容。
		a.edit = nil
		if a.win != nil {
			a.win.Invalidate()
		}
		a.accountWindowClosed(win)
	})
}

// closeEditWindow 收起窗口并丢弃状态。窗口没开着时只清状态（headless 测试
// 走的就是这条）。
func (a *app) closeEditWindow() {
	a.edit = nil
	if win := a.editWin; win != nil {
		a.editWin = nil
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// redrawEditWindow 让窗口按最新状态重建（上传进度、保存状态）。只在主线程调用。
func (a *app) redrawEditWindow() {
	if win := a.editWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// editWindowView 是编辑窗口的 view。窗口自带标题栏与窗口按钮，所以这里不需要
// 遮罩，也不占主窗口的位置。
func (a *app) editWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里的保存结果与上传失败都弹在这里；窗口不在了就退回主窗口。
	defer a.drainToasts(c, a.claimsToasts(toastEdit))
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, a.submitEdit)
	c.OnShortcut(0, ui.KeyEscape, a.closeEditWindow)

	st := a.edit
	if st == nil {
		return
	}
	// 刚粘进来的图片在这里落地：这一帧就要把占位符写进正文（见 drainPaste）。
	a.drainPaste(uploadTargetEdit)

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口，右上角是最小化与关闭。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, editIcon).Size(15, 15).TextColor(t.Accent)
				ui.Text(c, st.windowTitle()).FontSize(13).Bold().SingleLine().
					TextColor(t.Text)
				if sub := editWindowSubtitle(st); sub != "" {
					ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.editWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeEditWindow)
		})
		ui.Divider(c)

		a.editWindowBody(c, st)

		ui.Divider(c)
		a.editWindowFooter(c, st)
	})
	// 右栏预览里的配图也能点开多图预览；预览是一扇独立窗口，这里只在图片
	// 到位后补开它（见 viewerTick）。
	a.viewerTick()
	// 缺权限时就在这个窗口里弹授权：保存 / 追加附言的请求是从这里发起的。
	a.authDialogFor(c, toastEdit)
}

// editWindowSubtitle 是标题栏里的一行：编辑评论时报评论 id 的前几位，帖子
// 与附言报帖子编号，让人一眼确认改的是哪一份内容。
func editWindowSubtitle(st *editState) string {
	if st.kind == editKindReply {
		return "评论 " + truncate(st.replyID, 8)
	}
	return "帖子 " + st.shortID
}

// editWindowBody 是窗口正文：编辑帖子时多一个标题输入框，然后是 Markdown
// 编辑区（左边写、右边预览）。
func (a *app) editWindowBody(c *ui.Context, st *editState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).
		Padding(6, editWindowPad).Gap(6).Children(func() {
		ui.Text(c, st.hint()).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		if st.kind == editKindTopic {
			a.editTitleField(c, st)
			a.editGuardField(c, st)
		}
		a.editEditorSection(c, st)
	})
}

// editTitleField 是编辑帖子时的标题输入框。编辑评论与附言没有标题，不画。
func (a *app) editTitleField(c *ui.Context, st *editState) {
	ui.Field(c, "标题", func() {
		ui.TextInput(c, &st.title).Label("标题").
			Placeholder("一句话说清这帖讲什么").FillWidth().
			MaxLength(composeTitleMax).Bind(&st.titleField)
	}).Description(fmt.Sprintf("%d / %d 字",
		utf8.RuneCountInString(st.title), composeTitleMax))
}

// editGuardField 是编辑帖子时的可见范围：默认停在「保持当前设置」，只有作者
// 主动换选项才发 access_guard_* 字段。读响应没带当前门槛时也保留这一项，
// 避免改个标题就把网页端设的限制清掉。
func (a *app) editGuardField(c *ui.Context, st *editState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "可见范围").FontSize(11).TextColor(t.TextMuted)
			ui.Select(c, &st.guardChoice, editGuardLabels).Label("可见范围").Width(180)
			switch guardFromEditLabel(st.guardChoice) {
			case guardLevel:
				ui.NumberInput(c, &st.guardLevel,
					composeGuardLevelLo, composeGuardLevelHi, 1).Label("最低可见等级").Width(120)
				ui.Text(c, "级及以上").FontSize(11).TextColor(t.TextMuted)
			case guardAttribute:
				labels := make([]string, 0, len(d20Attrs))
				for _, at := range d20Attrs {
					labels = append(labels, at.Label)
				}
				ui.Select(c, &st.guardAttr, labels).Label("D20 属性").Width(160)
				ui.NumberInput(c, &st.guardAttrValue, 0, composeGuardAttrHi, 1).
					Label("属性下限").Width(120)
			}
		})
		hint := "选「所有人可见」会清除现有门槛；停在「保持当前设置」则不改可见范围。"
		if st.guardKnown {
			hint = "当前：" + editGuardSummary(st.origGuard) + "。" + hint
		}
		ui.Text(c, hint).FontSize(10).TextColor(t.TextMuted).MaxLines(2)
	})
}

// editGuardSummary 是一行「当前可见范围」的说明。属性名缺失时只报属性门槛，
// 不编一个空名字。
func editGuardSummary(g guardSelection) string {
	switch g.Type {
	case guardNone:
		return "所有人可见"
	case guardLogin:
		return "登录用户可见"
	case guardLevel:
		return fmt.Sprintf("Lv.%d 及以上可见", g.Level)
	case guardAttribute:
		if g.Attr == "" {
			return "按 D20 属性可见"
		}
		name := d20AttrLabel(g.Attr)
		if name == "" {
			name = g.Attr
		}
		return fmt.Sprintf("%s ≥ %d 可见", name, g.AttrValue)
	}
	return "未知"
}

// editEditorSection 是正文区：工具条、按需展开的表情 / 图片面板，然后是
// 左边写、右边预览的两栏。整个编辑区也是拖放区，图片直接拖进来就能上传。
func (a *app) editEditorSection(c *ui.Context, st *editState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Gap(6).Children(func() {
		// 工具条与面板入口固定在上面，滚动正文时始终可见。
		ui.Row(c).FillWidth().Gap(2).AlignItems(ui.Center).KeepFocus().
			Children(func() {
				a.editorToolbar(c, &st.mdEditor, &st.body)
				a.editorPanelButtons(c, &st.mdEditor, &st.body, st.emojis, "插入表情", "插入图片")
				a.editorIconToggle(c, st.winPreview, eyeIcon, "预览",
					"显示 / 收起右侧预览", func() { st.winPreview = !st.winPreview })
				ui.Textf(c, "%d / %d 字", utf8.RuneCountInString(st.body), composeContentMax).
					FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		a.editorPanels(c, uploadTargetEdit, &st.mdEditor, &st.body, st.emojis)

		box := ui.Column(c).FillWidth().Grow(1).MinHeight(0)
		over := a.editorDropZone(box, uploadTargetEdit)
		if over {
			box.Background(t.SurfaceHover)
		}
		box.Children(func() {
			ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(editWindowPad).
				AlignItems(ui.Stretch).Children(func() {
				// 左栏：输入框占满整栏高度。
				ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
					Padding(4, 8).Radius(10).Border(1, t.Border).
					Background(t.Background).Children(func() {
					c.Key("edit-body-area")
					area := ui.TextArea(c, &st.body).Label("正文内容").
						Border(0, t.Border).Background(ui.Transparent).FillWidth().
						Lines(editEditorLines, editEditorMaxLine).
						Placeholder("支持 Markdown：**加粗**、# 标题、> 引用、- 列表、`代码`、[链接](url)、![图片](url) 与 :smile: 表情；图片可直接拖进来或粘贴").
						OnPaste(func(text string) bool {
							return a.pasteIntoEditor(uploadTargetEdit, text)
						}).
						Bind(&st.bodyField)
					st.syncSelection(area)
					// 编辑评论 / 附言打开就落在正文里：没有标题要填。
					if st.focusBody {
						st.bodyField.Focus(c)
						st.focusBody = false
					}
				})
				if st.winPreview {
					// 右栏：按发表后的样子实时渲染，跟着窗口高度长。
					ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
						Radius(10).Border(1, t.Border).
						Background(t.Surface).Children(func() {
						a.editorPreviewView(c, st.body,
							mergeEmojiMaps(a.emojis, st.emojis), st.mdOpts, 0)
					})
				}
			})
			if over {
				ui.Text(c, "松开即可把图片插入正文").FontSize(11).
					TextColor(t.Accent).Padding(4, 2)
			}
		})
	})
}

// editWindowFooter 是底部一行：左边是保存进度（没有改动时说明一句），右边是
// 取消与主按钮。保存失败的原因不落在页面上，用窗口内 toast 说明（见 submitEdit）。
func (a *app) editWindowFooter(c *ui.Context, st *editState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, editWindowPad).Gap(10).
		AlignItems(ui.Center).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			if st.busy {
				lobeSpin(c, feedback.SpinProps{CustomSize: 14})
				ui.Text(c, "正在保存…").FontSize(11).TextColor(t.TextMuted).SingleLine()
			} else if !st.changed() {
				ui.Text(c, "还没有改动").FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		if ui.Button(c, "取消").Clicked() {
			a.closeEditWindow()
		}
		save := ui.PrimaryButton(c, st.submitLabel()).
			Disabled(st.busy || !st.changed())
		if save.Clicked() {
			a.submitEdit()
		}
	})
}

/* ------------------------------------------------------------------
   提交
---------------------------------------------------------------- */

// submitEdit 校验并提交。失败（含校验不通过）只在窗口底部弹一条 toast：
// 编辑区保持原样，改完可以直接再存一次。
func (a *app) submitEdit() {
	st := a.edit
	if st == nil || st.busy {
		return
	}
	if !a.auth.loggedIn() {
		a.editToast("请先登录后再编辑")
		return
	}
	payload, err := st.payload()
	if err != nil {
		a.editToast(err.Error())
		return
	}
	if !st.changed() {
		a.editToast("没有改动")
		return
	}
	// 附言的幂等键跟着正文走：授权重跑与超时重试复用同一个键，不会追加两条。
	key := st.postscriptKey()
	st.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		err := st.submit(ctx, a.client, payload, key)
		a.publish(func() {
			if st != a.edit {
				return // 窗口已经关掉：结果只影响服务端，页面状态不再改
			}
			st.busy = false
			if err != nil {
				// 窗口已过 / 机会已用掉：隐藏入口并收起窗口，不重试。这是
				// 400 edit_window_expired，不是参数错误也不是缺权限。
				if isEditWindowExpired(err) {
					if st.detail != nil && st.detail.topic != nil {
						st.detail.topic.CanEdit = false
						st.detail.topic.CanEditKnown = true
						a.redrawDetailFor(st.detail)
					}
					a.closeEditWindow()
					a.editToast("可修改时间已过，入口已关闭")
					return
				}
				// 缺权限时先补授权，回调成功后按同样的内容重发一次，用户不用
				// 重新输入。
				a.handleAuthErrorFor(toastEdit, err)
				a.editToast(friendlyError(err))
				return
			}
			a.applyEdit(st)
			// 先关窗口再发提示：窗口关掉后提示按规则退回主窗口，不会落在一个
			// 正在销毁的窗口上。
			a.closeEditWindow()
			a.editToast(st.doneText())
		})
	}()
}
