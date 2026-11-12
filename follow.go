package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 关注用户（只保留用户资料卡上的关注入口）
//
//	POST   /v1/users/:userId/follow   关注（scope interact）
//	DELETE /v1/users/:userId/follow   取关（scope interact）
//
// 关注端点要的是用户 uuid（不是 username）。资料卡的账号资料响应自带
// is_following 与 id，所以这里不再单独读用户主页；帖子详情的作者行只展示
// 身份，不提供关注按钮。读不到状态时按钮按未关注展示，点击后以服务端结果
// 为准。

const followTimeout = 20 * time.Second

// followState 是资料卡对某个用户的关注状态缓存。所有字段只在 UI 线程读写；
// 网络结果通过 app.publish 回来。关注失败的原因不落在这里：它是一次操作的
// 结果，用操作所在窗口的 toast 说明（见 submitFollow）。
type followState struct {
	userID    string // 目标用户 uuid，关注 / 取关路径用
	following bool
	known     bool   // 是否已从服务端确认关注状态
	busy      bool   // 正在提交关注 / 取关
	viewer    string // 状态属于哪个登录账号；换账号后重新读取
}

// userPath 是用户主页路径。username 是公开标识，必须转义，避免昵称里的
// 特殊字符改变请求路径。资料卡与「我的」窗口都通过它读账号资料。
func userPath(username string) string {
	return "/v1/users/" + url.PathEscape(username)
}

// userFollowPath 是关注 / 取关路径，参数是用户 uuid。
func userFollowPath(userID string) string {
	return "/v1/users/" + url.PathEscape(userID) + "/follow"
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

// followAuthorIsSelf 判断目标用户是不是当前登录账号：自己的资料卡不展示
// 关注按钮。
func (a *app) followAuthorIsSelf(author string) bool {
	name := strings.TrimSpace(a.auth.username())
	return name != "" && strings.EqualFold(name, strings.TrimSpace(author))
}

// submitFollow 提交一次关注 / 取关，并把结果写回 fs。current 报告这份状态
// 是否仍属于当前界面，replay 在重新授权成功后重跑同一次操作。target 是发起
// 这次操作的窗口，结果就提示在那里，而不是一律回主窗口。
func (a *app) submitFollow(fs *followState, follow bool, target toastTarget, current func() bool, replay func()) {
	if fs == nil || fs.busy || !current() || !a.auth.loggedIn() {
		return
	}
	if fs.userID == "" {
		a.toast(target, "缺少用户标识，暂时无法关注")
		return
	}
	fs.busy = true
	userID := fs.userID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), followTimeout)
	go func() {
		defer cancel()
		err := a.client.setFollow(ctx, userID, follow)
		a.publish(func() {
			if !current() {
				return
			}
			fs.busy = false
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话，
			// 按未知处理，当前账号下次渲染时会重新读取。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				fs.known = false
				fs.following = false
				fs.viewer = ""
				return
			}
			if err != nil {
				if a.handleAuthError(err, replay) {
					return
				}
				a.toast(target, friendlyError(err))
				return
			}
			fs.following = follow
			fs.known = true
			fs.viewer = a.auth.username()
		})
	}()
}
