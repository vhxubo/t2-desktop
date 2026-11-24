package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// 「修改节点」窗口：发帖后 10 分钟内、作者本人可把帖子挪到另一个节点，机会
// 只有一次。窗口和编辑正文完全独立——入口由 can_edit_node 决定，请求走
// POST /v1/topics/:short_id/node（见 node_edit.go），这里只负责选节点与提交。

const (
	nodeEditWindowWidth  = 520
	nodeEditWindowHeight = 460
	nodeEditMinWidth     = 400
	nodeEditMinHeight    = 340
	nodeEditPad          = 14
)

// showTopicNodeEdit 打开「修改节点」。入口只在 can_edit_node 为 true 时出现；
// 匿名帖的真实发布者也会拿到这个字段，所以不能按昵称拦截。
func (a *app) showTopicNodeEdit(d *detailState) {
	if !a.canEditNode(d) {
		return
	}
	a.publish(func() {
		st := newNodeEditState(d)
		if st != nil {
			a.openNodeEditWindow(st)
		}
	})
}

func (a *app) openNodeEditWindow(st *nodeEditState) {
	if st == nil {
		return
	}
	a.nodeEdit = st
	a.ensureNodeCatalog()
	// headless 测试没有主循环：只把状态备好，测试直接渲染 nodeEditWindowView。
	if a.win == nil {
		return
	}
	if win := a.nodeEditWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("修改节点"),
		Width:             nodeEditWindowWidth,
		Height:            nodeEditWindowHeight,
		MinWidth:          nodeEditMinWidth,
		MinHeight:         nodeEditMinHeight,
		StateKey:          "topic-node",
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.nodeEditWindowView)),
	})
	a.nodeEditWin = win
	win.OnClosed(func() {
		a.nodeEditWin = nil
		a.nodeEdit = nil
		a.authGateWindowClosed(toastNodeEdit)
		if a.win != nil {
			a.win.Invalidate()
		}
		a.accountWindowClosed(win)
	})
}

func (a *app) closeNodeEditWindow() {
	a.nodeEdit = nil
	if win := a.nodeEditWin; win != nil {
		a.nodeEditWin = nil
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *app) redrawNodeEditWindow() {
	if win := a.nodeEditWin; win != nil {
		win.Invalidate()
	}
}

func (a *app) nodeEditWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	defer a.drainToasts(c, a.claimsToasts(toastNodeEdit))
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, a.submitNodeEdit)
	c.OnShortcut(0, ui.KeyEscape, a.closeNodeEditWindow)

	st := a.nodeEdit
	if st == nil {
		return
	}
	// 节点表变化很少，ensure 自己带单飞与失败冷却；视图每帧调也不会刷请求。
	a.ensureNodeCatalog()

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.Hash()).Size(15, 15).TextColor(t.Accent)
				ui.Text(c, "修改节点").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.nodeEditWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeNodeEditWindow)
		})
		ui.Divider(c)

		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(nodeEditPad, nodeEditPad).
			Gap(10).Children(func() {
			ui.Text(c, "当前节点："+st.current.label()).FontSize(12).
				TextColor(t.TextMuted).SingleLine()
			nodeCascader(c, a.nodeCatalogNodes(), nodeCascadeProps{
				Label:      "目标节点",
				Value:      &st.target,
				Open:       &st.open,
				ActiveRoot: &st.activeRoot,
				Search:     &st.search,
				ListHeight: 300,
				OnPick: func(f nodeFilter) {
					// nodeCascader 先写 Value 再回调，这里已经拿不到旧值；选一次
					// 就铸新键，保证同一个键不会配两个不同的 node_id。
					st.key, st.keySig = "", ""
					st.target = f
				},
			})
			ui.Text(c, "发帖后 10 分钟内仅可修改一次").FontSize(11).TextColor(t.TextMuted)
		})

		ui.Divider(c)
		ui.Row(c).FillWidth().Padding(nodeEditPad, 10).Gap(8).
			AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "取消").Clicked() {
				a.closeNodeEditWindow()
			}
			save := ui.PrimaryButton(c, "确定").
				Disabled(st.busy || st.sameNode() || st.target.Ref.ID == "")
			if save.Clicked() {
				a.submitNodeEdit()
			}
		})
	})
	a.authDialogFor(c, toastNodeEdit)
}
