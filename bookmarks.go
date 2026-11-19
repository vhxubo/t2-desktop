package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// 评论书签（api-write.md）
//
//	POST   /v1/replies/:id/bookmark   {remark?}   scope interact
//	DELETE /v1/replies/:id/bookmark               scope interact
//
// 评论列表带令牌时会下发 is_bookmarked（见 detail.go 的 decodeReply），所以
// 详情页能直接按服务端状态展示「加书签 / 取消书签」，不必先读一次书签列表。
// remark 是可选备注，这里留空：先把「能不能加、能不能删」这条断头路接通，
// 备注是另一个交互。
//
// 书签列表在「我的」窗口的「书签」分段（见 profile.go）。增删之后那份列表
// 已经过期，成功时清掉缓存，下次打开重新拉。

const bookmarkTimeout = 20 * time.Second

// replyBookmarkPath 是书签端点。评论 id 是 uuid，仍按路径段转义，异常数据
// 不会改变请求路径。
func replyBookmarkPath(replyID string) string {
	return "/v1/replies/" + url.PathEscape(replyID) + "/bookmark"
}

// setReplyBookmark 加 / 删书签，走和其它写操作相同的刷新 + 重试链路。目标
// 状态已经成立时服务端可能回 409 conflict（重复加 / 重复移除），按成功处理：
// 书签同样是幂等动作。
func (c *Client) setReplyBookmark(ctx context.Context, replyID string, bookmark bool) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		var err error
		if bookmark {
			// remark 可选；发一个空对象，避免服务端把「没有 body」当成坏请求。
			_, err = c.postJSON(ctx, c.apiURL(replyBookmarkPath(replyID)), token, "", struct{}{})
		} else {
			_, err = c.deleteJSON(ctx, c.apiURL(replyBookmarkPath(replyID)), token)
		}
		var ae *apiError
		if errors.As(err, &ae) &&
			(ae.Status == http.StatusConflict || ae.Status == http.StatusNotFound) {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

// setReplyBookmarked 把一条评论的书签状态写回详情状态里的两份列表。折叠
// 楼中楼时 display 是 replyList 的副本，两份都要改，否则界面上的按钮会
// 停在旧状态。
func setReplyBookmarked(d *detailState, id string, on bool) {
	if d == nil || id == "" {
		return
	}
	apply := func(list []replyItem) {
		for i := range list {
			if list[i].ID == id {
				list[i].Bookmarked = on
				list[i].BookmarkKnown = true
			}
		}
	}
	apply(d.replyList)
	apply(d.display)
}

// replyBookmarked 报告一条评论此刻是否已加书签：只有登录、且服务端明确给过
// 状态时才算「已加」。状态未知时按钮按「加书签」展示，点一下以服务端结果
// 为准。
func (a *app) replyBookmarked(d *detailState, id string) bool {
	if d == nil || id == "" || !a.auth.loggedIn() {
		return false
	}
	for _, r := range d.replyList {
		if r.ID == id {
			return r.BookmarkKnown && r.Bookmarked
		}
	}
	return false
}

// toggleReplyBookmark 是评论底行书签按钮的动作：已加就取消，没加就加上。
// 未登录先走全局授权弹窗，授权成功后同一次操作自动重跑。界面先乐观更新，
// 失败回滚；结果用主窗口 toast 报告。
func (a *app) toggleReplyBookmark(d *detailState, r replyItem) {
	if d == nil || !a.detailLive(d) {
		return
	}
	if r.ID == "" {
		a.toast(a.detailToastFor(d), "缺少评论标识，暂时无法加书签")
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能给评论加书签。", func() {
			if a.detailLive(d) {
				a.submitReplyBookmark(d, r.ID, !a.replyBookmarked(d, r.ID))
			}
		})
		return
	}
	if d.bmBusy != "" {
		return
	}
	a.submitReplyBookmark(d, r.ID, !a.replyBookmarked(d, r.ID))
}

// submitReplyBookmark 提交一次书签变更。desired 是目标状态，重试（网络
// 超时、授权后重放）都带着同一个目标状态，不会因为本地已经乐观改过而反向。
func (a *app) submitReplyBookmark(d *detailState, id string, desired bool) {
	if d == nil || !a.detailLive(d) || id == "" || d.bmBusy != "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能给评论加书签。", func() {
			if a.detailLive(d) {
				a.submitReplyBookmark(d, id, desired)
			}
		})
		return
	}
	d.bmBusy = id
	setReplyBookmarked(d, id, desired)
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), bookmarkTimeout)
	go func() {
		defer cancel()
		err := a.client.setReplyBookmark(ctx, id, desired)
		a.publish(func() {
			if !a.detailLive(d) {
				return
			}
			if d.bmBusy == id {
				d.bmBusy = ""
			}
			// 请求期间换了账号 / 退出登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				setReplyBookmarked(d, id, false)
				return
			}
			if err != nil {
				setReplyBookmarked(d, id, !desired)
				if a.handleAuthError(err, func() {
					a.submitReplyBookmark(d, id, desired)
				}) {
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			if desired {
				a.toast(a.detailToastFor(d), "已加书签")
			} else {
				a.toast(a.detailToastFor(d), "已取消书签")
			}
			a.invalidateBookmarks()
		})
	}()
}

// invalidateBookmarks 让「我的 - 书签」下次打开时重新请求：详情页改了书签，
// 那份列表不能再沿用旧结果。
func (a *app) invalidateBookmarks() {
	if a.profile == nil {
		return
	}
	st := &a.profile.content[profileContentBookmarks]
	if st.loading {
		return // 正在拉的这一页回来后会覆盖缓存，不必插队
	}
	st.loaded = false
	st.items = nil
	st.page = 0
	st.total = 0
	st.totalPages = 0
	st.noMore = false
	st.err = ""
	st.moreErr = ""
}

// removeProfileBookmark 在「我的 - 书签」里直接移除一条书签。成功后把这行
// 从列表里拿掉（列表按页缓存，重拉整页反而会跳），并同步详情页里同一条
// 评论的按钮状态。
func (a *app) removeProfileBookmark(it Item) {
	ps := a.profile
	if ps == nil || it.CommentID == "" || ps.bookmarkBusy != "" {
		return
	}
	id := it.CommentID
	ps.bookmarkBusy = id
	ctx, cancel := context.WithTimeout(context.Background(), bookmarkTimeout)
	go func() {
		defer cancel()
		err := a.client.setReplyBookmark(ctx, id, false)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			if ps.bookmarkBusy == id {
				ps.bookmarkBusy = ""
			}
			if err != nil {
				if a.handleAuthErrorFor(toastProfile, err, func() { a.removeProfileBookmark(it) }) {
					return
				}
				a.toast(toastProfile, friendlyError(err))
				return
			}
			st := &ps.content[profileContentBookmarks]
			kept := st.items[:0]
			for _, row := range st.items {
				if row.CommentID != id {
					kept = append(kept, row)
				}
			}
			st.items = kept
			if st.total > 0 {
				st.total--
			}
			setReplyBookmarked(a.detail, id, false)
			a.toast(toastProfile, "已移除书签")
		})
	}()
}
