package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 个人常量条目多时会把下拉顶出屏幕：列表进限高滚动区，「全部常量」留在底部
// 常驻，和评分分类下拉（scores_window.go）同一套办法。
const aboutConstantsPopoverMaxH float32 = 256

// 个人介绍编辑框的「快捷模板」工具条，照搬官网个人介绍页编辑框顶部的两个
// 下拉：「个人常量」插入 {{KEY}} 统计占位符，「展示组件」插入 daisyUI 组件
// token。官网前端的 ReadmeEditor（2libra.com 的 about 页 JS chunk）里，常量项
// 的 value 是 `{{KEY}}`、组件项生成下列 token，提交前自校验见 api-economy.md
// 的「介绍内容格式」。这里把 token 生成按官网原样落到 Go，下拉换成 MyGo 的
// Popover，点选后直接插到正文光标处，省得用户再对着文档手拼。

// aboutConstant 是「个人常量」下拉里的一项。
type aboutConstant struct {
	Label string // 显示名，来自 constantsLabel，缺失时由 key 推出来
	Token string // {{KEY}}
	Line  string // 「显示名：{{KEY}}」，与官网「复制常量」放进剪贴板的形状一致
}

// aboutHumanizeKey 复刻官网的 key 转显示名：下划线分词、每词首字母大写。
// 例如 tip_out_coins → Tip Out Coins。
func aboutHumanizeKey(key string) string {
	parts := strings.Split(strings.Trim(key, "_"), "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		parts[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(parts, " ")
}

// buildAboutConstants 按官网的规则从 about 响应生成常量列表：键取自
// achievement_count（这里解码成 Stats），显示名优先用 constantsLabel。排序
// 让下拉在响应字段顺序变化时保持稳定。
func buildAboutConstants(about userAbout) []aboutConstant {
	out := make([]aboutConstant, 0, len(about.Stats))
	for k := range about.Stats {
		key := strings.ToUpper(k)
		label := about.ConstantsLabel[strings.ToLower(k)]
		if label == "" {
			label = aboutHumanizeKey(k)
		}
		token := "{{" + key + "}}"
		out = append(out, aboutConstant{Label: label, Token: token, Line: label + "：" + token})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// aboutAllConstantsLine 是「全部常量」一次插入的内容：官网把每项写成
// 「显示名：{{KEY}}」，再用换行拼起来。
func aboutAllConstantsLine(consts []aboutConstant) string {
	lines := make([]string, 0, len(consts))
	for _, c := range consts {
		lines = append(lines, c.Line)
	}
	return strings.Join(lines, "\n")
}

// aboutValidImageURL 复刻官网的 URL 校验：非空、不是站内 /api 地址，且是
// http(s) 或以 / 开头的站内路径（官网渲染时补域名）。站内资源友好，但绝不
// 把接口地址当图片塞进介绍里。
func aboutValidImageURL(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" {
		return false
	}
	low := strings.ToLower(u)
	switch {
	case strings.HasPrefix(low, "https://2libra.com/api"),
		strings.HasPrefix(low, "http://2libra.com/api"),
		strings.HasPrefix(low, "2libra.com/api"),
		strings.HasPrefix(low, "/api"):
		return false
	case strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "http://"):
		return true
	case strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"):
		return true
	}
	return false
}

// 下面六个 token 生成器照抄官网各组件弹窗「确认」时的拼接：去掉空项、字段
// 用 | / , 分隔，省掉可以省略的尾部字段。顺序和 api-economy.md 一致。

func aboutCarouselToken(urls []string) string {
	valid := make([]string, 0, len(urls))
	for _, u := range urls {
		if u = strings.TrimSpace(u); aboutValidImageURL(u) {
			valid = append(valid, u)
		}
	}
	if len(valid) == 0 {
		return ""
	}
	return "[carousel:" + strings.Join(valid, ",") + "]"
}

func aboutCollapseToken(items [][2]string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		title, content := strings.TrimSpace(it[0]), strings.TrimSpace(it[1])
		if title == "" || content == "" {
			continue
		}
		parts = append(parts, title+"|"+content)
	}
	if len(parts) == 0 {
		return ""
	}
	return "[collapse:" + strings.Join(parts, ",") + "]"
}

func aboutTextRotateToken(items []string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if s := strings.TrimSpace(it); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[text-rotate:" + strings.Join(parts, ",") + "]"
}

func aboutRadialProgressToken(v int) string {
	return "[radial-progress:" + strconv.Itoa(aboutClampPercent(v)) + "]"
}

func aboutProgressToken(v, max int) string {
	if max < 1 {
		max = 100
	}
	if v < 0 {
		v = 0
	}
	if v > max {
		v = max
	}
	if max == 100 {
		return "[progress:" + strconv.Itoa(v) + "]"
	}
	return "[progress:" + strconv.Itoa(v) + ":" + strconv.Itoa(max) + "]"
}

func aboutHeroToken(imageURL, title, desc, btnText, btnLink string) string {
	imageURL, title = strings.TrimSpace(imageURL), strings.TrimSpace(title)
	if imageURL == "" || title == "" {
		return ""
	}
	desc, btnText, btnLink = strings.TrimSpace(desc), strings.TrimSpace(btnText), strings.TrimSpace(btnLink)
	token := "[hero:" + imageURL + "|" + title + "|" + desc + "|" + btnText
	if btnLink != "" {
		token += "|" + btnLink
	}
	return token + "]"
}

func aboutClampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// aboutComponentTemplate 是「展示组件」下拉的一项：名称与官网下拉的顺序、
// 文案一致，token 里的中文是占位说明，插入后直接在正文里改。
type aboutComponentTemplate struct {
	Name  string
	Token string
	Tip   string
}

