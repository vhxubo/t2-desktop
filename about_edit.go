package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
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
				if a.handleAuthErrorFor(toastUserCard, err, func() { a.submitUserCardAbout() }) {
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
			a.toast(toastUserCard, "个人介绍已保存；审核通过后会扣除 500 金币")
			// 扣费是异步的，不在请求返回时刷新余额；标题栏定时刷新与回到
			// 前台时的校准会补上实际扣款。
		})
	}()
}

// userCardAboutEditor 是编辑个人介绍的模态框：正文、公开开关、字数与确认。
// 保存按钮上写明 500 金币，用户点它才发请求。用 Lobe 的 Modal 统一标题、
// ✕ 关闭、取消/确认与滚动区；保存期间的进度留在内容里。
func (a *app) userCardAboutEditor(c *ui.Context) {
	cs := a.userCard
	if cs == nil || !cs.aboutEditOpen {
		return
	}
	t := c.Theme()
	open := true
	count := utf8.RuneCountInString(cs.aboutDraft)
	countColor := t.TextMuted
	if count > userAboutMaxChars {
		countColor = t.Danger
	}
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "编辑个人介绍",
		Width:      420,
		OkText:     "保存并支付 500 金币",
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, "保存会消耗 500 金币，审核通过后扣除；违规内容会被清空并暂停编辑。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			ui.TextArea(c, &cs.aboutDraft).Label("个人介绍").
				Lines(8, 14).FillWidth()
			ui.Toggle(c, &cs.aboutPublic, "公开（广场可见）").
				Label("公开个人介绍")
			ui.Text(c, "公开时任何人可见；关闭后仅社区成员可见。").
				FontSize(10).TextColor(t.TextMuted).MaxLines(2)
			ui.Textf(c, "%d / %d 字符", count, userAboutMaxChars).
				FontSize(10).TextColor(countColor)
			if cs.aboutErrEdit != "" {
				ui.Text(c, cs.aboutErrEdit).FontSize(11).TextColor(t.Danger).MaxLines(2)
			}
			if cs.aboutBusy {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{CustomSize: 13})
					ui.Text(c, "正在保存…").FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
	// 取消、✕、Esc 与点窗外都由 Modal 关窗：清掉草稿状态。
	if !open {
		cs.aboutEditOpen = false
		cs.aboutErrEdit = ""
		return
	}
	// busy 时 Modal 的确认按钮仍是亮的，submit 自己会拦住重复提交；
	// 成功后由异步回调清掉 aboutEditOpen，弹窗自然消失。
	if ok {
		a.submitUserCardAbout()
	}
}
