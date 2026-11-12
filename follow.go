package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 关注作者
//
//	GET    /v1/users/:userName        读取 is_following 与作者 uuid
//	POST   /v1/users/:userId/follow   关注（scope interact）
//	DELETE /v1/users/:userId/follow   取关（scope interact）
//
// 关注端点要的是用户 uuid（不是 username），所以先读用户主页拿到 id 与
// 当前登录者对该用户的关注状态；主题详情里的 author 只有 id，没有
// is_following。读不到状态时按钮按未关注展示，点击后以服务端结果为准。

const followTimeout = 20 * time.Second

// followState 是一篇帖子作者关注状态的 UI 侧缓存。所有字段只在 UI 线程
// 读写；网络结果通过 app.publish 回来。
type followState struct {
	userID    string // 作者 uuid，关注 / 取关路径用
	following bool
	known     bool // 是否已从服务端确认关注状态
	checked   bool // 是否已经查过一次（失败也置位，避免每帧重试）
	need      bool // 关注状态还没确定，需要（或等待登录后）读取
	loading   bool
	busy      bool // 正在提交关注 / 取关
	err       string
	viewer    string // 状态属于哪个登录账号；换账号后重新读取
	gen       uint64
}

// userProfile 只保留关注功能需要的字段。未知字段按 v1 兼容承诺忽略。
type userProfile struct {
	ID        string
	Username  string
	Following bool
	Known     bool
}

// userPath 是用户主页路径。username 是公开标识，必须转义，避免昵称里的
// 特殊字符改变请求路径。
func userPath(username string) string {
	return "/v1/users/" + url.PathEscape(username)
}

// userFollowPath 是关注 / 取关路径，参数是用户 uuid。
func userFollowPath(userID string) string {
	return "/v1/users/" + url.PathEscape(userID) + "/follow"
}

func (c *Client) fetchUserProfile(ctx context.Context, username string) (userProfile, error) {
	return withAuthRetry(c, ctx, func(token string) (userProfile, error) {
		d, err := c.getJSON(ctx, c.apiURL(userPath(username)), token)
		if err != nil {
			return userProfile{}, err
		}
		return decodeUserProfile(d), nil
	})
}

func decodeUserProfile(d json.RawMessage) userProfile {
	o := asObject(d)
	if o == nil {
		return userProfile{}
	}
	p := userProfile{ID: o.str("id"), Username: o.str("username")}
	// is_following 只在带令牌时有意义；匿名响应里为 false。字段缺失时保持
	// Known=false，调用方不要把它当成「未关注」。
	if _, ok := o["is_following"]; ok {
		p.Known = true
		p.Following = o.flag("is_following", "followed")
	}
	return p
}

// setFollow 关注或取关一个用户，走和读请求相同的刷新 + 重试链路。重复关注
// / 重复取关返回的 409 conflict 视为成功：目标状态已经成立。
func (c *Client) setFollow(ctx context.Context, userID string, follow bool) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		var err error
		if follow {
			_, err = c.postJSON(ctx, c.apiURL(userFollowPath(userID)), token, "", nil)
		} else {
			_, err = c.deleteJSON(ctx, c.apiURL(userFollowPath(userID)), token)
		}
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

// followAuthorIsSelf 判断作者是不是当前登录账号：自己的帖子不展示关注按钮。
func (a *app) followAuthorIsSelf(author string) bool {
	name := strings.TrimSpace(a.auth.username())
	return name != "" && strings.EqualFold(name, strings.TrimSpace(author))
}

func (a *app) canFollowAuthor(p *topicDetail) bool {
	if p == nil || strings.TrimSpace(p.Author) == "" || p.Author == "匿名" {
		return false
	}
	return !a.followAuthorIsSelf(p.Author)
}

// ensureFollowState 是受守卫的后台任务：登录后读一次作者主页，确认是否已
// 关注。只有 need 置位（正文加载完、登录账号变化）时才发请求，避免每次
// 重建都多拉一次用户主页。自动加载关闭（headless 测试）时什么都不做。
func (a *app) ensureFollowState(d *detailState) {
	if d == nil || d.topic == nil || !a.autoLoad || !d.follow.need {
		return
	}
	p := d.topic
	if !a.canFollowAuthor(p) || !a.auth.loggedIn() {
		return // 未登录时保留 need，登录后由 followButton 再次触发
	}
	if d.follow.userID == "" {
		d.follow.userID = p.AuthorID
	}
	viewer := a.auth.username()
	if d.follow.known && d.follow.viewer == viewer {
		d.follow.need = false
		return
	}
	if d.follow.loading || (d.follow.checked && d.follow.viewer == viewer) {
		d.follow.need = false
		return
	}
	d.follow.loading = true
	d.follow.viewer = viewer
	d.follow.gen++
	gen := d.follow.gen
	username := p.Author
	ctx, cancel := context.WithTimeout(context.Background(), followTimeout)
	go func() {
		defer cancel()
		prof, err := a.client.fetchUserProfile(ctx, username)
		a.publish(func() {
			if a.detail != d {
				return
			}
			// 关注 / 取关可能已经改过状态并让本次读取作废：仍要收起
			// loading，否则按钮会一直停在加载态。
			d.follow.loading = false
			if d.follow.gen != gen {
				return
			}
			d.follow.need = false
			d.follow.checked = true
			d.follow.viewer = viewer
			if err != nil {
				// 状态读不到不影响读帖；按钮仍可点，以关注接口的结果为准。
				return
			}
			if prof.ID != "" {
				d.follow.userID = prof.ID
			}
			if prof.Known {
				d.follow.following = prof.Following
				d.follow.known = true
			}
		})
	}()
}

