package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// detailState is per-post UI state. Background work only publishes results
// through app.publish; no element or context is ever retained here.
type detailState struct {
	shortID, focusID, fallbackURL string
	topic                         *topicDetail
	topicLoading                  bool
	topicErr                      string
	topicGen                      uint64

	replyList             []replyItem
	replyLoading          bool
	replyErr              string
	replyLoaded           bool
	replyPage, replyTotal int
	replyTotalPages       int
	replyGen              uint64
	flat                  bool
	list                  ui.ListState
}

func (d *detailState) originURL() string {
	if d.topic != nil && d.topic.URL != "" {
		if d.focusID != "" {
			return d.topic.URL + "?commentId=" + url.QueryEscape(d.focusID)
		}
		return d.topic.URL
	}
	return d.fallbackURL
}

func newMarkdownOptions(a *app) *markdown.Options {
	return &markdown.Options{
		// Only http(s) links leave the app; relative 2Libra paths get the
		// site origin. javascript: and friends are dropped.
		OnLink: a.openExternal,
		LoadImage: func(u string) (ui.ImageSource, error) {
			return a.loadImage(u)
		},
		WrapCode: true,
	}
}

func (a *app) openExternal(raw string) {
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	if u.Scheme == "" {
		if !strings.HasPrefix(raw, "/") {
			return
		}
		raw = siteBaseURL + raw
	} else if !ui.SafeToOpen(raw) {
		return
	}
	a.srv.OpenURL(raw) // ui.Services.OpenURL must run on the UI thread
}

// loadImage backs markdown images (including the :code: emoji map) with a
// bounded, http(s)-only fetch. ui.DecodeImage handles PNG/JPEG/GIF/SVG.
func (a *app) loadImage(raw string) (ui.ImageSource, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("不支持的图片地址：%s", raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "2libra-native-reader/0.1 (+mygo)")
	resp, err := a.client.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("图片 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ui.DecodeImage(data)
}

func (a *app) openDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	a.detail = &detailState{
		shortID:     it.ShortID,
		focusID:     it.CommentID,
		fallbackURL: it.URL,
		replyPage:   1,
		flat:        it.CommentID != "", // 从评论列表进来用扁平序，更容易找到那层楼
	}
	a.pageName = "detail"
	a.status = ""
	// load* are no-ops when autoLoad is off, so headless UI tests never
	// touch the network.
	a.loadTopic()
	a.loadReplies(1)
	a.ensureEmojis()
}

func (a *app) closeDetail() {
	a.detail = nil
	a.pageName = "list"
	a.status = ""
}

func (a *app) reloadDetail() {
	d := a.detail
	if d == nil {
		return
	}
	d.topicErr, d.replyErr = "", ""
	a.loadTopic()
	a.loadReplies(1)
}

func (a *app) loadTopic() {
	d := a.detail
	if d == nil || d.topicLoading || !a.autoLoad {
		return
	}
	d.topicLoading = true
	d.topicErr = ""
	a.reqSeq++
	gen := a.reqSeq
	d.topicGen = gen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		topic, err := a.client.fetchTopic(ctx, d.shortID)
		a.publish(func() {
			if a.detail != d || d.topicGen != gen {
				return
			}
			d.topicLoading = false
			if err != nil {
				d.topicErr = friendlyError(err)
				return
			}
			d.topic = &topic
		})
	}()
}

func (a *app) loadReplies(page int) {
	d := a.detail
	if d == nil || d.replyLoading || page < 1 || !a.autoLoad {
		return
	}
	d.replyLoading = true
	d.replyErr = ""
	d.replyPage = page
	a.reqSeq++
	gen := a.reqSeq
	d.replyGen = gen
	flat := d.flat
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchReplies(ctx, d.shortID, page, flat)
		a.publish(func() {
			if a.detail != d || d.replyGen != gen {
				return
			}
			d.replyLoading = false
			if err != nil {
				d.replyErr = friendlyError(err)
				return
			}
			rows := flattenReplies(res)
			if page == 1 {
				d.replyList = rows
				d.list.ScrollTo(0, ui.Start)
			} else {
				seen := make(map[string]bool, len(d.replyList))
				for _, r := range d.replyList {
					seen[r.ID] = true
				}
				for _, r := range rows {
					if !seen[r.ID] {
						d.replyList = append(d.replyList, r)
						seen[r.ID] = true
					}
				}
			}
			d.replyTotal = res.Total
			d.replyTotalPages = res.TotalPages
			d.replyPage = page
			d.replyLoaded = true
			if d.focusID != "" {
				for i, r := range d.replyList {
					if r.ID == d.focusID {
						d.list.ScrollTo(i, ui.Start)
						break
					}
				}
			}
		})
	}()
}

