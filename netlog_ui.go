package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

/* ------------------------------------------------------------------
   网络调试窗口：全局请求记录的查看界面
---------------------------------------------------------------- */

// 网络调试窗口类似 Chrome DevTools 的 Network 面板：上半是请求列表（最新
// 在最上面），下半是选中请求的详情（参数、请求头、请求体、响应头、响应体）。
// 数据来自进程级的 netRecorder（见 netlog.go）：不管从哪个账号窗口发出的
// 请求都记在同一份缓冲里，窗口只是它的查看器。
const (
	netLogWindowWidth  = 960
	netLogWindowHeight = 660
	netLogWindowMinW   = 760
	netLogWindowMinH   = 480
)

// netLogRedrawInterval 限制新请求到达时刷新窗口的频率：批量加载图片时请求
// 来得很密，不节流会把主线程刷爆。250ms 对人眼足够「实时」。
const netLogRedrawInterval = 250 * time.Millisecond

var netLogKindLabels = []string{"全部", "2Libra", "图片", "其他"}

/* ------------------------------------------------------------------
   全局入口、显示与关闭
---------------------------------------------------------------- */

// showNetLog 是网络调试窗口的全局入口：任何窗口、任何协程都能调用。它把
// 工作挪到主线程，窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showNetLog() {
	a.publish(a.showNetLogWindow)
}

// showNetLogWindow 在主线程上真正打开网络调试窗口。打开时向进程级记录器
// 注册一条回调：新请求到达或某条记录完成时刷新窗口（节流见 netLogChanged）。
func (a *app) showNetLogWindow() {
	// 无头测试没有主循环：不碰 mygo 的窗口，测试直接渲染 netLogWindowView。
	if a.win == nil {
		return
	}
	if win := a.netLogWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	a.netLogUnwatch = a.proxy.net.addWatcher(a.netLogChanged)
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("网络调试"),
		Width:             netLogWindowWidth,
		Height:            netLogWindowHeight,
		MinWidth:          netLogWindowMinW,
		MinHeight:         netLogWindowMinH,
		StateKey:          "netlog",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 与其他窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.netLogWindowView),
	})
	a.netLogWin = win
	win.OnClosed(func() {
		a.netLogWin = nil
		a.unwatchNetLog()
		a.accountWindowClosed(win)
	})
}

// unwatchNetLog 注销记录器回调。窗口正常关闭与主窗口收尾两条路都会走到，
// 反注册是幂等的，重复调用安全。
func (a *app) unwatchNetLog() {
	if a.netLogUnwatch != nil {
		a.netLogUnwatch()
		a.netLogUnwatch = nil
	}
}

// closeNetLogWindow 关掉网络调试窗口。只在主线程调用。
func (a *app) closeNetLogWindow() {
	a.unwatchNetLog()
	win := a.netLogWin
	if win == nil {
		return
	}
	a.netLogWin = nil
	win.Close()
}

