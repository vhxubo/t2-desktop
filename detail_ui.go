package main

import (
	"context"
	"fmt"
	"io"
	"log"
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

	replyList    []replyItem
	replyLoading bool
	replyErr     string
	// replyMoreErr 是「续拉下一页评论」失败的原因：已经显示出来的评论
	// 不能被整页错误替换，只在列表底部提示并可重试。
	replyMoreErr          string
	replyLoaded           bool
	replyPage, replyTotal int
	replyTotalPages       int
	replyNoMore           bool // 评论已到底（空页 / 无新增），停止无限滚动续拉
	replyGen              uint64
	flat                  bool
	list                  ui.ListState

	// 正文作者头像：topic 加载后按 URL 取一次位图，回 UI 线程缓存，
	// 头像块（左下角）画首字母占位直到位图到达（或消失）。
	topicAvatar        *ui.Bitmap
	topicAvatarURL     string
	topicAvatarLoading bool

	// composer: write a comment (optionally as a nested reply to replyToID).
	draft        string
	replyToID    string
	replyToFloor int
	posting      bool
	postErr      string
	// composerField is the input's identity across builds: its committed
	// focus decides whether the composer rests at one line or unfolds.
	composerField ui.Handle
	// composerIdle folds the composer back after a comment was sent, until
	// the input is selected again or the next draft is typed.
	composerIdle bool
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

// commentFontSize 是评论正文的字号：比帖子正文小一号，评论区读起来更紧凑。
const commentFontSize float32 = 13

// newMarkdownOptions 造一套 Markdown 渲染选项。size 是这类视图的基准字号，
// 0 表示跟随主题（帖子正文）；评论区传 commentFontSize，标题、代码、列表
// 缩进等以 em 计的长度都会按这个字号换算。
func newMarkdownOptions(a *app, size float32) *markdown.Options {
	o := &markdown.Options{
		// Links to another 2Libra post are intercepted and shown in place;
		// only other http(s) links leave the app. javascript: is dropped.
		OnLink: a.handleLink,
		LoadImage: func(u string) (ui.ImageSource, error) {
			return a.loadImage(u)
		},
		// Markdown 默认只画整段图片，段落内的配图与 :code: 表情会只剩
		// [说明]；这类段落改由 article_render.go 接管。
		Block:    a.articleBlockAt(size),
		WrapCode: true,
	}
	if size > 0 {
		// 只覆盖字号，其余颜色、字体与间距由 Markdown 默认主题补齐。
		o.Theme = &markdown.Theme{FontSize: size}
	}
	return o
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
	data, err := a.fetchImage(raw)
	if err != nil {
		return nil, err
	}
	return ui.DecodeImage(data)
}

// loadBitmap fetches an image for widgets that take a bitmap (Avatar, which
// crops it to a circle).
func (a *app) loadBitmap(raw string) (*ui.Bitmap, error) {
	data, err := a.fetchImage(raw)
	if err != nil {
		return nil, err
	}
	return ui.DecodeBitmap(data)
}

func (a *app) fetchImage(raw string) ([]byte, error) {
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
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// ensureAvatar loads the signed-in account's avatar once per URL, off the UI
// thread, and publishes the bitmap back; the title bar draws initials until
// it arrives (or when the profile has no picture).
func (a *app) ensureAvatar() {
	if !a.autoLoad || a.avatarLoading {
		return // headless tests never touch the network
	}
	u := a.auth.avatarURL()
	if u == "" || (u == a.avatarURL && a.avatar != nil) {
		return
	}
	a.avatarURL = u
	a.avatarLoading = true
	go func() {
		bmp, err := a.loadBitmap(u)
		a.publish(func() {
			a.avatarLoading = false
			if err != nil {
				log.Printf("头像加载失败：%v", err)
				return
			}
			a.avatar = bmp
		})
	}()
}

// ensureTopicAvatar loads the post author's avatar once per URL, off the UI
// thread, and publishes the bitmap back; the bottom-left author block draws
// the nickname's initial until it arrives (or when the topic has no picture).
func (a *app) ensureTopicAvatar(d *detailState) {
	if !a.autoLoad || d.topicAvatarLoading {
		return // headless tests never touch the network
	}
	if d.topic == nil {
		return
	}
	u := d.topic.Avatar
	if u == "" || (u == d.topicAvatarURL && d.topicAvatar != nil) {
		return
	}
	d.topicAvatarURL = u
	d.topicAvatarLoading = true
	go func() {
		bmp, err := a.loadBitmap(u)
		a.publish(func() {
			d.topicAvatarLoading = false
			if err != nil {
				log.Printf("正文作者头像加载失败：%v", err)
				return
			}
			d.topicAvatar = bmp
		})
	}()
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
			a.ensureTopicAvatar(d)
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
	d.replyMoreErr = ""
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
				if page > 1 && len(d.replyList) > 0 {
					// 已经有评论时续拉失败：保留列表，在底部提示并可重试。
					d.replyMoreErr = friendlyError(err)
				} else {
					d.replyErr = friendlyError(err)
				}
				return
			}
			d.replyMoreErr = ""
			rows := flattenReplies(res)
			if page == 1 {
				d.replyList = rows
				d.list.ScrollTo(0, ui.Start)
				d.replyNoMore = len(rows) == 0
			} else {
				seen := make(map[string]bool, len(d.replyList))
				for _, r := range d.replyList {
					seen[r.ID] = true
				}
				added := 0
				for _, r := range rows {
					if !seen[r.ID] {
						d.replyList = append(d.replyList, r)
						seen[r.ID] = true
						added++
					}
				}
				// 服务端返回空页或没有新增条目：标记到底，避免无限加载。
				d.replyNoMore = added == 0
			}
			d.replyTotal = res.Total
			d.replyTotalPages = res.TotalPages
			d.replyPage = page
			d.replyLoaded = true
			// 只定位首屏：续拉后面的页时再跳回被定位的评论，会把用户
			// 刚滚到的位置猛地拽走。
			if page == 1 && d.focusID != "" {
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
	a.titleBar(c)
	ui.Divider(c)
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
	ui.Column(c).Grow(1).FillWidth().Children(func() {
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
			// 右上角纯图标关闭，无边框、无填充；内联浏览时仍先退一层再回列表。
			closeBtn := ui.Button(c, "").
				Background(ui.Transparent).
				Border(0, t.Border).
				TextColor(t.Text).
				FontSize(14).
				Padding(6, 6).
				Radius(6)
			closeBtn.Children(func() {
				ui.Icon(c, closeIcon).Size(18, 18)
			})
			closeBtn.Label("关闭详情")
			if closeBtn.Clicked() {
				a.closeDetail()
			}
		})
		ui.Divider(c)
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			// 正文与评论各占一半：两栏 Grow(1) 均分剩余宽度，MinWidth(0)
			// 防止长代码/图片把某一栏撑宽；中间用 1px 分隔线而非会吃
			// 宽度的 Spacer（Spacer 默认 Grow(1)，会留下空白带）。
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				// 正文可滚动（Grow(1) 吃满左栏剩余高度，MinHeight(0) 允许
				// 它收缩到比正文内容更矮，正文在框内滚动，底栏不被顶出）；
				// 底部固定作者头像 + 昵称 + 分类（节点）+ 标签，不随正文滚动。
				ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(14, 18).Gap(10).Children(func() {
					a.topicBody(c, d)
				})
				a.topicAuthorBlock(c, d)
			})
			ui.Divider(c)
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				a.replyPane(c, d)
			})
		})
		// 数据来源声明 + 可点击的原文链接占满整个窗口底部，放在最底部，
		// 正文加载完成（d.topic 非空）时才出现。
		a.topicSourceFooter(c, d)
	})
}

