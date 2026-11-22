package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 命令面板：标题栏输入框点击即展开，默认在「命令」模式，列出注册表里的全部
// 命令；切到「搜索」模式才是原来的搜索窗口入口。输入框用闪电作前导图标，有
// 内容时右侧出现一键清空按钮。命令本身不重复实现动作，只是调用注册时的 Run。
//
// 输入框的文字存在 palette.text：命令模式下它是过滤词，搜索模式下它是关键词。
// 回车成功后文字交给搜索窗口（a.search.q）并就地清空；切回空输入时也自动回到
// 默认的命令模式。没有过滤词时列表按「收藏 / 最近使用 / 分类」分组；一输入
// 过滤词就摊平显示命中项，顺序仍是注册顺序。

const (
	paletteWidth     float32 = 360 // 下拉面板宽度，略宽于标题栏搜索框
	paletteMaxHeight float32 = 360 // 列表最高，超出就滚动，不顶到内容区
	// titleSearchPlaceholder 是输入框聚焦且为空时显示的左对齐占位文案。未
	// 聚焦时改用居中的 titleSearchHint（见 main.go），两者不会同时出现。
	titleSearchPlaceholder = "搜索命令、帖子和用户..."
)

// titleSearchField 构建标题栏命令面板的输入框。mygo 自带 SearchField 把放大镜
// 图标写死，这里自己拼同样的结构，把前导图标换成闪电，呼应「命令面板」；有
// 内容时右侧出现一键清空按钮。
//
// 可访问名和几何都放在输入元素上，与原 SearchField 一致：测试和读屏拿到的都是
// 整条输入框的边界，点击也直接落在输入上。返回的 box 是包在外面的定位容器，
// 作面板锚点与内外点击判定，让面板里的按钮、清空按钮都算「在里面」。
func (a *app) titleSearchField(c *ui.Context, width, height float32) (ui.Element, ui.Element) {
	tk := lobetheme.TokensOf(c)
	t := c.Theme()
	box := ui.Row(c).Width(width).Height(height)
	var in ui.Element
	box.Children(func() {
		// 输入本身就是字段盒子：0 宽边框禁掉聚焦时的 Accent 描边。
		in = ui.TextInput(c, &a.palette.text).
			Width(width).Height(height).
			Padding(0, 26, 0, titleSearchIconInset).
			Background(tk.FillTertiary).Border(0, ui.Transparent).Radius(tk.BorderRadiusSM).
			FocusRing(false).Label("搜索关键词")
		// 前导闪电图标：绝对定位在左侧，IgnoreInput 让点击透到下面的输入。
		ui.Row(c).Absolute().Left(7).Top(0).Bottom(0).Width(15).
			Center().IgnoreInput(true).Children(func() {
			ui.Icon(c, lucide.Zap()).Label("闪电").Size(15, 15).TextColor(tk.TextTertiary)
		})
		// 有内容时右侧出现一键清空按钮。KeepFocus 让点击后焦点还在输入里，
		// 不会把命令面板误关掉。
		if a.palette.text != "" {
			clear := ui.ButtonBase(c).Absolute().Right(6).Top(0).Bottom(0).Width(16).
				Radius(8).Background(t.TextMuted.Alpha(0.5)).Label("Clear").KeepFocus()
			clear.Children(func() {
				ui.Icon(c, lucide.X()).Size(10, 10).TextColor(t.Background).Shrink(0)
			})
			clear.OnClick(func() {
				a.palette.text = ""
				a.palette.selected = 0
				a.palette.mode = paletteModeCommands
				a.palette.input.Focus(c)
			})
		}
	})
	return in, box
}

const (
	paletteModeCommands = iota
	paletteModeSearch
)

var paletteModeLabels = []string{"命令", "搜索"}

// paletteCommand 是面板里的一条命令，由注册表的定义加上当前偏好生成。
// keywords 是拼音 / 英文 / 别名，让中文标题也能被拉丁字母搜到；shortcut 是
// 当前生效的键位文字，直接显示在行尾。
type paletteCommand struct {
	id       string
	title    string
	hint     string
	keywords string
	category string
	shortcut string
	favorite bool
	icon     *ui.SVG
	run      func()
}

