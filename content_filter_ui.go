package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「我的 → 内容屏蔽」：三块名单共用一个 chip 编辑器，只有标题、上限与
// 占位文案不同。chip 的增删只改本地草稿，点「保存」才 PUT 整份替换；
// 「撤销改动」回到服务端快照。进入标签页时强制 GET 一次，避免拿旧基线
// 覆盖网页端的改动。

type contentFilterSection struct {
	title, placeholder string
}

var contentFilterSections = [3]contentFilterSection{
	{"屏蔽用户", "输入用户名"},
	{"屏蔽节点", "输入节点名"},
	{"屏蔽关键词", "输入关键词"},
}

func contentFilterSectionList(d *contentFilterDTO, i int) *[]string {
	switch i {
	case 0:
		return &d.BlockedUsers
	case 1:
		return &d.BlockedNodes
	default:
		return &d.Keywords
	}
}

// contentFilterEncodedBytes 用 url.QueryEscape 估算整份名单编码后的体积，
// 口径与文档一致：一个中文字 UTF-8 三字节，编码成 %XX%XX%XX 九字节。
func contentFilterEncodedBytes(d contentFilterDTO) int {
	lists := [3][]string{d.BlockedUsers, d.BlockedNodes, d.Keywords}
	n := 20 * 3
	for _, list := range lists {
		for _, s := range list {
			n += 4 + len(url.QueryEscape(s))
		}
	}
	return n
}

// profileFilterBody 是「内容屏蔽」标签页。
func (a *app) profileFilterBody(c *ui.Context, ps *profileState) {
	list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			// 改名失效与两端同源这两件事不写用户不会知道，顶部说明是必要的。
			display.Text(c, "与网页端「设置 → 内容屏蔽」是同一份名单；对方改名后需要重新添加。",
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})

			s := a.contentFilter
			if !a.auth.loggedIn() {
				display.Text(c, "登录后管理屏蔽名单", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				return
			}
			if s == nil {
				return
			}
			if s.loading && !s.loaded {
				ui.Column(c).FillWidth().Center().Padding(24).Children(func() {
					lobeSpin(c, feedback.SpinProps{Size: lobetheme.Large, Tip: "正在读取屏蔽名单…"})
				})
				return
			}
			if s.err != "" {
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					display.Text(c, "名单可能不是最新："+s.err, display.TextProps{
						Type: display.TextDanger, FontSize: 11, Rows: 2,
					})
					if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
						a.ensureContentFilter(true)
					}
				})
			}

			for i, sec := range contentFilterSections {
				a.contentFilterChips(c, i, sec, s)
			}

			offline := false
			if st := a.proxy.netStatus(); st != nil {
				offline = st.isOffline()
			}
			why := validateFilterDTO(s.draft, a.auth.username())
			if why == "" {
				why = s.saveErr
			}
			if offline {
				why = "离线时不能修改屏蔽名单"
			}
			n := contentFilterEncodedBytes(s.draft)
			if n > contentFilterMaxBytes*4/5 {
				display.Text(c, fmt.Sprintf("已用 %d / %d 字节", n, contentFilterMaxBytes),
					display.TextProps{Type: display.TextSecondary, FontSize: 10, Rows: 1})
			}
			if why != "" {
				display.Text(c, why, display.TextProps{
					Type: display.TextDanger, FontSize: 11, Rows: 2,
				})
			}
			for i, msg := range s.fieldErrs {
				if msg != "" {
					display.Text(c, contentFilterSections[i].title+"："+msg, display.TextProps{
						Type: display.TextDanger, FontSize: 11, Rows: 2,
					})
				}
			}

			ui.Row(c).FillWidth().Padding(4, 0, 0, 0).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				undo := action.Button(c, "撤销改动", action.ButtonProps{Size: lobetheme.Small})
				undo.Disabled(!s.dirty || s.saving)
				if undo.Clicked() {
					a.resetFilterDraft()
				}
				label := "保存"
				if s.saving {
					label = "处理中…"
				}
				save := action.Button(c, label, action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Primary,
				})
				save.Disabled(!s.dirty || s.saving || s.err != "" || offline || why != "")
				if save.Clicked() {
					a.writeContentFilter(cloneFilterDTO(s.draft), toastProfile, nil)
				}
			})
		})
	})
	list.Grow(1).MinHeight(0)
}