// toggleFollow 关注或取消关注当前帖子的作者。未登录时先走全局授权弹窗，
// 授权成功后同一次操作自动重跑；缺 scope 的 403 也一样。
func (a *app) toggleFollow(d *detailState, follow bool) {
	if d == nil || a.detail != d {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能关注作者。", func() {
			if a.detail == d {
				a.toggleFollow(d, follow)
			}
		})
		return
	}
	a.submitFollow(&d.follow, follow,
		func() bool { return a.detail == d },
		func() {
			if a.detail == d {
				a.toggleFollow(d, follow)
			}
		})
}

// submitFollow 提交一次关注 / 取关，并把结果写回 fs。帖子详情的作者块与
// 用户资料卡共用这一条路径：两边只有「状态属于谁」不同，current 报告这份
// 状态是否仍属于当前界面，replay 在重新授权成功后重跑同一次操作。
func (a *app) submitFollow(fs *followState, follow bool, current func() bool, replay func()) {
	if fs == nil || fs.busy || !current() || !a.auth.loggedIn() {
		return
	}
	if fs.userID == "" {
		fs.err = "缺少用户标识，暂时无法关注"
		return
	}
	fs.busy = true
	fs.err = ""
	fs.gen++ // 作废可能还在途的关注状态读取
	gen := fs.gen
	userID := fs.userID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), followTimeout)
	go func() {
		defer cancel()
		err := a.client.setFollow(ctx, userID, follow)
		a.publish(func() {
			if !current() || fs.gen != gen {
				return
			}
			fs.busy = false
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话，
			// 按未知处理，当前账号下次渲染时会重新读取。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				fs.known = false
				fs.checked = false
				fs.following = false
				fs.viewer = ""
				fs.need = a.auth.loggedIn()
				return
			}
			if err != nil {
				if a.handleAuthError(err, replay) {
					return
				}
				fs.err = friendlyError(err)
				return
			}
			fs.following = follow
			fs.known = true
			fs.checked = true
			fs.need = false
			fs.viewer = a.auth.username()
			fs.err = ""
		})
	}()
}

// followButton 画头像右侧的关注按钮：未关注显示「关注」，已关注显示
// 「已关注」，点一下切换。关注中是纯主按钮，已关注用低强调的描边按钮，
// 一眼能区分「可以关注」和「已经关注」。
func (a *app) followButton(c *ui.Context, d *detailState) {
	p := d.topic
	if p == nil || !a.canFollowAuthor(p) {
		return
	}
	// 换了登录账号：上个账号的关注状态不能继续用，重新读一次。
	viewer := a.auth.username()
	if a.auth.loggedIn() && d.follow.checked && d.follow.viewer != viewer {
		d.follow.checked = false
		d.follow.known = false
		d.follow.following = false
		d.follow.viewer = ""
		d.follow.need = true
	}
	a.ensureFollowState(d)
	t := c.Theme()
	// 只认属于当前登录账号的状态：换账号后、新状态回来之前按未关注展示，
	// 不能把上一个账号的「已关注」留在按钮上。
	following := a.auth.loggedIn() && d.follow.known &&
		d.follow.viewer == viewer && d.follow.following
	label := "关注"
	if following {
		label = "已关注"
	}
	var btn ui.Element
	if following {
		btn = ui.Button(c, label).
			Background(ui.Transparent).
			Border(1, t.Border).
			TextColor(t.TextMuted)
	} else {
		btn = ui.PrimaryButton(c, label)
	}
	btn.FontSize(12).Padding(4, 12).Radius(6).Width(76).
		Disabled(d.follow.busy)
	if following {
		btn.Tooltip("点击取消关注")
	} else {
		btn.Tooltip("关注这位作者")
	}
	if btn.Clicked() {
		a.toggleFollow(d, !following)
	}
}