// paletteRow 是一条可显示的命令；heading 非空时先画一个分组标题。选中下标
// 只数命令，不数标题，这样 ↑↓ 与回车始终对得上。
type paletteRow struct {
	heading string
	cmd     paletteCommand
}

// paletteState 是命令面板跨帧保存的状态：面板是否展开、当前高亮哪一项、当前
// 处在哪种模式，以及输入框的文字。文字与搜索窗口的 q 分开存：回车成功后这边
// 清空，搜索词留在搜索窗口里，不会跟着一起被清掉。
type paletteState struct {
	open         bool
	selected     int
	mode         int
	text         string    // 标题栏输入框的文字
	wasFocused   bool      // 上一帧输入框是否聚焦：聚焦时展开，失焦时收起
	suppressOpen bool      // 面板刚因失焦/外部点击关掉，忽略随后焦点回到锚点造成的重开
	input        ui.Handle // 输入框身份，切模式后把焦点还给它，好接着输入
	// focusTarget 是 FocusBind 绑定的期望焦点：1 是输入框，0 表示输入框没有
	// 焦点。面板收起后用它确认光标已随面板一起隐藏。
	focusTarget int
	// blurPending 标记下一次构建要把焦点从输入框挪到字段盒子。mygo 关闭弹出
	// 层时会把焦点还给它打开的锚点（输入框），仅清焦点会被 restoreFocus 还原；
	// 把焦点交给仍在场上的盒子就不会被还原，光标也随之消失。
	blurPending bool
}

// paletteCommands 从注册表取当前可用的命令，并附上收藏状态与键位文字。
// 顺序就是注册顺序；可用性（例如登录态）在注册表里已经过滤过。
func (a *app) paletteCommands() []paletteCommand {
	defs := a.commands.available()
	out := make([]paletteCommand, 0, len(defs))
	if a.commands == nil {
		return out
	}
	favorites := map[string]bool{}
	if a.commands.prefs != nil {
		for _, id := range a.commands.prefs.snapshot().Favorites {
			favorites[id] = true
		}
	}
	for _, def := range defs {
		out = append(out, paletteCommand{
			id: def.ID, title: def.Title, hint: def.Hint, keywords: def.Keywords,
			category: def.Category, shortcut: a.commands.bindingLabel(def.ID),
			favorite: favorites[def.ID], icon: def.Icon, run: def.Run,
		})
	}
	return out
}

// filterPalette 按过滤词筛命令。标题、说明、分组名与 keywords 都参与匹配，
// 大小写不敏感；空词返回全量（保持注册顺序）。
func filterPalette(cmds []paletteCommand, q string) []paletteCommand {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return cmds
	}
	out := make([]paletteCommand, 0, len(cmds))
	for _, c := range cmds {
		hay := strings.ToLower(c.title + " " + c.hint + " " + c.keywords + " " + c.category)
		if strings.Contains(hay, q) {
			out = append(out, c)
		}
	}
	return out
}