func aboutComponentTemplates() []aboutComponentTemplate {
	return []aboutComponentTemplate{
		{"轮播图", aboutCarouselToken([]string{"https://图片地址1", "https://图片地址2"}), "多张图片轮播，逗号分隔，最多 10 张"},
		{"折叠面板", aboutCollapseToken([][2]string{{"标题", "内容"}}), "标题|内容，多组用逗号分隔"},
		{"轮播文字", aboutTextRotateToken([]string{"文字1", "文字2", "文字3"}), "多段文字轮播，逗号分隔"},
		{"环形进度", aboutRadialProgressToken(70), "0-100，也支持 {{KEY}}/总数 表达式"},
		{"进度条", aboutProgressToken(40, 100), "当前值:最大值，最大值默认 100"},
		{"Hero", aboutHeroToken("https://背景图地址", "标题", "描述", "按钮文字", "https://按钮链接"), "背景图|标题|描述|按钮文字|按钮链接"},
	}
}

// aboutTemplateState 是快捷模板的纯 UI 状态，跟着资料卡 /「我的」窗口的个人
// 介绍编辑状态走；不保存 ui.Element 或 *ui.Context。
type aboutTemplateState struct {
	ed        mdEditor // 选区与插入，复用发帖编辑器那套按 rune 的逻辑
	constOpen bool
	compOpen  bool
}

func (st *aboutTemplateState) reset() {
	st.ed = mdEditor{}
	st.constOpen = false
	st.compOpen = false
}

// insert 把一段模板插进正文光标处，并收起两个下拉。
func (st *aboutTemplateState) insert(body *string, text string) {
	if text == "" {
		return
	}
	st.ed.insertText(body, text)
	st.constOpen = false
	st.compOpen = false
}

// aboutTemplateToolbar 是正文上方的快捷模板工具条，对应官网编辑框的两个下拉。
// 必须画在正文输入框之前：这样下拉里点选插入时，输入框能在同一帧里读到待
// 恢复的选区（见 editor_core.go 的 syncSelection）。
func aboutTemplateToolbar(c *ui.Context, st *aboutTemplateState, about userAbout, body *string) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		constBtn := action.Button(c, "个人常量", action.ButtonProps{Size: lobetheme.Small})
		constBtn.Tooltip("插入 {{KEY}} 统计占位符，可一次插入全部")
		lobeoverlay.Popover(c, constBtn, &st.constOpen, lobeoverlay.PopoverProps{
			Placement: lobeoverlay.Bottom,
		}, func() {
			aboutConstantsPopover(c, st, about, body)
		})

		compBtn := action.Button(c, "展示组件", action.ButtonProps{Size: lobetheme.Small})
		compBtn.Tooltip("插入轮播图、折叠面板、进度条等展示组件")
		lobeoverlay.Popover(c, compBtn, &st.compOpen, lobeoverlay.PopoverProps{
			Placement: lobeoverlay.Bottom,
		}, func() {
			aboutComponentsPopover(c, st, body)
		})
	})
}

// aboutConstantsPopover 列出当前用户可用的统计常量；点一项插入它的说明行，
// 末尾的「全部常量」一次插入所有常量的说明表。
func aboutConstantsPopover(c *ui.Context, st *aboutTemplateState, about userAbout, body *string) {
	consts := buildAboutConstants(about)
	ui.Column(c).Width(260).Gap(2).Children(func() {
		if len(consts) == 0 {
			ui.Text(c, "暂无可用常量").FontSize(11).TextColor(lobetheme.TokensOf(c).TextSecondary)
			return
		}
		// 常量项放进限高滚动区；弹层本身不滚动，条目一多就会溢出屏幕看不全。
		ui.Scroll(c).MaxHeight(aboutConstantsPopoverMaxH).FillWidth().Children(func() {
			for _, ct := range consts {
				ct := ct
				aboutTemplateRow(c, ct.Label, ct.Token, "插入 "+ct.Line, func() {
					st.insert(body, ct.Line)
				})
			}
		})
		// 「全部常量」钉在滚动区下面，列表再长也点得到。
		layout.Divider(c, layout.DividerProps{})
		aboutTemplateRow(c, "全部常量", "", "一次插入全部常量的说明表", func() {
			st.insert(body, aboutAllConstantsLine(consts))
		})
	})
}

// aboutComponentsPopover 列出官网的六种展示组件；点一项往正文插入一段可直接
// 改写的 token 模板。
func aboutComponentsPopover(c *ui.Context, st *aboutTemplateState, body *string) {
	ui.Column(c).Width(230).Gap(2).Children(func() {
		for _, tpl := range aboutComponentTemplates() {
			tpl := tpl
			aboutTemplateRow(c, tpl.Name, "", tpl.Tip, func() {
				st.insert(body, tpl.Token)
			})
		}
	})
}

// aboutTemplateRow 是下拉里的一行：左边名称、右边可选的 token（常量的
// {{KEY}}），整行可点。可见文案就是无障碍名，点击测试按名字找得到。
func aboutTemplateRow(c *ui.Context, label, value, tip string, goAction func()) {
	tk := lobetheme.TokensOf(c)
	row := ui.Row(c).FillWidth().Padding(7, 8).Radius(tk.BorderRadiusSM).
		Gap(8).AlignItems(ui.Center).Background(ui.Transparent)
	if row.Hovered() {
		row.Background(tk.FillSecondary)
	}
	row.Children(func() {
		ui.Text(c, label).FontSize(12).SingleLine()
		if value != "" {
			ui.Spacer(c).Grow(1)
			ui.Text(c, value).FontSize(10).SingleLine().TextColor(tk.TextSecondary)
		}
	})
	row.Label(label).Tooltip(tip)
	if row.Clicked() {
		goAction()
	}
}