// netLogChanged 是记录器的通知回调：在网络协程里触发，节流后挪回主线程
// 刷新调试窗口。窗口没开着就不刷。
func (a *app) netLogChanged() {
	a.netLogRedrawMu.Lock()
	now := time.Now()
	if now.Sub(a.netLogLastRedraw) < netLogRedrawInterval {
		a.netLogRedrawMu.Unlock()
		return
	}
	a.netLogLastRedraw = now
	a.netLogRedrawMu.Unlock()
	a.publish(func() {
		if win := a.netLogWin; win != nil {
			win.Invalidate()
		}
	})
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// netLogWindowView 是网络调试窗口的 view：标题栏、工具栏（暂停 / 清空 /
// 分类过滤 / 文本筛选 / 条数）、请求列表与详情两段。
func (a *app) netLogWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	c.Root().Background(t.Background)
	// Esc 直接关掉窗口。复制结果这类提示归本窗口自己领走（见 toasts.go）。
	c.OnShortcut(0, ui.KeyEscape, a.closeNetLogWindow)
	defer a.drainToasts(c, a.claimsToasts(toastNetLog))

	entries := a.proxy.net.Snapshot()
	shown := a.netLogFiltered(entries)
	// 选中项可能已被清空或挤掉：这一帧直接取消选中，别让详情区悬空。
	var sel *netEntry
	if a.netLogSel != 0 {
		sel = a.proxy.net.ByID(a.netLogSel)
		if sel == nil {
			a.netLogSel = 0
		}
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角是最小化与关闭。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				display.Text(c, "网络调试", display.TextProps{FontSize: 13, Strong: true, Rows: 1}).
					TextColor(t.Text)
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.netLogWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", func() { a.closeNetLogWindow() })
		})
		layout.Divider(c, layout.DividerProps{})

		// 工具栏：暂停 / 恢复、清空、分类过滤、文本筛选与条数。
		ui.Row(c).FillWidth().Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.proxy.net.Paused() {
				b := action.Button(c, "继续记录", action.ButtonProps{Size: lobetheme.Small}).
					Tooltip("恢复记录新的请求，已有记录保留")
				if b.Clicked() {
					a.proxy.net.SetPaused(false)
				}
			} else {
				b := action.Button(c, "暂停记录", action.ButtonProps{Size: lobetheme.Small}).
					Tooltip("暂停记录，发出去的请求不再进入列表")
				if b.Clicked() {
					a.proxy.net.SetPaused(true)
				}
			}
			if iconActionButton(c, trashIcon, "清空记录", "清空当前列表里的全部请求记录").Clicked() {
				a.proxy.net.Clear()
				a.netLogSel = 0
			}
			action.Segmented(c, &a.netLogKind, netLogKindLabels, action.SegmentedProps{
				Size: lobetheme.Small,
			}).Label("分类筛选")
			ui.Box(c).Grow(1).MinWidth(0).Children(func() {
				field.Input(c, &a.netLogFilter, field.InputProps{
					Size:        lobetheme.Small,
					Placeholder: "按方法或 URL 筛选，如 /v1/topics",
				}).Label("筛选").Grow(1).MinWidth(0)
			})
			display.Text(c, fmt.Sprintf("%d 条", len(shown)), display.TextProps{FontSize: 11, Rows: 1}).
				TextColor(t.TextMuted)
		})
		layout.Divider(c, layout.DividerProps{})

		// 请求列表：最新在最上面，免去追着滚动条跑。
		layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			ui.Column(c).FillWidth().Gap(0).Padding(4, 6).Children(func() {
				if len(shown) == 0 {
					hint := "还没有请求。应用发出的每个请求都会出现在这里。"
					if a.proxy.net.Paused() {
						hint = "记录已暂停。点「继续记录」恢复。"
					}
					display.Text(c, hint, display.TextProps{FontSize: 12, Rows: 2}).
						TextColor(t.TextMuted).Padding(12, 8)
					return
				}
				for i := len(shown) - 1; i >= 0; i-- {
					a.netLogRow(c, shown[i])
				}
			})
		}).Grow(45).MinHeight(0)
		layout.Divider(c, layout.DividerProps{})

		// 详情：选中请求的参数、头与体。
		layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			a.netLogDetail(c, sel)
		}).Grow(55).MinHeight(0)
	})
}