func (a *app) topicBody(c *ui.Context, d *detailState) {
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
	// 顶部只保留帖子级元信息（等级 / 置顶 / 评论已关闭）。时间、浏览、回复数
	// 不再展示；作者头像 + 昵称、分类（节点）与标签收敛到左栏底部的固定块
	// （topicAuthorBlock），数据来源 + 原文链接收敛到窗口最底部的固定栏
	// （topicSourceFooter），避免顶部信息过载。
	var meta []string
	if p.AuthorLevel > 0 {
		meta = append(meta, fmt.Sprintf("Lv.%d", p.AuthorLevel))
	}
	if p.IsPinned {
		meta = append(meta, "置顶")
	}
	if p.CommentsClosed {
		meta = append(meta, "评论已关闭")
	}
	if len(meta) > 0 {
		ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(t.TextMuted)
	}
	// 元信息行与正文之间不再画分隔线：这条线在正文顶端显得像多余的一条，
	// 没有元信息时更只剩一条孤线。间距由 Scroll 的 Gap 承担。
	// 帖子自己的 emojis 可能包含社区自定义图标；并到全局表情表后一起渲染。
	emojis := mergeEmojiMaps(a.emojis, p.Emojis)
	if strings.TrimSpace(p.Content) == "" {
		ui.Text(c, "（正文为空）").TextColor(t.TextMuted)
	} else {
		markdown.View(c, replaceEmoji(p.Content, emojis, true), a.md).FillWidth()
	}
	for i, ps := range p.Postscripts {
		ui.Divider(c)
		label := fmt.Sprintf("附言 %d", i+1)
		if ps.CreatedAt != "" {
			label += " · " + relativeTime(ps.CreatedAt)
		}
		ui.Text(c, label).FontSize(13).Bold()
		postEmojis := mergeEmojiMaps(a.emojis, p.Emojis, ps.Emojis)
		markdown.View(c, replaceEmoji(ps.Content, postEmojis, true), a.md).FillWidth()
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
}

// topicAuthorBlock 是左栏底部的固定块，不随正文滚动：头像 + 昵称 + 分类（节点），
// 昵称下方放标签。正文加载完成（d.topic 非空）时才出现。
func (a *app) topicAuthorBlock(c *ui.Context, d *detailState) {
	p := d.topic
	if p == nil {
		return
	}
	t := c.Theme()
	ui.Divider(c)
	ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Avatar(c, p.Author, d.topicAvatar).Size(32, 32)
		ui.Column(c).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, p.Author).FontSize(14).Bold()
				a.authorBadges(c, p.Badges)
				if p.NodeName != "" {
					ui.Text(c, p.NodeName).FontSize(12).TextColor(t.TextMuted)
				}
			})
			if len(p.Tags) > 0 {
				ui.Text(c, "#"+strings.Join(p.Tags, "  #")).FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}

