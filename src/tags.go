package main

import (
	"context"
	"encoding/json"
	"net/url"
)

// 热门标签
//
//	GET /v1/tags/hot?period=day|week|month   scope：—（匿名可读）
//
// 响应是裸数组，列表字段名不在 {posts, items, list} 里；每项线上实测为
// { tag_id, name, count }，文档只说「name 与热度信息」，所以 tag_id / id 与
// count / post_count 两种写法都认。标签名同时是 /v1/search?type=tags 的
// 关键词，点一下就切到搜索窗口的「标签」结果。

const hotTagsPath = "/v1/tags/hot"

// hotTagPeriods 与搜索窗口里分段控件的顺序一致，值直接照抄文档。
var hotTagPeriods = []string{"day", "week", "month"}

var hotTagPeriodLabels = []string{"日榜", "周榜", "月榜"}

// hotTag 是热门标签的一条。
type hotTag struct {
	ID    string
	Name  string
	Count int
}

// hotTagPeriod 把分段控件的下标换成接口取值。越界时回到 day，与文档默认值
// 一致。
func hotTagPeriod(span int) string {
	if span < 0 || span >= len(hotTagPeriods) {
		return hotTagPeriods[0]
	}
	return hotTagPeriods[span]
}

// hotTagsQuery 是热门标签的查询参数。period 总是显式带上：默认值也在文档里，
// 写出来测试与线上请求都更容易核对。
func hotTagsQuery(period string) string {
	if period == "" {
		period = hotTagPeriods[0]
	}
	v := url.Values{}
	v.Set("period", period)
	return v.Encode()
}

func (c *Client) fetchHotTags(ctx context.Context, period string) ([]hotTag, error) {
	u := c.apiURL(hotTagsPath) + "?" + hotTagsQuery(period)
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, u, token)
	})
	if err != nil {
		return nil, err
	}
	return decodeHotTags(d), nil
}

// decodeHotTags 解析热门标签数组。未知字段忽略；没有名字的条目直接丢掉，
// 免得界面上出现一个点不出结果的空标签。
func decodeHotTags(d json.RawMessage) []hotTag {
	var out []hotTag
	for _, raw := range rawArray(d) {
		o := asObject(raw)
		if o == nil {
			continue
		}
		name := o.str("name")
		if name == "" {
			continue
		}
		out = append(out, hotTag{
			ID:    o.str("tag_id", "id"),
			Name:  name,
			Count: o.num("count", "post_count"),
		})
	}
	return out
}