// netLogFiltered 按分类芯片与文本筛选框过滤记录。文本匹配方法与 URL，
// 不区分大小写。
func (a *app) netLogFiltered(entries []*netEntry) []*netEntry {
	var out []*netEntry
	needle := strings.ToLower(strings.TrimSpace(a.netLogFilter))
	for _, e := range entries {
		// 芯片下标 0 是「全部」；其余与条目的 uiKind 一一对应。
		if a.netLogKind != 0 && e.uiKind() != a.netLogKind {
			continue
		}
		if needle != "" {
			hay := strings.ToLower(e.Method + " " + e.URL)
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// netLogRow 画列表里的一行：方法、状态、耗时、URL。点击选中，详情区跟着换。
func (a *app) netLogRow(c *ui.Context, e *netEntry) {
	t := c.Theme()
	selected := a.netLogSel == e.ID
	row := ui.Row(c).FillWidth().Padding(4, 8).Gap(8).AlignItems(ui.Center).
		Radius(4).Background(ui.Transparent)
	if selected || row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	methodColor := t.TextMuted
	switch e.Method {
	case "POST", "PUT", "PATCH", "DELETE":
		methodColor = t.Accent
	}
	row.Children(func() {
		display.Text(c, e.Method, display.TextProps{FontSize: 11, Strong: true, Rows: 1}).
			Width(60).TextColor(methodColor)
		display.Text(c, netStatusText(e), display.TextProps{FontSize: 11, Rows: 1}).
			Width(72).TextColor(netStatusColor(t, e))
		display.Text(c, netElapsedText(e), display.TextProps{FontSize: 11, Rows: 1}).
			Width(76).TextAlign(ui.End).TextColor(t.TextMuted)
		display.Text(c, e.URL, display.TextProps{FontSize: 11, Rows: 1}).
			Grow(1).MinWidth(0).TextColor(t.Text)
	})
	row.Label(e.Method + " " + e.URL).
		Tooltip("点击查看这次请求的参数与响应").
		OnClick(func() { a.netLogSel = e.ID })
}

// netStatusText 把状态写成短文本：连接失败、进行中或状态码。
func netStatusText(e *netEntry) string {
	switch {
	case e.Err != "":
		return "失败"
	case e.Status == 0 && !e.Done:
		return "…"
	case e.Status == 0:
		return "—"
	default:
		return strconv.Itoa(e.Status)
	}
}

// netStatusColor 给状态文本挑颜色：2xx 正常、3xx 提示、4xx/5xx 与失败用
// 危险色、进行中用弱化色。
func netStatusColor(t *ui.Theme, e *netEntry) ui.Color {
	switch {
	case e.Err != "" || e.Status >= 400:
		return t.Danger
	case e.Status >= 300:
		return t.Accent
	case e.Status == 0:
		return t.TextMuted
	default:
		return t.Text
	}
}

// netElapsedText 把耗时写成短文本。没读完的显示进行中，不足 1 毫秒的
// 归成 <1ms，避免全是 0ms。
func netElapsedText(e *netEntry) string {
	if !e.Done {
		return "…"
	}
	d := e.Elapsed.Round(time.Millisecond)
	if d <= 0 {
		return "<1ms"
	}
	return d.String()
}

/* ------------------------------------------------------------------
   详情区
---------------------------------------------------------------- */

// netLogDetail 画选中请求的详情：概览、请求（方法 / URL / 查询参数 / 头 /
// 体）与响应（状态 / 头 / 体）。没选中时给一句提示。
func (a *app) netLogDetail(c *ui.Context, e *netEntry) {
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(12, 14).Gap(12).Children(func() {
		if e == nil {
			display.Text(c, "在上方选择一条请求，查看它的参数与响应",
				display.TextProps{FontSize: 12, Rows: 2}).TextColor(t.TextMuted)
			return
		}

		a.netCopyActions(c, e)

		a.netSection(c, "概览", func() {
			a.netKV(c, "状态", netDetailStatus(e))
			a.netKV(c, "耗时", netElapsedText(e))
			a.netKV(c, "开始时间", e.Start.Format("15:04:05.000"))
			a.netKV(c, "分类", netLogKindLabels[e.uiKind()])
		})

		a.netSection(c, "请求", func() {
			a.netKV(c, "方法", e.Method)
			a.netKV(c, "URL", e.URL)
			a.netQuery(c, e.URL)
			if len(e.ReqHeaders) > 0 {
				a.netSectionTitle(c, "请求头", 0)
				for _, h := range e.ReqHeaders {
					a.netKV(c, h.Key, h.Value)
				}
			}
			if e.ReqBodyNote != "" {
				display.Text(c, e.ReqBodyNote, display.TextProps{FontSize: 11, Rows: 2}).
					TextColor(t.TextMuted)
			} else if e.ReqBody != "" {
				a.netSectionTitle(c, "请求体", 0)
				a.netCodeBlock(c, netPrettyJSON(e.ReqBody))
				if e.ReqBodyTruncated {
					display.Text(c, "请求体已截断，仅显示前 "+netHumanSize(netLogMaxBody),
						display.TextProps{FontSize: 11, Rows: 1}).TextColor(t.TextMuted)
				}
			}
		})

		a.netSection(c, "响应", func() {
			a.netKV(c, "状态", netDetailStatus(e))
			if e.Err != "" {
				display.Text(c, "连接失败："+e.Err, display.TextProps{FontSize: 11, Rows: 3}).
					TextColor(t.Danger)
			}
			if len(e.RespHeaders) > 0 {
				a.netSectionTitle(c, "响应头", 0)
				for _, h := range e.RespHeaders {
					a.netKV(c, h.Key, h.Value)
				}
			}
			if !e.Done {
				display.Text(c, "响应体尚未读完，内容还在增加…",
					display.TextProps{FontSize: 11, Rows: 1}).TextColor(t.TextMuted)
			}
			if e.RespNote != "" {
				display.Text(c, e.RespNote, display.TextProps{FontSize: 11, Rows: 3}).
					TextColor(t.TextMuted)
			}
			if e.RespBody != "" {
				a.netSectionTitle(c, "响应体", 0)
				a.netCodeBlock(c, netPrettyJSON(e.RespBody))
				if e.RespTrunc {
					display.Text(c, "响应体已截断，仅显示前 "+netHumanSize(netLogMaxBody),
						display.TextProps{FontSize: 11, Rows: 1}).TextColor(t.TextMuted)
				}
			}
		})
	})
}

/* ------------------------------------------------------------------
   复制：详情数据一键拷进剪贴板，整条请求可还原成 curl
---------------------------------------------------------------- */

// netCopyActions 是详情区顶部的一排复制按钮：请求头 / 请求体 / 响应头 /
// 响应体各自一键拷走，最后还有一枚「复制为 curl」把整条请求还原成可直接
// 贴进终端重放的命令。几个动作并排时同一枚图标分不清彼此，按约定保留
// 文字；只列出当前有数据可拷的项，不给点了必然复制空串的按钮。
func (a *app) netCopyActions(c *ui.Context, e *netEntry) {
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		if len(e.ReqHeaders) > 0 {
			a.netCopyChip(c, "复制请求头", "把全部请求头拷进剪贴板",
				netHeadersText(e.ReqHeaders), "已复制请求头")
		}
		if e.ReqBody != "" {
			a.netCopyChip(c, "复制请求体", "把请求体拷进剪贴板",
				e.ReqBody, "已复制请求体")
		}
		if len(e.RespHeaders) > 0 {
			a.netCopyChip(c, "复制响应头", "把全部响应头拷进剪贴板",
				netHeadersText(e.RespHeaders), "已复制响应头")
		}
		if e.RespBody != "" {
			a.netCopyChip(c, "复制响应体", "把响应体拷进剪贴板",
				e.RespBody, "已复制响应体")
		}
		a.netCopyChip(c, "复制为 curl", "把这条请求还原成 curl 命令，贴进终端即可重放",
			netCurlCommand(e), "已复制 curl 命令")
	})
}

// netCopyChip 是详情区的一枚文字复制按钮：点击把 text 写进剪贴板，结果由
// 本窗口的 toast 报告。
func (a *app) netCopyChip(c *ui.Context, label, tip, text, okText string) {
	if action.Button(c, label, action.ButtonProps{Size: lobetheme.Small}).
		Tooltip(tip).Clicked() {
		a.copyNetValue(c, text, okText)
	}
}

// copyNetValue 把一段文本写进剪贴板，成功或失败都用网络调试窗口的 toast
// 报告；窗口不在了由主窗口接手（见 mainClaimsToasts）。拷贝结果在下一帧
// 之前才回来（同 copyToClipboard），提示先排队，等窗口下次重建时领走。
func (a *app) copyNetValue(c *ui.Context, text, okText string) {
	if text == "" || c == nil {
		return
	}
	c.WriteClipboardThen(text, func(err error) {
		if err != nil {
			a.toast(toastNetLog, "复制失败："+err.Error())
			return
		}
		a.toast(toastNetLog, okText)
	})
}

// netHeadersText 把一组头写成「Key: Value」行，方便贴进文档或脚本。
func netHeadersText(headers []netHeader) string {
	lines := make([]string, 0, len(headers))
	for _, h := range headers {
		lines = append(lines, h.Key+": "+h.Value)
	}
	return strings.Join(lines, "\n")
}

// netShellQuote 把一段文本包成 bash 的单引号字面量：内容里的单引号按
// '\”（关引号、转义引号、再开引号）的惯用法转义，整段贴进终端不会断开。
func netShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// netCurlCommand 把这条请求还原成 curl 命令：贴进终端就能重放。头与体用的
// 是调试窗口记录到的那份——Authorization、Cookie 等敏感头在记录时已脱敏，
// 请求体超长时只有前 netLogMaxBody 字节，两类缺口都在命令尾部的注释里
// 说明，免得对着一条注定 401 的命令排查半天。
func netCurlCommand(e *netEntry) string {
	args := []string{netShellQuote(e.URL)}
	if e.Method != "GET" {
		args = append(args, "-X "+e.Method)
	}
	redacted := false
	for _, h := range e.ReqHeaders {
		if strings.Contains(h.Value, "（已隐藏") {
			redacted = true
		}
		args = append(args, "-H "+netShellQuote(h.Key+": "+h.Value))
	}
	if e.ReqBody != "" {
		args = append(args, "--data-raw "+netShellQuote(e.ReqBody))
	}

	var b strings.Builder
	b.WriteString("curl")
	for i, arg := range args {
		if i == 0 {
			b.WriteString(" " + arg)
			continue
		}
		b.WriteString(" \\\n  " + arg)
	}
	// 注释只能放在末行之后：续行符后面跟注释会被 bash 续进命令里。
	if e.ReqBodyTruncated {
		b.WriteString("\n# 请求体已截断，仅前 " + netHumanSize(netLogMaxBody))
	}
	if redacted {
		b.WriteString("\n# Authorization/Cookie 等敏感头的值已脱敏，执行前请自行补全")
	}
	return b.String()
}

// netDetailStatus 把概览里的「状态」写完整：状态码加说明，失败给原因。
func netDetailStatus(e *netEntry) string {
	switch {
	case e.Err != "":
		return "失败"
	case e.Status == 0 && !e.Done:
		return "进行中"
	case e.Status == 0:
		return "未接入记录层（如更新插件内部请求）"
	default:
		s := "HTTP " + strconv.Itoa(e.Status)
		if e.Stream {
			s += " · 流式响应"
		}
		return s
	}
}

// netSection 画详情里的一大块：一个小标题 + 缩进的内容。
func (a *app) netSection(c *ui.Context, title string, body func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		display.Text(c, title, display.TextProps{FontSize: 12, Strong: true, Rows: 1}).
			TextColor(t.Text)
		body()
	})
}

