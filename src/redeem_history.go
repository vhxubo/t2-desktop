package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// 「我兑换过的兑换码」（api-read.md 的「我的」）
//
//	GET /v1/me/redeem-codes   scope read:me，page / limit(≤50)，按兑换时间倒序
//
// 每行带兑换码明文、红包备注（可为 null）、兑换时间、帖子上的码描述，以及
// 来源帖子（short_id / 标题 / 是否已删除 / 节点 / 作者）。帖子已删除时仍会
// 返回，界面要把标题降级成不可点击；作者注销时是 deleted_user，这里不关心。
//
// 领取仍走 POST /v1/redeem-codes/:code/redeem（见 redeem.go）——本端点只能看。

// redeemedCode 是一行兑换记录。PostShortID 为空或 PostDeleted 为真时不能
// 打开原帖。
type redeemedCode struct {
	ID          string
	Code        string
	Note        string
	Description string
	RedeemedAt  string

	PostShortID string
	PostTitle   string
	PostDeleted bool
	PostNode    string
}

// redeemedCodePage 是一页兑换记录。字段与其它分页端点同口径。
type redeemedCodePage struct {
	Items      []redeemedCode
	Total      int
	Page       int
	Limit      int
	TotalPages int
}

// fetchRedeemedCodes 拉第 page 页兑换记录。它是只读端点，但必须带令牌
// （scope read:me），因此走 withAuthRetry：401 时刷新一次并重试。
func (c *Client) fetchRedeemedCodes(ctx context.Context, page int) (redeemedCodePage, error) {
	return withAuthRetry(c, ctx, func(token string) (redeemedCodePage, error) {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(redeemedCodesLimit))
		d, err := c.getJSON(ctx, c.apiURL(redeemedCodesPath)+"?"+q.Encode(), token)
		if err != nil {
			return redeemedCodePage{}, err
		}
		return decodeRedeemedCodes(d, page), nil
	})
}

// decodeRedeemedCodes 解一页兑换记录。列表字段是 items（文档明确），行内的
// 来源帖放在 post 里；未知字段忽略。
func decodeRedeemedCodes(d json.RawMessage, requestedPage int) redeemedCodePage {
	out := redeemedCodePage{Page: requestedPage, Limit: redeemedCodesLimit}
	o := asObject(d)
	if o == nil {
		return out
	}
	out.Total = rawInt(o["total"])
	out.Page = rawInt(o["page"])
	out.Limit = rawInt(o["limit"])
	out.TotalPages = rawInt(o["total_pages"])
	for _, raw := range rawArray(o["items"]) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		code := item.str("code")
		if code == "" {
			continue // 没有码的行没有可展示的主信息，跳过
		}
		row := redeemedCode{
			ID:          item.str("id"),
			Code:        code,
			Note:        cleanText(item.str("note")),
			Description: cleanText(item.str("code_description")),
			RedeemedAt:  item.str("redeemed_at"),
		}
		if post := item.sub("post"); post != nil {
			row.PostShortID = post.str("short_id")
			row.PostTitle = cleanText(post.str("title"))
			row.PostDeleted = post.flag("is_deleted")
			if node := post.sub("node"); node != nil {
				row.PostNode = node.str("name")
			}
		}
		out.Items = append(out.Items, row)
	}
	if out.Page <= 0 {
		out.Page = requestedPage
	}
	if out.Limit <= 0 {
		out.Limit = redeemedCodesLimit
	}
	if out.Total <= 0 {
		out.Total = len(out.Items)
	}
	if out.TotalPages <= 0 {
		out.TotalPages = 1
	}
	return out
}
