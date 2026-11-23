package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 头像（api-economy.md 的「头像」）
//
//	POST   /v1/me/avatar   multipart，字段名 file；scope write:economy，需开通
//	DELETE /v1/me/avatar   重置为默认头像（同上）
//
// 与 /v1/upload/image 的区别：头像会被服务端重编码成固定尺寸，单张上限 5 MB，
// 每人每天 2 次；换头像消耗 500 金币（首次设置是否免费由服务端决定，客户端不
// 参与记账，界面按 500 提前说明）。返回的 avatar_url 已经是完整地址，直接替换
// 本机会话里的头像。
//
// 选好本地图片后先进 avatar_crop.go 的裁剪编辑器，确认后才上传这里准备好的
// 512×512 PNG；未确认的图片不会发请求，也不会扣金币。
//
// avatar_url 的路径是不透明的随机标识（v1.5 起不再含账号信息，历史地址的形态
// 可能不同）：只原样展示与缓存，不解析路径、不据此推断用户，也不用它当身份
// 标识——用户用用户名 / 账号 id 区分。

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

// openAvatarPopup 在点「更换头像」时确认登录。气泡的开合由 Lobe 的 Popover
// 自己处理（点锚点切换、点窗外与 Esc 关闭），这里只负责未登录时先走授权，并在
// 授权回来后把气泡打开。
func (a *app) openAvatarPopup(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能更换头像。")
		return
	}
	ps.avatarErr = ""
}

// pickAvatar 打开系统文件选择框。headless 测试没有原生对话框，用
// a.pickImagePaths 换成直接返回一个路径。
func (a *app) pickAvatar(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能更换头像。")
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
	// 选好图片先进裁剪编辑器，用户确认后才真正上传（见 avatar_crop.go）。
	a.beginAvatarCrop(ps, paths[0])
}

// uploadAvatarData 上传已经准备好的图片字节。头像接口的 5 MB 上限与真实
// 格式仍由 inspectImage 把关；失败原因写回裁剪框或气泡，用户可以直接重试。
func (a *app) uploadAvatarData(ps *profileState, name string, data []byte) {
	if ps == nil || a.profile != ps || ps.avatarBusy || len(data) == 0 {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能更换头像。")
		return
	}
	src, err := inspectImage(uploadSource{Name: name, Data: data}, avatarMaxBytes)
	if err != nil {
		a.avatarFail(ps, err.Error())
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
				// 授权流程接管后只恢复忙碌标记，不再重复展示普通错误。
				if a.handleAuthErrorFor(toastProfile, err) {
					if ps.avatarCrop.open {
						ps.avatarCrop.busy = false
					}
					return
				}
				a.avatarFail(ps, friendlyError(err))
				return
			}
			a.applyAvatar(ps, url)
			ps.avatarOpen = false
			a.closeAvatarCrop(ps)
			a.toast(toastProfile, fmt.Sprintf("头像已更新，本次消耗 %d 金币", avatarCost))
		})
	}()
}

// avatarFail 按当前阶段把失败原因写回裁剪框或换头像气泡。
func (a *app) avatarFail(ps *profileState, msg string) {
	if ps == nil {
		return
	}
	if ps.avatarCrop.open {
		ps.avatarCrop.busy = false
		ps.avatarCrop.err = msg
		return
	}
	ps.avatarErr = msg
}

// resetAvatar 调用 DELETE 恢复默认头像。结果同样是服务端给的地址（null）。
func (a *app) resetAvatar(ps *profileState) {
	if ps == nil || a.profile != ps || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能重置头像。")
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
				if a.handleAuthErrorFor(toastProfile, err) {
					return
				}
				ps.avatarErr = friendlyError(err)
				return
			}
			a.applyAvatar(ps, url)
			ps.avatarOpen = false
			a.toast(toastProfile, "已恢复默认头像")
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