// netSectionTitle 是块内的小节标题（请求头、请求体这类），字号小一档。
func (a *app) netSectionTitle(c *ui.Context, title string, _ int) {
	t := c.Theme()
	display.Text(c, title, display.TextProps{FontSize: 11, Strong: true, Rows: 1}).
		TextColor(t.TextMuted)
}

// netKV 画一行键值：键是固定宽度的弱化文本，值换行展示（URL 与头的值可能
// 很长）。行尾有一枚复制图标：单独拷走这个值——请求头、查询参数、URL 全都
// 能各自复制，不用手动框选。
func (a *app) netKV(c *ui.Context, key, value string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, key, display.TextProps{FontSize: 11, Rows: 1}).
			TextColor(t.TextMuted).Width(140)
		display.Text(c, value, display.TextProps{FontSize: 11, Rows: 1}).
			TextColor(t.Text).Grow(1).MinWidth(0)
		if iconActionButton(c, copyLinkIcon, "复制 "+key, "把「"+key+"」的值复制到剪贴板").Clicked() {
			a.copyNetValue(c, value, "已复制「"+key+"」")
		}
	})
}

// netQuery 把 URL 的查询参数逐个画出来：调试接口时最常看的就是「传了什么
// 参数」。没有参数就不画。
func (a *app) netQuery(c *ui.Context, raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return
	}
	a.netSectionTitle(c, "查询参数", 0)
	for key, vv := range u.Query() {
		for _, v := range vv {
			a.netKV(c, key, v)
		}
	}
}

// netCodeBlock 画一段等宽感的正文块：JSON 已在传入前格式化过，用 Lobe 的
// Block 给出浅底圆角面板，正文用 display.Text 的等宽样式。
func (a *app) netCodeBlock(c *ui.Context, text string) {
	t := c.Theme()
	block := layout.Block(c, layout.BlockProps{Variant: layout.BlockFilled}).
		FillWidth().Padding(8, 10).Radius(6).TextColor(t.Text)
	block.Children(func() {
		display.Text(c, text, display.TextProps{FontSize: 11, Rows: 0, Code: true})
	})
}

// netPrettyJSON 尽量把响应体格式化成缩进的 JSON；不是合法 JSON 就原样
// 返回（SSE 的事件流、HTML 等不折腾）。
func netPrettyJSON(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}