// paletteRows 决定面板里显示哪些行。有过滤词时摊平命中项；没有过滤词时先
// 显示收藏与最近使用，剩下的按分类分组，每条命令只出现一次。
func (a *app) paletteRows(q string) []paletteRow {
	cmds := a.paletteCommands()
	if strings.TrimSpace(q) != "" {
		filtered := filterPalette(cmds, q)
		rows := make([]paletteRow, 0, len(filtered))
		for _, cmd := range filtered {
			rows = append(rows, paletteRow{cmd: cmd})
		}
		return rows
	}

	byID := make(map[string]paletteCommand, len(cmds))
	for _, cmd := range cmds {
		byID[cmd.id] = cmd
	}
	var favorites, recent []string
	if a.commands != nil && a.commands.prefs != nil {
		snap := a.commands.prefs.snapshot()
		favorites, recent = snap.Favorites, snap.Recent
	}
	rows := make([]paletteRow, 0, len(cmds))
	shown := make(map[string]bool, len(cmds))
	add := func(id, heading string) {
		cmd, ok := byID[id]
		if !ok || shown[id] {
			return
		}
		shown[id] = true
		rows = append(rows, paletteRow{heading: heading, cmd: cmd})
	}
	for _, id := range favorites {
		add(id, "收藏")
	}
	for _, id := range recent {
		if len(rows) >= len(cmds) {
			break
		}
		add(id, "最近使用")
	}
	for _, category := range commandCategories {
		for _, cmd := range cmds {
			if cmd.category != category {
				continue
			}
			add(cmd.id, category)
		}
	}
	// 插件注册的自定义命令可能用了新的分类名，统一收在「工具」后面。
	for _, cmd := range cmds {
		add(cmd.id, commandCategoryTools)
	}
	return rows
}

// paletteMove 让 ↑ / ↓ 在过滤后的列表里移动高亮，首尾循环。搜索模式没有列表，
// 这些键不做事，交给文本框移动光标。
func (a *app) paletteMove(q string, delta int) {
	if a.palette.mode != paletteModeCommands {
		return
	}
	rows := a.paletteRows(q)
	if len(rows) == 0 {
		return
	}
	a.palette.selected = (a.palette.selected + delta + len(rows)) % len(rows)
}

// paletteRun 处理回车：命令模式执行高亮的命令，搜索模式打开搜索窗口。
func (a *app) paletteRun(q string) bool {
	if a.palette.mode == paletteModeSearch {
		a.commitTitleSearch(q)
		return true
	}
	rows := a.paletteRows(q)
	if len(rows) == 0 {
		// 没有匹配的命令：把这个词当成关键词，直接进搜索窗口。
		a.commitTitleSearch(q)
		return true
	}
	i := a.palette.selected
	if i < 0 || i >= len(rows) {
		i = 0
	}
	a.runCommandByID(rows[i].cmd.id)
	return true
}

// closePalette 收起面板：清空输入、高亮与模式回到默认（命令模式）。
func (a *app) closePalette() {
	a.palette.open = false
	a.palette.text = ""
	a.palette.selected = 0
	a.palette.mode = paletteModeCommands
	// 执行命令后保持收起：焦点还在输入框里也别重新展开（mygo 会把焦点还给
	// 锚点），要再点一次输入框才重新打开。
	a.palette.wasFocused = false
	a.palette.suppressOpen = true
	a.palette.blurPending = true
}

// commitTitleSearch 把标题栏输入交给搜索窗口并清空输入框、收起面板。搜索词留
// 在 a.search.q，标题栏这边回到空白，不会连带把搜索结果清掉。
func (a *app) commitTitleSearch(q string) {
	if a.search == nil {
		a.search = newSearchState()
	}
	a.search.q = q
	a.closePalette()
	a.showSearch()
}

// toggleFavoriteCommand 切换命令收藏，结果立即落盘并让下一帧按新分组重排。
func (a *app) toggleFavoriteCommand(id string) {
	if a.commands == nil || a.commands.prefs == nil {
		return
	}
	if _, err := a.commands.prefs.toggleFavorite(id); err != nil {
		a.toast(toastMain, err.Error())
	}
	a.publish(func() {})
}