// /v1/emojis changes very rarely: one request per app run is enough, as the
// docs recommend. A failure leaves the short codes verbatim.
func (a *app) ensureEmojis() {
	if a.emojiLoading || a.emojiTried || !a.autoLoad {
		return
	}
	a.emojiLoading = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		m, err := a.client.fetchEmojis(ctx)
		a.publish(func() {
			a.emojiLoading = false
			a.emojiTried = true
			if err != nil {
				a.status = "表情表加载失败，正文短码原样显示"
				return
			}
			a.emojis = m
		})
	}()
}

func (a *app) detailView(c *ui.Context) {
	t := c.Theme()
	d := a.detail
	if d == nil {
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Text(c, "没有打开的帖子").TextColor(t.TextMuted)
			if ui.Button(c, "← 返回列表").Clicked() {
				a.pageName = "list"
			}
		})
		return
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				a.closeDetail()
			}
			label := "帖子 " + d.shortID
			if d.topic != nil && d.topic.Title != "" {
				label = d.topic.Title
			}
			ui.Text(c, truncate(label, 42)).FontSize(15).Bold().SingleLine().Grow(1)
			if d.focusID != "" {
				ui.Text(c, "来自评论定位").FontSize(11).TextColor(t.TextMuted)
			}
			if ui.Button(c, "浏览器打开").Clicked() {
				a.openExternal(d.originURL())
			}
			if ui.Button(c, "刷新").Clicked() {
				a.reloadDetail()
			}
		})
		ui.Divider(c)
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).Grow(3).FillHeight().MinHeight(0).Children(func() {
				ui.Scroll(c).Fill().Padding(14, 18).Gap(10).Children(func() {
					a.topicPane(c, d)
				})
			})
			ui.Spacer(c).Width(1).FillHeight().Background(t.Border)
			ui.Column(c).Grow(2).FillHeight().MinHeight(0).Children(func() {
				a.replyPane(c, d)
			})
		})
		ui.Divider(c)
		ui.Row(c).Padding(6, 14).Children(func() {
			ui.Text(c, "内容来自 2Libra 用户，版权归原作者所有。原文链接："+d.originURL()).
				FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		})
	})
}

func (a *app) topicPane(c *ui.Context, d *detailState) {
	t := c.Theme()
	if d.topicLoading && d.topic == nil {
		ui.Spinner(c).Size(24, 24)
		ui.Text(c, "正在加载正文…").TextColor(t.TextMuted)
		return
	}
	if d.topicErr != "" {
		ui.Text(c, "正文加载失败").FontSize(15).Bold()
		ui.Text(c, d.topicErr).TextColor(t.Danger).MaxLines(4)
		if ui.Button(c, "重试").Clicked() {
			a.loadTopic()
		}
		return
	}
	p := d.topic
	if p == nil {
		ui.Text(c, "暂无正文").TextColor(t.TextMuted)
		return
	}
	ui.Text(c, p.Title).FontSize(20).Bold()
	meta := []string{p.Author}
	if p.AuthorLevel > 0 {
		meta = append(meta, fmt.Sprintf("Lv.%d", p.AuthorLevel))
	}
	if p.NodeName != "" {
		meta = append(meta, p.NodeName)
	}
	if p.CreatedAt != "" {
		meta = append(meta, relativeTime(p.CreatedAt))
	}
	if p.Views > 0 {
		meta = append(meta, fmt.Sprintf("%d 浏览", p.Views))
	}
	if p.CommentCount > 0 {
		meta = append(meta, fmt.Sprintf("%d 回复", p.CommentCount))
	}
	if p.IsPinned {
		meta = append(meta, "置顶")
	}
	if p.CommentsClosed {
		meta = append(meta, "评论已关闭")
	}
	ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(t.TextMuted)
	if len(p.Tags) > 0 {
		ui.Text(c, "#"+strings.Join(p.Tags, "  #")).FontSize(11).TextColor(t.TextMuted)
	}
	ui.Divider(c)
	if strings.TrimSpace(p.Content) == "" {
		ui.Text(c, "（正文为空）").TextColor(t.TextMuted)
	} else {
		markdown.View(c, replaceEmoji(p.Content, a.emojis, true), a.md).FillWidth()
	}
	for i, ps := range p.Postscripts {
		ui.Divider(c)
		label := fmt.Sprintf("附言 %d", i+1)
		if ps.CreatedAt != "" {
			label += " · " + relativeTime(ps.CreatedAt)
		}
		ui.Text(c, label).FontSize(13).Bold()
		markdown.View(c, replaceEmoji(ps.Content, a.emojis, true), a.md).FillWidth()
	}
	if v := p.Vote; v != nil {
		ui.Divider(c)
		ui.Column(c).FillWidth().Padding(10, 12).Gap(4).Radius(8).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "投票 · "+v.Title).Bold()
			for _, o := range v.Options {
				ui.Textf(c, "%s · %d 票", o.Text, o.Votes).FontSize(12)
			}
			if v.Finished {
				ui.Text(c, "投票已结束").FontSize(11).TextColor(t.TextMuted)
			}
		})
	}
	ui.Divider(c)
	if ui.Button(c, "在浏览器打开原文").Clicked() {
		a.openExternal(d.originURL())
	}
	ui.Text(c, "本应用由第三方开发者独立开发运营，与 2Libra 无隶属或合作关系。").
		FontSize(10).TextColor(t.TextMuted)
}