// topicSourceFooter 占满整个窗口底部，放在最底部：数据来源声明 + 可点击原文
// 链接（点一下用系统浏览器打开）。正文加载完成（d.topic 非空）时才出现。
func (a *app) topicSourceFooter(c *ui.Context, d *detailState) {
	p := d.topic
	if p == nil {
		return
	}
	t := c.Theme()
	ui.Divider(c)
	ui.Row(c).Padding(6, 14).Gap(4).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(c, "内容来自 2Libra 用户，版权归原作者所有。").FontSize(10).TextColor(t.TextMuted)
		ui.Text(c, "原文链接：").FontSize(10).TextColor(t.TextMuted)
		link := ui.Button(c, truncate(d.originURL(), 60)).
			Background(ui.Transparent).Border(0, t.Border).
			TextColor(t.Accent).FontSize(10).Padding(0, 2)
		if link.Clicked() {
			a.openExternal(d.originURL())
		}
	})
}

func (a *app) replyPane(c *ui.Context, d *detailState) {
	t := c.Theme()
	switch {
	case d.replyLoading && len(d.replyList) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(24, 24)
			ui.Text(c, "正在加载评论…").TextColor(t.TextMuted)
		})
	case d.replyErr != "" && len(d.replyList) == 0:
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
		a.loadMoreFooter(c, d.replyMoreErr, d.replyLoading, func() {
			a.loadReplies(d.replyPage)
		})
		// 无限滚动：滚到底且还有下一页时自动加载更多，移除原来的页码与上下页按钮。
		// 与首页 / 通知列表保持一致：AtEnd 触发、replyNoMore 兜底停止；
		// 续拉失败后停在底部提示上，等用户点「重试」，不再自动重发。
		if replyCanLoadMore(d) && d.replyMoreErr == "" && d.list.AtEnd() {
			a.loadReplies(d.replyPage + 1)
		}
	}
	a.replyComposer(c, d)
}

