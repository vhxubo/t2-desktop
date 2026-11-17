package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// 头像（api-economy.md 的「头像」）
//
//	POST   /v1/me/avatar   multipart，字段名 file；scope write:economy，需开通
//	DELETE /v1/me/avatar   重置为默认头像（同上）
//
// 与 /v1/upload/image 的区别：头像会被服务端重编码成固定尺寸，单张上限 5 MB，
// 每人每天 2 次；首次设置免费，之后换头像可能扣金币（服务端决定金额，客户端
// 不猜）。返回的 avatar_url 已经是完整地址，直接替换本机会话里的头像。

const (
	avatarPath = "/v1/me/avatar"
	// avatarMaxBytes 是头像接口自己的上限，比图床的 10 MB 更小。
	avatarMaxBytes = 5 << 20
)

// decodeAvatarURL 从换头像 / 重置头像的响应里取地址。重置时服务端给 null，
// 这里解成空串，界面据此回到昵称首字母。
func decodeAvatarURL(d json.RawMessage) string {
	return asObject(d).str("avatar_url", "avatarUrl", "avatar")
}

// uploadAvatar 走 multipart 上传一张新头像。401 与读端点一样先刷新令牌再重传；
// 头像没有幂等键，重复上传同一张图最多算两次额度，不会重复扣费以外的副作用。
func (c *Client) uploadAvatar(ctx context.Context, src uploadSource, data []byte) (string, error) {
	var url string
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		auth := ""
		if token != "" {
			auth = "Bearer " + token
		}
		body, status, err := c.postFile(ctx, c.apiURL(avatarPath), auth, nil, nil,
			"file", src.Name, src.Mime, data)
		if err != nil {
			return struct{}{}, err
		}
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return struct{}{}, fmt.Errorf("换头像响应不是 {c,m,d,t} 信封（HTTP %d）：%w", status, err)
		}
		if status >= 400 || env.C != 0 {
			return struct{}{}, &apiError{
				Status:        status,
				Code:          env.Code,
				Message:       env.M,
				RequiredScope: env.RequiredScope,
				RetryAfter:    env.RetryAfter,
			}
		}
		url = decodeAvatarURL(env.D)
		if url == "" {
			return struct{}{}, errors.New("换头像成功但响应里没有头像地址")
		}
		return struct{}{}, nil
	})
	return url, err
}

// deleteAvatar 重置为默认头像。响应里的 avatar_url 为 null，解成空串。
func (c *Client) deleteAvatar(ctx context.Context) (string, error) {
	var url string
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.deleteJSON(ctx, c.apiURL(avatarPath), token)
		if err != nil {
			return struct{}{}, err
		}
		url = decodeAvatarURL(d)
		return struct{}{}, nil
	})
	return url, err
}

/* ------------------------------------------------------------------
   本机状态：会话里的头像、账号索引与「我的」资料头
--------------------------------------------------------------- */

// openAvatarPopup 打开「更换头像」气泡。未登录时先走全局授权弹窗。
func (a *app) openAvatarPopup(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能更换头像。", func() {
			if a.profile == ps {
				a.openAvatarPopup(ps)
			}
		})
		return
	}
	ps.avatarOpen = true
	ps.avatarErr = ""
}

// pickAvatar 打开系统文件选择框。headless 测试没有原生对话框，用
// a.pickImagePaths 换成直接返回一个路径。
func (a *app) pickAvatar(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能更换头像。", func() {
			if a.profile == ps {
				a.pickAvatar(ps)
			}
		})
		return
	}
	var paths []string
	var err error
	if a.pickImagePaths != nil {
		paths, err = a.pickImagePaths()
	} else {
		parent := a.profileWin
		if parent == nil {
			parent = a.win
		}
		paths, err = openAvatarDialog(parent)
	}
	if err != nil {
		ps.avatarErr = "选择图片失败：" + err.Error()
		return
	}
	if len(paths) == 0 {
		return
	}
	a.uploadAvatarFile(ps, paths[0])
}

// uploadAvatarFile 校验本地图片后上传。大小与真实格式由 inspectImage 判断
// （只看文件头，不信扩展名），上限用头像接口自己的 5 MB。
func (a *app) uploadAvatarFile(ps *profileState, path string) {
	if ps == nil || a.profile != ps || ps.avatarBusy || path == "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能更换头像。", func() {
			if a.profile == ps {
				a.uploadAvatarFile(ps, path)
			}
		})
		return
	}
	src, err := inspectImage(uploadSource{Path: path, Name: filepath.Base(path)}, avatarMaxBytes)
	if err != nil {
		ps.avatarErr = err.Error()
		return
	}
	data, err := src.bytes()
	if err != nil {
		ps.avatarErr = "读取图片失败：" + err.Error()
		return
	}
	ps.avatarBusy = true
	ps.avatarErr = ""
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		url, err := a.client.uploadAvatar(ctx, src, data)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			ps.avatarBusy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err, func() {
					a.uploadAvatarFile(ps, path)
				}) {
					return
				}
				ps.avatarErr = friendlyError(err)
				return
			}
			a.applyAvatar(ps, url)
			ps.avatarOpen = false
			a.toast(toastProfile, "头像已更新")
			a.coinsChanged(coinEventAvatar)
		})
	}()
}

// resetAvatar 调用 DELETE 恢复默认头像。结果同样是服务端给的地址（null）。
func (a *app) resetAvatar(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能重置头像。", func() {
			if a.profile == ps {
				a.resetAvatar(ps)
			}
		})
		return
	}
	ps.avatarBusy = true
	ps.avatarErr = ""
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		url, err := a.client.deleteAvatar(ctx)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			ps.avatarBusy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err, func() { a.resetAvatar(ps) }) {
					return
				}
				ps.avatarErr = friendlyError(err)
				return
			}
			a.applyAvatar(ps, url)
			ps.avatarOpen = false
			a.toast(toastProfile, "已恢复默认头像")
			a.coinsChanged(coinEventAvatar)
		})
	}()
}

// applyAvatar 把服务端确认的新地址写回资料头、会话与账号索引：标题栏、
// 「我的」窗口和账号切换面板都读这几份状态，任何一份落下都会显示旧头像。
func (a *app) applyAvatar(ps *profileState, url string) {
	if ps == nil {
		return
	}
	ps.user.Avatar = url
	a.auth.setAvatar(url)
	if a.book != nil && a.accountID != "" {
		a.book.setAccountAvatar(a.accountID, url)
	}
	if url != "" {
		a.prefetchAvatars(url)
	}
	if a.win != nil {
		a.win.Invalidate()
	}
	a.redrawProfileWindow()
}