// commandPalette 把下拉面板挂在搜索框下方。面板开合由 palette.open 驱动：点输入
// 框展开，执行命令、失焦或点窗外关闭。
func (a *app) commandPalette(c *ui.Context, anchor ui.Element, q string) {
	st := &a.palette
	tk := lobetheme.TokensOf(c)
	rows := a.paletteRows(q)
	if st.selected < 0 {
		st.selected = 0
	}
	if st.selected >= len(rows) {
		st.selected = len(rows) - 1
	}

	lobeoverlay.Popover(c, anchor, &st.open, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		ui.Column(c).Width(paletteWidth).Gap(2).Children(func() {
			// 顶部两种模式：命令（默认）与搜索。搜索是次要入口，放在切换里。用
			// 普通按钮而不是 Segmented，点完把焦点还给输入框，好接着直接打字。
			ui.Row(c).FillWidth().Padding(2, 4).Gap(4).AlignItems(ui.Center).Children(func() {
				for _, m := range []int{paletteModeCommands, paletteModeSearch} {
					mode := m
					variant := action.Text
					if st.mode == mode {
						variant = action.Primary
					}
					btn := action.Button(c, paletteModeLabels[mode], action.ButtonProps{
						Size: lobetheme.Small, Variant: variant,
					})
					if btn.Clicked() {
						st.mode = mode
						st.selected = 0
						st.input.Focus(c) // 焦点还给输入框，好接着打字
					}
				}
				ui.Spacer(c).Grow(1)
				hint := "↑↓ 选择 · ↵ 执行 · Esc 清空"
				if st.mode == paletteModeSearch {
					hint = "↵ 打开搜索窗口"
				}
				ui.Text(c, hint).FontSize(10).TextColor(tk.TextTertiary)
			})
			if st.mode == paletteModeSearch {
				label := "输入关键词后按回车搜索"
				if strings.TrimSpace(q) != "" {
					label = "按回车搜索「" + strings.TrimSpace(q) + "」"
				}
				ui.Box(c).FillWidth().Padding(14).Center().Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(tk.TextSecondary)
				})
				return
			}
			if len(rows) == 0 {
				label := "没有匹配的命令"
				if strings.TrimSpace(q) != "" {
					label = "没有匹配的命令，按回车搜索「" + strings.TrimSpace(q) + "」"
				}
				ui.Box(c).FillWidth().Padding(12).Center().Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(tk.TextSecondary)
				})
				return
			}
			ui.Scroll(c).Width(paletteWidth).MaxHeight(paletteMaxHeight).Children(func() {
				lastHeading := ""
				for i := range rows {
					idx := i
					row := &rows[i]
					if row.heading != "" && row.heading != lastHeading {
						ui.Text(c, row.heading).FontSize(10).TextColor(tk.TextTertiary).
							Padding(8, 9, 2, 9)
					}
					lastHeading = row.heading
					cmd := &row.cmd
					button := ui.ButtonBase(c).FillWidth().Padding(7, 9).Radius(tk.BorderRadiusSM).
						Gap(9).AlignItems(ui.Center)
					switch {
					case idx == st.selected:
						button.Background(tk.PrimaryBg)
					case button.Hovered():
						button.Background(tk.FillSecondary)
					}
					button.Children(func() {
						ui.Icon(c, cmd.icon).Size(15, 15).TextColor(tk.Primary).Shrink(0)
						ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
							ui.Text(c, cmd.title).FontSize(12).SingleLine().TextColor(tk.Text)
							ui.Text(c, cmd.hint).FontSize(10).SingleLine().TextColor(tk.TextSecondary)
						})
						if cmd.shortcut != "" {
							ui.Text(c, cmd.shortcut).FontSize(10).SingleLine().
								TextColor(tk.TextTertiary).Shrink(0)
						}
						// 星标是行内的独立按钮：点它只改收藏，不执行命令、不关面板。
						icon := lucide.Star()
						if !cmd.favorite {
							icon = lucide.StarOff()
						}
						star := ui.ButtonBase(c).Width(18).Height(18).Center().Radius(tk.BorderRadiusSM).
							KeepFocus().Label("收藏 " + cmd.title).Tooltip("收藏或取消收藏")
						star.Children(func() {
							ui.Icon(c, icon).Size(12, 12).Shrink(0).TextColor(tk.Primary)
						})
						star.OnClick(func() { a.toggleFavoriteCommand(cmd.id) })
					})
					button.Label("命令 " + cmd.title).Tooltip(cmd.hint)
					button.OnClick(func() { a.runCommandByID(cmd.id) })
				}
			})
		})
	})
}
