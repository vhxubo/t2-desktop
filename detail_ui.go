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

	// composer: write a comment (optionally as a nested reply to replyToID).
	draft        string
	replyToID    string
	replyToFloor int
	posting      bool
	postErr      string
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
		// Links to another 2Libra post are intercepted and shown in place;
		// only other http(s) links leave the app. javascript: is dropped.
		OnLink: a.handleLink,
		LoadImage: func(u string) (ui.ImageSource, error) {
			return a.loadImage(u)
		},
		WrapCode: true,
	}
}

// handleLink is the Markdown OnLink. A link to another post replaces the
// current detail view (inline), like following a link in a reader; the
// previous post stays on a small back stack. Self-links are ignored.
func (a *app) handleLink(raw string) {
	if it, ok := parseInAppTopicLink(raw); ok {
		if a.pageName == "detail" && a.detail != nil && it.ShortID == a.detail.shortID {
			return
		}
		a.openDetail(it)
		return
	}
	a.openExternal(raw)
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
	if a.pageName == "detail" && a.detail != nil {
		// Inline navigation: keep the post we came from so "✕ 关闭" walks
		// back through the chain before dropping to the list.
		if len(a.detailStack) >= 20 {
			a.detailStack = a.detailStack[1:]
		}
		a.detailStack = append(a.detailStack, a.detail)
	} else {
		a.detailStack = nil
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
	a.status = ""
	if n := len(a.detailStack); n > 0 {
		a.detail = a.detailStack[n-1]
		a.detailStack = a.detailStack[:n-1]
		return
	}
	a.detail = nil
	a.pageName = "list"
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
	c.SetTheme(homeTheme())
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
			label := "帖子 " + d.shortID
			if d.topic != nil && d.topic.Title != "" {
				label = d.topic.Title
			}
			ui.Text(c, truncate(label, 42)).FontSize(15).Bold().SingleLine().Grow(1)
			if d.focusID != "" {
				ui.Text(c, "来自评论定位").FontSize(11).TextColor(t.TextMuted)
			}
			if n := len(a.detailStack); n > 0 {
				ui.Textf(c, "内联浏览 %d 层", n).FontSize(11).TextColor(t.TextMuted)
			}
			if ui.Button(c, "浏览器打开").Clicked() {
				a.openExternal(d.originURL())
			}
			if ui.Button(c, "刷新").Clicked() {
				a.reloadDetail()
			}
			// 关闭挪到右上角（原左上角的返回），内联浏览时仍先退一层再回列表。
			if ui.Button(c, "✕ 关闭").Clicked() {
				a.closeDetail()
			}
		})
		ui.Divider(c)
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			// 正文与评论各占一半：两栏 Grow(1) 均分剩余宽度，MinWidth(0)
			// 防止长代码/图片把某一栏撑宽；中间用 1px 分隔线而非会吃
			// 宽度的 Spacer（Spacer 默认 Grow(1)，会留下空白带）。
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				ui.Scroll(c).Fill().Padding(14, 18).Gap(10).Children(func() {
					a.topicPane(c, d)
				})
			})
			ui.Divider(c)
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
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
		}).Grow(1).MinHeight(0).Padding(6, 12).Gap(10)
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
	a.replyComposer(c, d)
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
	row := ui.Column(c).FillWidth().Padding(8, 10, 8, left).Radius(8).
		Background(bg).Border(1, border).Gap(4)
	hovered := row.Hovered()
	row.Children(func() {
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
		// Comments are plain text, so give any post link in them an
		// explicit in-app action instead of leaving it unclickable.
		// 评论内的操作（打开链接、回复）均为文字按钮，且只在悬浮整条
		// 评论时才出现，保持列表默认状态的清爽。
		if !r.Deleted && hovered {
			if links := inAppTopicLinks(r.Content); len(links) > 0 {
				ui.Row(c).Gap(6).Wrap().Children(func() {
					for _, link := range links {
						target := link
						a.replyAction(c, "在应用内打开 "+target.ShortID, func() {
							a.handleLink(target.URL)
						})
					}
				})
			}
			// Reply nesting: point the composer at this comment. The server
			// assigns the floor, so we only remember the id and a display
			// floor for the "正在回复 N 楼" chip; closed comments can't reply.
			closed := a.detail != nil && a.detail.topic != nil && a.detail.topic.CommentsClosed
			if !closed {
				a.replyAction(c, "回复", func() {
					a.detail.replyToID = r.ID
					a.detail.replyToFloor = r.Floor
					if a.detail.replyToFloor == 0 {
						a.detail.replyToFloor = r.FlatFloor
					}
				})
			}
		}
	})
}

