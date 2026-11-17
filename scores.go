package main

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// 帖子评分榜
//
//	GET /v1/scores/categories          分类字典
//	GET /v1/scores                     sort=all|high|low、category、page、limit≤30
//
// 两个端点都在 api-economy.md 的「榜单」里，默认需开通。线上响应的形状是：
// categories 为 {categories:[名字...]}；评分列表为
// {items:[{post:{short_id,node:{slug}}, author:{...}, score:{id,target_name,
// score,comment,category,created_at}}], total, page, limit, total_pages}。

const (
	scorePageSize = 30
	// scoreCategoryAll 是下拉框里的「全部分类」；真正发请求时这个值不传。
	scoreCategoryAll = "全部分类"
)

var scoreSortValues = []string{"all", "high", "low"}
var scoreSortLabels = []string{"全部", "高分", "低分"}

// scoreEntry 是一条评分记录：被评的对象、打分与帖子入口。
type scoreEntry struct {
	ID         string
	ShortID    string
	NodeSlug   string
	Target     string
	Score      float64
	ScoreKnown bool
	Comment    string
	Category   string
	CreatedAt  string
	Author     string
	Avatar     string
	IsPro      bool
	Badges     []badgeInfo
}

// scorePage 是归一后的一页评分。
type scorePage struct {
	Items      []scoreEntry
	Total      int
	Page       int
	TotalPages int
}

func decodeScoreCategories(d json.RawMessage) []string {
	o := asObject(d)
	if o == nil {
		return nil
	}
	var out []string
	for _, raw := range rawArray(o["categories"]) {
		var name string
		if json.Unmarshal(raw, &name) == nil && strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

func decodeScores(d json.RawMessage) scorePage {
	out := scorePage{}
	o := asObject(d)
	if o == nil {
		return out
	}
	out.Total = o.num("total")
	out.Page = o.num("page")
	out.TotalPages = o.num("total_pages")
	for _, raw := range rawArray(o["items"]) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		post := item.sub("post")
		score := item.sub("score")
		if score == nil {
			score = item
		}
		author := item.sub("author")
		shortID := post.str("short_id", "shortId")
		if shortID == "" {
			continue
		}
		e := scoreEntry{
			ID:        score.str("id"),
			ShortID:   shortID,
			Target:    score.str("target_name", "targetName"),
			Comment:   score.str("comment"),
			Category:  score.str("category"),
			CreatedAt: score.str("created_at", "createdAt"),
			Author:    author.str("username"),
			Avatar:    authorAvatar(author),
			IsPro:     author.flag("is_pro", "isPro"),
			Badges:    decodeBadges(author),
		}
		if node := post.sub("node"); node != nil {
			e.NodeSlug = node.str("slug")
		}
		if n, ok := score.number("score"); ok {
			e.Score = n
			e.ScoreKnown = true
		}
		if e.ID == "" {
			e.ID = shortID + "|" + e.Target
		}
		out.Items = append(out.Items, e)
	}
	if out.Page <= 0 {
		out.Page = 1
	}
	if out.TotalPages <= 0 && len(out.Items) > 0 {
		out.TotalPages = 1
	}
	if out.Total <= 0 {
		out.Total = len(out.Items)
	}
	return out
}

func scoreQuery(sort, category string, page int) string {
	if page < 1 {
		page = 1
	}
	v := url.Values{}
	v.Set("sort", sort)
	if category != "" && category != scoreCategoryAll {
		v.Set("category", category)
	}
	v.Set("page", strconv.Itoa(page))
	v.Set("limit", strconv.Itoa(scorePageSize))
	return v.Encode()
}

func (c *Client) fetchScoreCategories(ctx context.Context) ([]string, error) {
	return withAuthRetry(c, ctx, func(token string) ([]string, error) {
		d, err := c.getJSON(ctx, c.apiURL("/v1/scores/categories"), token)
		if err != nil {
			return nil, err
		}
		return decodeScoreCategories(d), nil
	})
}

func (c *Client) fetchScores(ctx context.Context, sort, category string, page int) (scorePage, error) {
	u := c.apiURL("/v1/scores") + "?" + scoreQuery(sort, category, page)
	return withAuthRetry(c, ctx, func(token string) (scorePage, error) {
		d, err := c.getJSON(ctx, u, token)
		if err != nil {
			return scorePage{}, err
		}
		return decodeScores(d), nil
	})
}

// scoreText 是界面上的分值：整数不写小数点，缺失时用破折号，不编 0 分。
func scoreText(e scoreEntry) string {
	if !e.ScoreKnown {
		return "—"
	}
	if math.Trunc(e.Score) == e.Score {
		return strconv.Itoa(int(e.Score))
	}
	return strconv.FormatFloat(e.Score, 'f', 1, 64)
}