// replyCanLoadMore 汇报是否还能（且应）续拉下一页评论：已加载、未在读、未到底、
// 且仍有下一页。totalPages 缺失（为 0）时按空页去重兜底，继续尝试续拉。
func replyCanLoadMore(d *detailState) bool {
	if !d.replyLoaded || d.replyLoading || d.replyNoMore {
		return false
	}
	return d.replyTotalPages == 0 || d.replyPage < d.replyTotalPages
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
	// 评论已关闭时整条都不能回复；已删除的评论同样不提供回复入口。
	closed := a.detail != nil && a.detail.topic != nil && a.detail.topic.CommentsClosed
	row.Children(func() {
		// 头部行固定高度，给右上角的「回复」按钮预留空间：悬浮时按钮出现
		// 也不会把整条评论撑高，避免显示时上下跳动。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Height(18).Children(func() {
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
			a.authorBadges(c, r.Badges)
			if r.CreatedAt != "" {
				ui.Text(c, relativeTime(r.CreatedAt)).FontSize(11).TextColor(t.TextMuted)
			}
			ui.Spacer(c).Grow(1)
			if r.Reactions != "" {
				ui.Text(c, r.Reactions).FontSize(11).TextColor(t.TextMuted)
			}
			// 回复入口以文字形式放在每条评论的右上角，悬浮整条评论时才
			// 出现，保持默认列表的清爽；已删除 / 评论已关闭的楼层不提供
			// 该入口。点一下就展开并聚焦输入框，点完就能写。
			if !r.Deleted && !closed && hovered {
				a.replyAction(c, "回复", func() {
					a.detail.replyToID = r.ID
					a.detail.replyToFloor = r.Floor
					if a.detail.replyToFloor == 0 {
						a.detail.replyToFloor = r.FlatFloor
					}
					a.detail.composerIdle = false
					a.detail.composerField.Focus(c)
				})
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
		a.commentText(c, content, mergeEmojiMaps(a.emojis, r.Emojis))
		// 评论里的应用内链接是次级操作：仅悬浮整条评论时出现，保持默认
		// 状态的清爽；回复入口已常驻右上角，这里不再重复。其所在行固定
		// 高度占位，出现时同样不会撑高整条评论。
		if !r.Deleted {
			if links := inAppTopicLinks(r.Content); len(links) > 0 {
				ui.Row(c).Height(18).AlignItems(ui.Center).Children(func() {
					if hovered {
						ui.Row(c).Gap(6).Wrap().Children(func() {
							for _, link := range links {
								target := link
								a.replyAction(c, "在应用内打开 "+target.ShortID, func() {
									a.handleLink(target.URL)
								})
							}
						})
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
		Border(0, c.Theme().Border).
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
// button is integrated into the field. 默认只占一行，把空间留给评论列表。
func (a *app) replyComposer(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(6, 10).Gap(4).Children(func() {
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

// replyInput is the Markdown editor for a new comment. It rests one line
// tall, like a quiet channel under the comments, and unfolds into the
// multiline field as soon as it is selected (focused), carries a draft, or
// is answering a floor. Sending folds it back so the list regains the room.
func (a *app) replyInput(c *ui.Context, d *detailState) {
	t := c.Theme()
	focused := d.composerField.Focused(c)
	if !focused {
		d.composerIdle = false // 失焦后再选中，重新展开
	}
	expanded := d.replyToID != "" || strings.TrimSpace(d.draft) != "" ||
		(focused && !d.composerIdle)
	sendDisabled := d.posting || strings.TrimSpace(d.draft) == ""
	vpad := float32(4)
	if expanded {
		vpad = 8
	}
	border := t.Border
	if focused {
		border = t.Accent // 编辑中的输入框给一圈强调色，明确「已选中」
	}
	// 未输入时不占满：折叠态让输入框只裹住内容，聚焦 / 有内容 / 回复某楼才撑满整行。
	box := ui.Column(c).Padding(vpad, 8).Radius(10).
		Background(t.Background).Border(1, border)
	if expanded {
		box = box.FillWidth()
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.End).Children(func() {
			// 多行文本区即 Markdown 编辑器：透明背景、去边框，融入外层输入框。
			// 未输入时不 Grow，只留约 200 宽的输入框；展开时才撑满整行。
			area := ui.TextArea(c, &d.draft).Label("写评论").
				MinHeight(0).Background(ui.Transparent).
				Border(0, t.Border).Bind(&d.composerField)
			if expanded {
				area.Grow(1).Lines(4, 10).
					Placeholder("说点什么…（支持 Markdown 与表情短码 :smile:）")
			} else {
				area.Lines(1, 8).Placeholder("写评论…")
			}
			if d.posting {
				ui.Spinner(c).Size(16, 16)
			}
			// 未登录时把框内动作换成「登录后评论」；否则为集成在框内的发送按钮。
			if !a.auth.loggedIn() {
				if ui.Button(c, "登录后评论").Clicked() {
					a.startLogin()
				}
			} else {
				send := ui.PrimaryButton(c, "发送").Disabled(sendDisabled)
				if send.Clicked() {
					a.submitReply()
				}
			}
			// 评论数（图标 + 数值）与视图切换（图标）贴着发送按钮右侧，同一行展示。
			cnt := ui.Button(c, "").
				Background(ui.Transparent).
				Border(0, t.Border).
				TextColor(t.TextMuted).
				FontSize(12).
				Padding(2, 4).
				Radius(6)
			cnt.Children(func() {
				ui.Icon(c, commentBubbleIcon).Size(16, 16)
				ui.Textf(c, "%d", d.replyTotal).FontSize(12)
			})
			cnt.Label("评论数")
			if cnt.Clicked() {
				d.list.ScrollTo(0, ui.Start)
			}
			// 视图切换：图标表示将要切到的视图，点一下在楼层 / 扁平时间序间切换。
			target := flatViewIcon
			viewLabel := "切到扁平时间序"
			if d.flat {
				target = floorViewIcon
				viewLabel = "切到楼层视图"
			}
			tg := ui.Button(c, "").
				Background(ui.Transparent).
				Border(0, t.Border).
				TextColor(t.Accent).
				FontSize(12).
				Padding(2, 4).
				Radius(6).
				Disabled(d.replyLoading)
			tg.Children(func() {
				ui.Icon(c, target).Size(16, 16)
			})
			tg.Label(viewLabel)
			if tg.Clicked() {
				d.flat = !d.flat
				d.replyList = nil
				d.replyLoaded = false
				d.replyNoMore = false
				d.replyMoreErr = ""
				a.loadReplies(1)
			}
		})
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
			d.composerIdle = true // 发送后折回一行，把空间还给评论列表
			d.replyList = append([]replyItem{created}, d.replyList...)
			d.replyTotal++
			if a.onReplyPosted != nil {
				a.onReplyPosted(created, nil)
			}
		})
	}()
}