// replyAction 渲染一个纯文字按钮：透明背景 + 强调色文字，无填充、无边框，
// 用于评论行内的次级操作（在应用内打开、回复）。是否展示由调用方控制
// （通常只在行悬浮时出现）。
func (a *app) replyAction(c *ui.Context, label string, onClick func()) {
	btn := ui.Button(c, label).
		Background(ui.Transparent).
		TextColor(c.Theme().Accent).
		FontSize(12).
		Padding(2, 6).
		Radius(6)
	if btn.Clicked() {
		onClick()
	}
}

// replyComposer sits at the bottom of the comment pane. It is gated the same
// way the API gates writes: anonymous users are sent to log in, closed posts
// show a hint, and a logged-in open post gets a Markdown editor whose send
// button is integrated into the field.
func (a *app) replyComposer(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(10, 12).Gap(8).Children(func() {
		if d.replyToID != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				who := "评论"
				if d.replyToFloor > 0 {
					who = fmt.Sprintf("%d 楼", d.replyToFloor)
				}
				ui.Text(c, "正在回复 "+who).FontSize(12).TextColor(t.Accent).Grow(1)
				a.replyAction(c, "取消", func() {
					d.replyToID = ""
					d.replyToFloor = 0
				})
			})
		}
		// 评论已关闭时不展示编辑器，仅给提示。
		if d.topic != nil && d.topic.CommentsClosed {
			ui.Text(c, "评论已关闭").FontSize(11).TextColor(t.TextMuted)
			return
		}
		a.replyInput(c, d)
	})
}

// replyInput is the Markdown editor for a new comment: a multiline text
// area whose send button lives inside the same bordered, rounded field.
// 未登录时，框内的动作改为「登录后评论」，编辑器本身仍然展示。
func (a *app) replyInput(c *ui.Context, d *detailState) {
	t := c.Theme()
	sendDisabled := d.posting || strings.TrimSpace(d.draft) == ""
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.End).Padding(6, 8).
		Radius(10).Background(t.Background).Border(1, t.Border).Children(func() {
		// 多行文本区即 Markdown 编辑器：透明背景、去边框，融入外层输入框。
		ui.TextArea(c, &d.draft).Label("写评论").
			Placeholder("说点什么…（支持 Markdown 与表情短码 :smile:）").
			Grow(1).Background(ui.Transparent).Border(0, t.Border)
		if d.posting {
			ui.Spinner(c).Size(16, 16)
		}
		// 未登录时把框内动作换成「登录后评论」；否则为集成在框内的发送按钮。
		if !a.auth.loggedIn() {
			if ui.Button(c, "登录后评论").Clicked() {
				a.loginMsg = ""
				a.pageName = "login"
			}
		} else {
			send := ui.PrimaryButton(c, "发送").Disabled(sendDisabled)
			if send.Clicked() {
				a.submitReply()
			}
		}
	})
	if d.postErr != "" {
		ui.Text(c, d.postErr).FontSize(11).TextColor(t.Danger).MaxLines(2)
	}
}

// submitReply posts the draft through the Client. On success it clears the
// box and optimistically prepends the created comment so the author sees it
// immediately; a later refresh re-sorts into the server's floor order.
func (a *app) submitReply() {
	d := a.detail
	if d == nil || d.posting {
		return
	}
	content := strings.TrimSpace(d.draft)
	if content == "" {
		d.postErr = "评论内容不能为空"
		return
	}
	if !a.auth.loggedIn() {
		d.postErr = "请先登录后再评论"
		return
	}
	if d.topic != nil && d.topic.CommentsClosed {
		d.postErr = "该帖子已关闭评论"
		return
	}
	parentID := d.replyToID
	d.posting = true
	d.postErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		created, err := a.client.createReply(ctx, d.shortID, content, parentID)
		a.publish(func() {
			if a.detail != d {
				return // navigated away; drop the stale result
			}
			d.posting = false
			if err != nil {
				d.postErr = friendlyError(err)
				if a.onReplyPosted != nil {
					a.onReplyPosted(replyItem{}, err)
				}
				return
			}
			if created.Author == "" || created.Author == "匿名" {
				if u := a.auth.username(); u != "" {
					created.Author = u
				}
			}
			if created.Floor == 0 && created.FlatFloor > 0 {
				created.Floor = created.FlatFloor
			}
			d.draft = ""
			d.replyToID = ""
			d.replyToFloor = 0
			d.replyList = append([]replyItem{created}, d.replyList...)
			d.replyTotal++
			if a.onReplyPosted != nil {
				a.onReplyPosted(created, nil)
			}
		})
	}()
}