func (a *app) replyPane(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Row(c).Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Textf(c, "评论 · %d 条", d.replyTotal).FontSize(14).Bold()
		ui.Spacer(c).Grow(1)
		view := "切到扁平时间序"
		if d.flat {
			view = "切到楼层视图"
		}
		if ui.Button(c, view).Disabled(d.replyLoading).Clicked() {
			d.flat = !d.flat
			d.replyList = nil
			d.replyLoaded = false
			a.loadReplies(1)
		}
	})
	ui.Divider(c)
	switch {
	case d.replyLoading && len(d.replyList) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载评论…").TextColor(t.TextMuted)
		})
	case d.replyErr != "":
		ui.Column(c).Grow(1).Center().Gap(8).Padding(16).Children(func() {
			ui.Text(c, "评论加载失败").Bold()
			ui.Text(c, d.replyErr).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.loadReplies(d.replyPage)
			}
		})
	case len(d.replyList) == 0:
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "暂无评论").TextColor(t.TextMuted)
		})
	default:
		rows := d.replyList
		d.list.Key = func(i int) any {
			if i >= 0 && i < len(rows) && rows[i].ID != "" {
				return rows[i].ID
			}
			return i
		}
		ui.List(c, &d.list, len(rows), func(i int) {
			a.replyRow(c, rows[i], d.focusID)
		}).Grow(1).MinHeight(0).Padding(4, 8)
	}
	ui.Divider(c)
	ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		page := d.replyPage
		if page < 1 {
			page = 1
		}
		ui.Textf(c, "第 %d 页 / 共 %d 页", page, max(1, d.replyTotalPages)).
			FontSize(11).TextColor(t.TextMuted).Grow(1)
		if ui.Button(c, "上一页").Disabled(page <= 1 || d.replyLoading).Clicked() {
			a.loadReplies(page - 1)
		}
		hasMore := d.replyLoaded && !d.replyLoading &&
			(d.replyTotalPages == 0 || page < d.replyTotalPages)
		if ui.Button(c, "下一页").Disabled(!hasMore).Clicked() {
			a.loadReplies(page + 1)
		}
	})
}

func (a *app) replyRow(c *ui.Context, r replyItem, focusID string) {
	t := c.Theme()
	bg := t.Surface
	border := t.Border
	if r.Pinned {
		border = t.Accent
	}
	if focusID != "" && r.ID != "" && r.ID == focusID {
		bg = t.SurfaceHover
		border = t.Accent
	}
	left := float32(10)
	left += float32(r.Depth) * 14
	ui.Column(c).FillWidth().Padding(8, 10, 8, left).Radius(8).
		Background(bg).Border(1, border).Gap(4).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			floor := ""
			switch {
			case r.Pinned:
				floor = "置顶"
			case r.Floor > 0:
				floor = fmt.Sprintf("%d 楼", r.Floor)
			case r.FlatFloor > 0:
				floor = fmt.Sprintf("%d 楼", r.FlatFloor)
			case r.LocatedFloor > 0:
				floor = fmt.Sprintf("%d 楼", r.LocatedFloor)
			}
			if floor != "" {
				ui.Text(c, floor).FontSize(11).TextColor(t.TextMuted)
			}
			ui.Text(c, r.Author).FontSize(12).Bold()
			if r.CreatedAt != "" {
				ui.Text(c, relativeTime(r.CreatedAt)).FontSize(11).TextColor(t.TextMuted)
			}
			ui.Spacer(c).Grow(1)
			if r.Reactions != "" {
				ui.Text(c, r.Reactions).FontSize(11).TextColor(t.TextMuted)
			}
		})
		if r.ParentSummary != "" || r.ParentFloor > 0 {
			ui.Textf(c, "回复 %d 楼：%s", r.ParentFloor, r.ParentSummary).
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		}
		content := r.Content
		if r.Deleted {
			content = "该评论已删除"
		}
		if strings.TrimSpace(content) == "" {
			content = "（无内容）"
		}
		ui.Text(c, replaceEmoji(content, a.emojis, false)).FontSize(13)
	})
}