// contentFilterChips 画一块名单：标题 + n/50、已有条目（点 × 移除）、输入框
// 与「添加」。三个区块共用这一份，只有标题和占位文案不同。
func (a *app) contentFilterChips(c *ui.Context, i int, sec contentFilterSection, s *contentFilterState) {
	tk := lobetheme.TokensOf(c)
	list := contentFilterSectionList(&s.draft, i)
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			display.Text(c, fmt.Sprintf("%s（%d/%d）", sec.title, len(*list), contentFilterMaxPerList),
				display.TextProps{Strong: true, FontSize: 12, Rows: 1})
			if s.dirty {
				ui.Text(c, "未保存").FontSize(10).TextColor(tk.Warning)
			}
		})
		if len(*list) > 0 {
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				for _, item := range *list {
					value := item
					chip := ui.Row(c).Gap(4).AlignItems(ui.Center).
						Padding(3, 7).Radius(tk.BorderRadius).Background(tk.FillTertiary)
					chip.Children(func() {
						ui.Text(c, value).FontSize(11).TextColor(tk.Text)
						remove := ui.Button(c, "×").Background(ui.Transparent).Border(0, ui.Transparent).
							FontSize(12).TextColor(tk.TextSecondary).Padding(0, 2).Radius(4)
						remove.Label("移除 " + value).Tooltip("移除 " + value)
						if remove.Clicked() {
							a.removeFilterEntry(i, value)
						}
					})
				}
			})
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			in := field.Input(c, &s.entry[i], field.InputProps{
				Placeholder: sec.placeholder, Size: lobetheme.Small,
			})
			in.Label(sec.placeholder)
			in.OnSubmit(func() { a.addFilterEntry(i) })
			if action.Button(c, "添加", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.addFilterEntry(i)
			}
		})
	})
}

func (a *app) addFilterEntry(i int) {
	s := a.contentFilter
	if s == nil {
		return
	}
	s.entry[i] = strings.TrimSpace(s.entry[i])
	if s.entry[i] == "" {
		return
	}
	value := s.entry[i]
	list := contentFilterSectionList(&s.draft, i)
	for _, item := range *list {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			s.entry[i] = ""
			return
		}
	}
	if len(*list) >= contentFilterMaxPerList {
		s.fieldErrs[i] = fmt.Sprintf("最多 %d 条", contentFilterMaxPerList)
		return
	}
	s.fieldErrs[i] = ""
	*list = append(*list, value)
	s.entry[i] = ""
	s.dirty = true
}

func (a *app) removeFilterEntry(i int, value string) {
	s := a.contentFilter
	if s == nil {
		return
	}
	list := contentFilterSectionList(&s.draft, i)
	kept := (*list)[:0]
	for _, item := range *list {
		if !strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			kept = append(kept, item)
		}
	}
	*list = kept
	s.fieldErrs[i] = ""
	s.dirty = true
}

// resetFilterDraft 回到服务端快照：撤销改动与名单重新加载都走这里。
func (a *app) resetFilterDraft() {
	s := a.contentFilter
	if s == nil {
		return
	}
	base := contentFilterDTO{}
	if s.filter != nil {
		base = cloneFilterDTO(s.filter.raw)
	}
	s.draft = cloneFilterDTO(base)
	s.entry = [3]string{}
	s.dirty = false
	s.saveErr = ""
	s.fieldErrs = [3]string{}
}

// profileMenuFilterRow 是账号菜单里的「内容屏蔽」直达入口：右侧只在有名单
// 时显示总数，没有就不占位。
func (a *app) profileMenuFilterRow(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	a.profileMenuRow(c, lucide.EyeOff(), "内容屏蔽", "内容屏蔽", "管理屏蔽的用户、节点与关键词", func() {
		if n := a.filterCount(); n > 0 {
			ui.Text(c, fmt.Sprintf("%d 项", n)).FontSize(11).TextColor(tk.TextSecondary)
		}
	}, func() { a.showProfileTab(profileTabFilter) })
}

// userCardBlockButton 画资料头右侧的「屏蔽 / 已屏蔽」按钮，紧跟关注按钮。
// 状态直接读共享名单快照，不另存一份；提交中禁用。自己的资料卡不画。
func (a *app) userCardBlockButton(c *ui.Context, cs *userCardState) {
	if cs.username == "" || a.userCardSelf() {
		return
	}
	blocked := a.filterBlocksUser(cs.username)
	busy := false
	if s := a.contentFilter; s != nil {
		busy = s.saving && (s.savingUser == cs.username || s.savingUser == "")
	}
	label := "屏蔽"
	if blocked {
		label = "已屏蔽"
	}
	if busy {
		label = "处理中…"
	}
	b := action.Button(c, label, action.ButtonProps{Size: lobetheme.Small})
	b.Shrink(0).Disabled(busy)
	if blocked {
		b.Tooltip("点击取消屏蔽")
	} else {
		b.Tooltip("不再显示这个人的帖子和评论")
	}
	if b.Clicked() {
		a.setUserBlocked(cs.username, !blocked)
	}
}
