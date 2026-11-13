package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	// 本地正文缓存：cachedAt 是缓存写入时间；fromCache 表示当前正文来自
	// cache/offline.json；cacheNote 是详情页顶部的一行离线说明。
	cachedAt  time.Time
	fromCache bool
	cacheNote string

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
	// flat 是评论区的视图：false 楼层视图、true 扁平时间序。它是账号级偏好，
	// 切换时按账号落盘（见 reply_view.go 的 saveReplyViewMode），与具体帖子
	// 无关，打开别的帖子也沿用。
	flat bool
	// 评论楼层视图的本地记忆（见 reply_view.go）。viewMem 是这次打开读到的
	// 上一次状态（按帖子记，不含视图模式）；collapsed 是当前折叠的楼中楼父
	// 评论（运行态，来自 viewMem）；readBase 是「新评论」的基线时间；stayIdx /
	// stayID 是评论区实时停留的行号与评论 ID；display 是剔除折叠隐藏行后的
	// 显示列表，所有滚动定位与列表行数都基于它，避免隐藏行打乱索引；
	// hiddenCount 记录每个折叠父被收起的后代条数，供「展开 N 条回复」使用。
	viewMem     *replyViewMemory
	collapsed   map[string]bool
	readBase    time.Time
	stayIdx     int
	stayID      string
	display     []replyItem
	hiddenCount map[string]int
	list        ui.ListState
	// bodyScroll 是左栏正文的滚动偏移：TrackScroll 记下来，供「返回顶部」
	// 判断正文是不是已经滚离顶部、以及把它直接写回 0 回顶。
	bodyScroll ui.ScrollState

	// 从评论列表点进来时要定位到具体某条评论。目标常常不落在第 1 页
	// （扁平序按 flat_floor 排），所以首屏没找到时自动续拉后面的页，
	// focusTries 限制最多拉多少页，focusMiss 在找完还没有时置位。
	focusPending bool
	focusTries   int
	focusMiss    bool

	// fav 是标题下方作者行收藏按钮的状态；react 是表态条与选择器的状态
	// （见 interact.go）。两者都只在 UI 线程读写，网络结果通过 publish 回来。
	fav   favoriteState
	react reactionState

	// composer: write a comment (optionally as a nested reply to replyToID).
	// 发送失败的原因不落在页面上，用主窗口的 toast 说明（见 submitReply）。
	// mdEditor 与发帖窗口共用选区、工具面板、表情与预览状态
	// （见 editor_core.go）；正文字符串仍是下面的 draft。
	mdEditor
	draft        string
	replyToID    string
	replyToFloor int
	replyToName  string
	posting      bool
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
			// 已定位到的评论用完整永久链接（含页码），站外打开也能落到那层楼；
			// 还没加载到它时退回只带 commentId 的地址。
			if at := replyIndexByID(d.replyList, d.focusID); at >= 0 {
				return replyPermalink(d.topic, d.fallbackURL, d.replyList[at])
			}
			return d.topic.URL + "?commentId=" + url.QueryEscape(d.focusID)
		}
		return d.topic.URL
	}
	return d.fallbackURL
}

// commentFontSize 是评论正文的字号：比帖子正文小一号，评论区读起来更紧凑。
const commentFontSize float32 = 13

// replyLocateMaxPages 是从评论列表点进来、首屏没找到目标评论时最多自动
// 续拉的页数：30 条一页，20 页足够覆盖绝大多数帖子，又不会无限请求。
const replyLocateMaxPages = 20

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

// loadImage backs markdown images (including the :code: emoji map) with the
// global image cache: memory first, then the disk cache, then one http(s)-only
// download. Images here are post content, so they take the temporary cache
// that expires after imageCacheTTL.
func (a *app) loadImage(raw string) (ui.ImageSource, error) {
	return a.loadImageNow(raw, imageCacheTemporary)
}

func (a *app) fetchImage(raw string) ([]byte, error) {
	if _, err := parseImageURL(raw); err != nil {
		return nil, err
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

// ensureAvatar 预取当前登录账号的头像。userTag 在标题栏和登录页读同一份
// 头像缓存；图片没到之前先画昵称首字母。
func (a *app) ensureAvatar() {
	a.prefetchAvatars(a.auth.avatarURL())
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
		mdEditor:     newMDEditor(),
		shortID:      it.ShortID,
		focusID:      it.CommentID,
		focusPending: it.CommentID != "",
		fallbackURL:  it.URL,
		replyPage:    1,
		flat:         it.CommentID != "", // 从评论列表进来用扁平序，更容易找到那层楼
	}
	// 先摆上缓存里的正文和第一页评论：网络慢或断开时第一帧就有内容。
	a.preloadDetailCache(a.detail)
	// 加载评论楼层视图的本地记忆：折叠、停留位置与新评论基线，都按帖子记。
	// 从评论列表点进来要定位某条评论时不恢复折叠（展开全部便于定位）。
	if mem, ok := a.loadReplyView(it.ShortID); ok {
		a.detail.viewMem = &mem
		a.detail.readBase = mem.ReadAt
		a.detail.stayIdx = mem.ScrollIdx
		a.detail.stayID = mem.ReadToID
		if it.CommentID == "" && len(mem.Collapsed) > 0 {
			a.detail.collapsed = make(map[string]bool, len(mem.Collapsed))
			for _, id := range mem.Collapsed {
				a.detail.collapsed[id] = true
			}
		}
	}
	// 视图模式是账号级偏好，不跟帖子走：上次切到扁平，打开别的帖子也还是
	// 扁平（见 reply_view.go 的 loadReplyViewMode）。从评论列表点进来要定位
	// 某层楼时仍强制扁平，但不改动这个偏好。
	if it.CommentID == "" {
		if flat, ok := a.loadReplyViewMode(); ok {
			a.detail.flat = flat
		}
	}
	// 本地已读：点开即算读过，回到列表时这一行标题置灰（见 post_read.go）。
	// 正文没加载出来也算读过，用户已经打开过它了。
	a.markPostRead(it.ShortID)
	// 阅读记录用缓存里的标题补全打开时缺的字段。
	a.recordReading(it, a.detail.topic)
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
	// 离开评论区前把浏览状态（视图、停留位置、折叠、新评论基线）落盘，下次
	// 回到同一篇时沿用。inline 跳转（压栈）和真正回到列表都会走到这里。
	if a.detail != nil {
		a.saveReplyView(a.detail)
	}
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
				a.handleAuthError(err, func() {
					if a.detail == d {
						a.loadTopic()
					}
				})
				// 已有缓存正文时不要把整页换成错误：继续显示缓存，只在顶部
				// 说明这一份是本地内容。
				if d.topic != nil {
					a.markDetailOffline(d)
					return
				}
				d.topicErr = friendlyError(err)
				return
			}
			d.topic = &topic
			d.fromCache = false
			d.cacheNote = ""
			a.prefetchAvatars(topic.Avatar)
			// 收藏状态随详情下发（带上令牌才有意义）；交给作者行的收藏按钮。
			syncFavorite(d, topic, a.auth.username())
			// 正文拿到后写一份本机缓存，并补全阅读记录里打开时还缺的字段。
			a.updateReadingTopic(d.shortID, &topic)
			a.saveCachedDetail(d)
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
				// 缺权限时不只把错误画在页面上：全局授权弹窗会带用户去补权限，
				// 回调成功后重发这一页。
				a.handleAuthError(err, func() {
					if a.detail == d {
						a.loadReplies(page)
					}
				})
				if page > 1 && len(d.replyList) > 0 {
					// 已经有评论时续拉失败：保留列表，在底部提示并可重试。
					d.replyMoreErr = friendlyError(err)
				} else if page == 1 && len(d.replyList) > 0 {
					// 第一页失败但缓存里已经有评论：继续显示缓存评论，顶部
					// 说明当前是本地内容，不把评论区换成错误页。
					a.markDetailOffline(d)
				} else {
					d.replyErr = friendlyError(err)
				}
				return
			}
			d.replyMoreErr = ""
			rows := flattenReplies(res)
			if page == 1 {
				d.replyList = rows
				d.rebuildDisplay()
				d.replyNoMore = len(rows) == 0
				// 恢复本地记忆的停留位置：没有定位目标时滚回上次读到的行；
				// 有定位目标（从评论列表点进来）时交给下面的 focus 逻辑，
				// 此时不恢复折叠、按扁平序展开全部，便于定位。
				if d.focusID != "" {
					d.list.ScrollTo(0, ui.Start)
				} else if d.viewMem != nil && d.stayIdx > 0 && d.stayIdx < len(d.display) {
					d.list.ScrollTo(d.stayIdx, ui.Start)
				} else {
					d.list.ScrollTo(0, ui.Start)
				}
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
				d.rebuildDisplay()
			}
			d.replyTotal = res.Total
			d.replyTotalPages = res.TotalPages
			d.replyPage = page
			d.replyLoaded = true
			// 评论作者头像随评论一起预取，列表行第一帧就能画（没到则先画
			// 昵称首字母）。
			a.prefetchReplyAvatars(rows)
			// 定位目标评论：找到就滚过去。目标不在首屏时继续自动续拉，
			// 直到找到、到底或用完预算；用户手动续拉时 focusPending 已
			// 经清掉，不会把用户刚滚到的位置拽走。
			at := -1
			if d.focusID != "" {
				at = replyIndexByID(d.display, d.focusID)
			}
			if at >= 0 {
				d.list.ScrollTo(at, ui.Start)
				d.focusPending = false
				d.focusMiss = false
			} else if d.focusPending {
				hasMore := d.replyTotalPages == 0 || page < d.replyTotalPages
				if d.focusTries < replyLocateMaxPages && hasMore && !d.replyNoMore {
					d.focusTries++
					a.loadReplies(page + 1)
				} else {
					d.focusPending = false
					d.focusMiss = true
				}
			}
			if page == 1 {
				// 只缓存第一页评论；续拉的页不写回，避免缓存无限增长。
				a.saveCachedDetail(d)
			}
		})
	}()
}

// refreshDetail 是详情页「刷新」：重新拉当前帖子的正文与第一页评论。
// 旧内容在请求回来之前保持显示（loadTopic / loadReplies 只在成功时替换），
// 所以在长帖里点刷新不会先把正文闪成空白。评论回到第 1 页，续拉从那里
// 重新开始；失败仍走离线那条路（正文来自本地缓存时在顶部说明）。
func (a *app) refreshDetail() {
	d := a.detail
	if d == nil || d.topicLoading || d.replyLoading {
		return
	}
	d.topicErr = ""
	d.replyErr = ""
	d.replyMoreErr = ""
	d.replyNoMore = false
	a.loadTopic()
	a.loadReplies(1)
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
			if err != nil {
				// 失败不锁死：启动时的预取可能碰上网络还没好，下次打开详情
				// 会再试一次，正文不必在整个运行期都显示 :code: 短码。
				a.emojiTried = false
				// 表情表也可能因权限被拒；授权成功后重试一次。
				a.handleAuthError(err, func() {
					a.ensureEmojis()
				})
				a.status = "表情表加载失败，正文短码原样显示"
				return
			}
			a.emojiTried = true
			a.emojis = m
		})
	}()
}

func (a *app) detailView(c *ui.Context) {
	// 主题已由 view 按外观模式设好，这里直接取用，避免重复解析。
	t := c.Theme()
	// Esc 与右上角关闭同义：内联浏览时先退一层，再回到列表。
	c.OnShortcut(0, ui.KeyEscape, func() { a.closeDetail() })
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
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Padding(12, 14).Gap(12).Children(func() {
			// 双栏各自成卡片：正文占约 55%，评论占约 45%；MinWidth(0) 防止
			// 长代码、长链接或嵌套评论把某一栏撑宽，中间用 12px 的空隙 +
			// 圆角描边形成清楚的边界。
			ui.Column(c).Grow(55).MinWidth(0).FillHeight().MinHeight(0).
				Radius(12).Background(t.Background).Border(1, t.Border).Children(func() {
				// 正文可滚动（Grow(1) 吃满左栏剩余高度，MinHeight(0) 允许
				// 它收缩到比正文内容更矮，正文在框内滚动）；作者信息收在
				// 标题下方，底部固定的是表态 / 打赏互动栏。
				ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(18, 24).Gap(12).
					TrackScroll(&d.bodyScroll).Children(func() {
					a.topicBody(c, d)
				})
				a.topicInteractionBar(c, d)
			})
			// 评论卡片用比页面底色略深一点的中性色：浅色下是极浅的灰、
			// 深色下比页面稍亮，两种外观都能把评论区从正文里分出来，又
			// 不抢正文的注意力。
			ui.Column(c).Grow(45).MinWidth(0).FillHeight().MinHeight(0).
				Radius(12).Background(detailReplySurface(t)).Border(1, t.Border).Children(func() {
				a.replyPane(c, d)
			})
		})
		// 数据来源声明 + 可点击的原文链接占满整个窗口底部，放在最底部。
		// 进入详情就画出来：正文加载完成后高度不变，卡片区域不会跳。
		a.topicSourceFooter(c, d)
		// 操作按钮不再占一行页头，而是悬浮在内容右上角：不占正文高度，
		// 也不会随正文滚走（见 detailToolBar）。
		a.detailToolBar(c, d)
	})
	// 表态、打赏与明细都是贴着各自入口的气泡（见 interact.go），在互动区
	// 构建时已经就地挂好，这里不需要再叠一层弹窗。
}

// detailReplySurface 是评论区卡片的底色：页面底色与文字色按很小比例混合，
// 得到浅色下的浅灰、深色下的深灰。比单独写死两个十六进制值更能跟随主题，
// 也不会和正文卡片（页面底色 + 描边）混成一片。
func detailReplySurface(t *ui.Theme) ui.Color {
	return t.Background.Mix(t.Text, 0.05)
}

// detailToolBar 是详情页右上角的悬浮工具条：返回顶部、刷新与关闭。收藏
// 已经移到标题下方的作者行最右侧（见 favoriteButton）。工具条绝对定位在
// 内容区右上角，不参与页面布局，所以正文和评论能多吃到原来被页头占掉的
// 高度；按钮本体仍是 30×30 的图标块，整条加一层描边底色，压在评论卡片
// 头部右侧的留白上也能看清。评论卡片的头部行不往右挤占这块空间
// （见 replyHeader）。
func (a *app) detailToolBar(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Row(c).Absolute().Top(15).Right(22).Gap(2).Padding(3).Radius(10).
		Background(t.Surface).Border(1, t.Border).
		Shadow(0, 3, 12, 0, ui.RGBA(0, 0, 0, 0.16)).
		Children(func() {
			// 只有正文或评论滚离顶部时才出现：工具条始终可达，不会像旧版
			// 页头那样为了放它固定占掉一行。
			if first, _ := d.list.Visible(); d.bodyScroll.Y > 0 || first > 0 {
				a.detailToolButton(c, backTopIcon, "返回顶部", "正文与评论都回到顶部", func() {
					// 直接把偏移写回 ScrollState 就是立即回顶（ScrollTo 会走一段
					// 动画，回顶部不必拖时间）；评论列表按行号同样立即回顶。
					d.bodyScroll.X, d.bodyScroll.Y = 0, 0
					d.list.ScrollTo(0, ui.Start)
				})
			}
			a.detailRefreshButton(c, d)
			a.detailToolButton(c, closeIcon, "关闭详情", "关闭详情（Esc）", a.closeDetail)
		})
}

// detailToolButton 是悬浮工具条里的纯图标按钮：无边框、无填充，悬停才出现
// 浅底，与列表页的 FAB 区分开。label 供读屏与测试识别，tip 是悬停说明。
func (a *app) detailToolButton(c *ui.Context, icon *ui.SVG, label, tip string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Size(30, 30).Radius(8).Center().Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	if hovered {
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(ink)
	})
	b.Label(label).Tooltip(tip).OnClick(onClick)
}

// detailRefreshButton 是悬浮工具条里的刷新按钮：请求在途时换成转圈并禁用，
// 避免连点重复发请求。它刷新的是整页（正文 + 第一页评论）。
func (a *app) detailRefreshButton(c *ui.Context, d *detailState) {
	t := c.Theme()
	loading := d.topicLoading || d.replyLoading
	b := ui.Box(c).Size(30, 30).Radius(8).Center().Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered && !loading {
		b.Background(t.SurfaceHover)
	}
	if loading {
		b.Disabled(true)
	}
	b.Children(func() {
		if loading {
			ui.Spinner(c).Size(14, 14)
			return
		}
		ink := t.TextMuted
		if hovered {
			ink = t.Text
		}
		ui.Icon(c, refreshIcon).Size(17, 17).TextColor(ink)
	})
	b.Label("刷新")
	tip := "重新加载正文与评论"
	if loading {
		tip = "正在刷新…"
	}
	b.Tooltip(tip)
	if !loading {
		b.OnClick(a.refreshDetail)
	}
}

func (a *app) topicBody(c *ui.Context, d *detailState) {
	t := c.Theme()
	// 评论定位、离线缓存与内联浏览层级原来占着页头的一整行；现在收进
	// 正文顶部，只在有话说时才占一点点高度。
	a.topicNotice(c, d)
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
		// 正文还没到时仍以帖子编号占位：内联跳转后能看出当前是哪一帖。
		ui.Text(c, "帖子 "+d.shortID).FontSize(13).Bold().SingleLine()
		ui.Text(c, "暂无正文").TextColor(t.TextMuted)
		return
	}
	// 标题在正文最上面：24px 加粗的标题与下面的作者行、副标题、正文拉开
	// 字号差，读者第一眼就能抓住主题。
	if title := strings.TrimSpace(p.Title); title != "" {
		ui.Text(c, title).FontSize(24).Bold().MaxLines(3)
	}
	// 作者行放在标题下面：头像缩到与正文同级的 20px，昵称、等级、徽章
	// 跟在旁边，收藏按钮贴在这一行的最右侧。它不再单独占一条页头，随正文
	// 一起滚动。
	a.topicAuthorLine(c, d)
	// 再下面是两行副标题：分类（两级）· 浏览数 · 置顶 / 评论已关闭，
	// 以及标签。作者信息已经在标题下方，这里不再重复。
	a.topicSubtitle(c, p)
	// 副标题与正文之间不再画分隔线：这条线在正文顶端显得像多余的一条。
	// 帖子自己的 emojis 可能包含社区自定义图标；并到全局表情表后一起渲染。
	emojis := mergeEmojiMaps(a.emojis, p.Emojis)
	if strings.TrimSpace(p.Content) == "" {
		ui.Text(c, "（正文为空）").TextColor(t.TextMuted)
	} else {
		a.markdownView(c, replaceEmoji(p.Content, emojis, true), a.md)
	}
	for i, ps := range p.Postscripts {
		ui.Divider(c)
		label := fmt.Sprintf("附言 %d", i+1)
		if ps.CreatedAt != "" {
			label += " · " + relativeTime(ps.CreatedAt)
		}
		ui.Text(c, label).FontSize(13).Bold()
		postEmojis := mergeEmojiMaps(a.emojis, p.Emojis, ps.Emojis)
		a.markdownView(c, replaceEmoji(ps.Content, postEmojis, true), a.md)
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
	// 正文末尾不再画互动行：表态与打赏固定在左栏底部的互动栏里
	// （见 detailView 的 topicInteractionBar），正文再长也始终可达。
}

// topicNotice 是正文顶部的一条状态说明：评论定位、离线缓存与内联浏览层级。
// 这些提示原先各占页头的一部分，压得页头很高；收到正文顶部后，没有提示时
// 连一行都不占。多条同时出现时在同一行内换行。
func (a *app) topicNotice(c *ui.Context, d *detailState) {
	t := c.Theme()
	type notice struct {
		text  string
		color ui.Color
	}
	var notes []notice
	if d.focusID != "" {
		text, color := "来自评论定位", t.TextMuted
		if d.focusMiss {
			text, color = "未找到该评论", t.Danger
		}
		notes = append(notes, notice{text, color})
	}
	// 离线说明：正文来自本地缓存时，明确告诉用户这是哪个时间的内容，
	// 而不是让旧内容看起来像刚刷新过。
	if d.cacheNote != "" {
		notes = append(notes, notice{d.cacheNote, t.TextMuted})
	}
	if n := len(a.detailStack); n > 0 {
		notes = append(notes, notice{fmt.Sprintf("内联浏览 %d 层", n), t.TextMuted})
	}
	if len(notes) == 0 {
		return
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		for _, nt := range notes {
			ui.Text(c, nt.text).FontSize(11).TextColor(nt.color).SingleLine()
		}
	})
}

// topicAuthorLine 是标题下方的作者信息：左边是 20px 小头像 + 昵称 + 等级 +
// 徽章，右边是收藏按钮。它跟着正文一起滚动，不再固定占一条页头；头像仍可
// 点开作者资料卡。关注按钮已经从详情页移除，关注入口只在用户资料卡里。
func (a *app) topicAuthorLine(c *ui.Context, d *detailState) {
	p := d.topic
	if p == nil {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		// 作者身份在左侧自己的区域里换行，窄窗口也不会把收藏挤出这一行。
		ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
			if strings.TrimSpace(p.Author) != "" {
				a.userTag(c, userTagSpec{
					Name: p.Author, Avatar: p.Avatar, Size: 13, Bold: true,
					Badges: p.Badges,
				})
			}
			if p.AuthorLevel > 0 {
				ui.Textf(c, "Lv.%d", p.AuthorLevel).FontSize(10).TextColor(t.TextMuted)
			}
		})
		// 收藏是帖子级的动作，贴在这条元信息行的最右侧。
		a.favoriteButton(c, d)
	})
}

// topicSubtitle 是标题下面的两行副标题：第一行是两级分类、浏览数与帖子状态
// （置顶 / 评论已关闭），第二行是标签。分类和标签原来贴在页头作者旁边，收回
// 到标题下方后，标题区一眼能读出「这帖发在哪、有多少人看过、打了什么标签」。
func (a *app) topicSubtitle(c *ui.Context, p *topicDetail) {
	t := c.Theme()
	path := p.categoryPath()
	if path == "" && p.Views <= 0 && !p.IsPinned && !p.CommentsClosed &&
		len(p.Tags) == 0 {
		return
	}
	// 两行紧挨着成为一块副标题：行与行之间只留 3px，和标题、正文之间才是
	// Scroll 的 12px 间距。
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		if path != "" || p.Views > 0 || p.IsPinned || p.CommentsClosed {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				if p.IsPinned {
					ui.Text(c, "置顶").FontSize(11).TextColor(t.Accent)
				}
				if path != "" {
					ui.Text(c, path).FontSize(12).TextColor(t.TextMuted)
				}
				if p.Views > 0 {
					ui.Textf(c, "%d 次浏览", p.Views).FontSize(11).TextColor(t.TextMuted)
				}
				if p.CommentsClosed {
					ui.Text(c, "评论已关闭").FontSize(11).TextColor(t.TextMuted)
				}
			})
		}
		if len(p.Tags) > 0 {
			// 标签是副标题的第二行：小一号、弱化色，不和正文抢注意力。
			ui.Text(c, "#"+strings.Join(p.Tags, "  #")).FontSize(11).
				TextColor(t.TextMuted).SingleLine()
		}
	})
}

// topicSourceFooter 占满整个窗口底部，放在最底部：数据来源声明 + 可点击原文
// 链接（点一下用系统浏览器打开）。这一行从进入详情起就画出来，正文是否加载
// 完成都不改变它占的高度，正文出现时两栏卡片不会先高后矮地跳一下；原文地址
// 用打开这一帖时带进来的链接兜底，正文到了再换成规范的永久链接。
func (a *app) topicSourceFooter(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Divider(c)
	ui.Row(c).Padding(6, 14).Gap(4).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(c, "内容来自 2Libra 用户，版权归原作者所有。").FontSize(10).TextColor(t.TextMuted)
		origin := d.originURL()
		if origin == "" {
			return
		}
		ui.Text(c, "原文链接：").FontSize(10).TextColor(t.TextMuted)
		link := ui.Button(c, truncate(origin, 60)).
			Background(ui.Transparent).Border(0, t.Border).
			TextColor(t.Accent).FontSize(10).Padding(0, 2)
		if link.Clicked() {
			a.openExternal(origin)
		}
	})
}

func (a *app) replyPane(c *ui.Context, d *detailState) {
	t := c.Theme()
	// 评论区自己有一行标题与计数 / 视图切换，输入框只负责编写与发送：
	// 原来挤在输入框右侧的评论数、视图切换移到这里，输入栏因此干净得多。
	a.replyHeader(c, d)
	ui.Divider(c)
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
		// display 是剔除折叠隐藏行后的显示列表；rebuildDisplay 在折叠为空时
		// 直接复用 replyList，索引与现有行为一致。所有滚动定位、列表行数都
		// 基于它。
		d.rebuildDisplay()
		rows := d.display
		d.list.Key = func(i int) any {
			if i >= 0 && i < len(rows) && rows[i].ID != "" {
				return rows[i].ID
			}
			return i
		}
		// 实时记录停留位置：视口顶部的行号与评论 ID，供离开时落盘恢复。
		if first, _ := d.list.Visible(); first >= 0 && first < len(rows) {
			d.stayIdx = first
			if rows[first].ID != "" {
				d.stayID = rows[first].ID
			}
		}
		// MyGo 的 ui.List 是虚拟化滚动区：只构建可视行，长帖也不会一次
		// 排完所有评论；Grow(1) + MinHeight(0) 让它吃满卡片中间，输入栏
		// 始终固定在卡片底部。
		ui.List(c, &d.list, len(rows), func(i int) {
			a.replyRow(c, rows[i], d.focusID, isNewReply(rows[i].CreatedAt, d.readBase))
		}).Grow(1).MinHeight(0).Padding(4, 10).Gap(10)
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

// replyHeader 是评论卡片顶部固定的一行：「评论」标题、评论数（点一下回到
// 列表顶部）与楼层 / 扁平视图切换。它不参与滚动，也不和输入框抢位置。
func (a *app) replyHeader(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(10, 14, 8, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "评论").FontSize(14).Bold()
		cnt := ui.Button(c, "").
			Background(ui.Transparent).
			Border(0, t.Border).
			TextColor(t.TextMuted).
			FontSize(12).
			Padding(2, 4).
			Radius(6)
		cnt.Children(func() {
			ui.Icon(c, commentBubbleIcon).Size(14, 14).TextColor(t.TextMuted)
			ui.Textf(c, "%d", d.replyTotal).FontSize(11).TextColor(t.TextMuted)
		})
		cnt.Label("评论数")
		cnt.Tooltip("回到评论顶部")
		if cnt.Clicked() {
			d.list.ScrollTo(0, ui.Start)
		}
		// 视图切换紧跟在评论数后面，不再用 Spacer 推到最右：右上角留给
		// 详情页的悬浮工具条（见 detailToolBar），两边不会叠在一起。
		// 图标表示将要切到的视图，点一下在楼层 / 扁平时间序间切换。
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
			ui.Icon(c, target).Size(16, 16).TextColor(t.Accent)
		})
		tg.Label(viewLabel)
		tg.Tooltip(viewLabel)
		if tg.Clicked() {
			d.flat = !d.flat
			d.replyList = nil
			d.replyLoaded = false
			d.replyNoMore = false
			d.replyMoreErr = ""
			// 换了视图重新找一次被定位的评论：楼层/扁平的分页位置不同。
			d.focusPending = d.focusID != ""
			d.focusTries = 0
			d.focusMiss = false
			a.loadReplies(1)
			// 视图偏好按账号立即落盘：换一篇帖子打开也沿用（见 reply_view.go）。
			a.saveReplyViewMode(d.flat)
		}
	})
}

// replyCanLoadMore 汇报是否还能（且应）续拉下一页评论：已加载、未在读、未到底、
// 且仍有下一页。totalPages 缺失（为 0）时按空页去重兜底，继续尝试续拉。
func replyCanLoadMore(d *detailState) bool {
	if !d.replyLoaded || d.replyLoading || d.replyNoMore {
		return false
	}
	return d.replyTotalPages == 0 || d.replyPage < d.replyTotalPages
}

func (a *app) replyRow(c *ui.Context, r replyItem, focusID string, isNew bool) {
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
		// 头部行只放身份信息，固定 18 高：作者、徽章与时间靠左，楼层号吸在
		// 右端。回复 / 表态 / 打赏都在下面的固定底行里。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Height(18).Children(func() {
			// 带楼中楼的评论可以折叠 / 展开：收起后只留这条本身，子回复
			// 藏起来（见 rebuildDisplay）。折叠入口只在有子树时出现。
			if r.HasChildren && a.detail != nil {
				collapsed := a.detail.collapsed[r.ID]
				fold := ui.Button(c, "").
					Background(ui.Transparent).Border(0, t.Border).
					TextColor(t.TextMuted).FontSize(10).Padding(0, 0).Radius(4)
				if collapsed {
					fold.Children(func() {
						ui.Icon(c, chevronRightIcon).Size(12, 12).TextColor(t.TextMuted)
					})
					fold.Tooltip("展开楼中楼")
				} else {
					fold.Children(func() {
						ui.Icon(c, chevronDownIcon).Size(12, 12).TextColor(t.TextMuted)
					})
					fold.Tooltip("折叠楼中楼")
				}
				if fold.Clicked() {
					if a.detail.collapsed == nil {
						a.detail.collapsed = make(map[string]bool)
					}
					a.detail.collapsed[r.ID] = !collapsed
					a.detail.rebuildDisplay()
					if at := replyIndexByID(a.detail.display, r.ID); at >= 0 {
						a.detail.list.ScrollTo(at, ui.Start)
					}
					a.saveReplyView(a.detail)
				}
			}
			// 新评论标记：上次离开之后新增的回复，头部最左给一个 accent 小标签。
			if isNew {
				ui.Text(c, "新").FontSize(9).Bold().
					TextColor(t.AccentText).Background(t.Accent).
					Padding(3, 4).Radius(3)
			}
			// 评论作者统一由 userTag 渲染：头像跟着 12px 的昵称大小走，
			// 头部行固定 18 高时头像恰好与昵称齐平。
			a.userTag(c, userTagSpec{
				Name:   r.Author,
				Avatar: r.Avatar,
				Size:   12,
				Bold:   true,
				Badges: r.Badges,
			})
			if r.CreatedAt != "" {
				ui.Text(c, relativeTime(r.CreatedAt)).FontSize(11).TextColor(t.TextMuted)
			}
			// 楼层号放在这一行的最右侧：它是每层楼的标识，靠边排列不会把
			// 作者与时间挤散，长昵称也不会和它抢位置。只有顶层评论才有
			// floor；楼中楼服务端给的是 null，不能拿 flat_floor 当楼层号，
			// 否则回复别人的评论也会被算成一楼。
			ui.Spacer(c).Grow(1)
			floor := ""
			switch {
			case r.Pinned:
				floor = "置顶"
			case r.Floor > 0:
				floor = fmt.Sprintf("%d 楼", r.Floor)
			}
			if floor != "" {
				// 楼层号也是评论的永久链接：点一下用系统浏览器打开站点上的
				// 这条评论，复制分享都从那里走。
				href := ""
				if a.detail != nil {
					href = replyPermalink(a.detail.topic, a.detail.fallbackURL, r)
				}
				if href != "" {
					link := ui.Button(c, floor).Background(ui.Transparent).
						Border(0, t.Border).TextColor(t.TextMuted).
						FontSize(10).Padding(0, 0).Radius(4)
					if link.Hovered() {
						link.TextColor(t.Accent)
					}
					link.Tooltip("在浏览器打开这条评论")
					if link.Clicked() {
						a.openExternal(href)
					}
				} else {
					ui.Text(c, floor).FontSize(10).TextColor(t.TextMuted)
				}
			}
			// 回复、表态与打赏都收到评论底部那一行（见下面的 replyFooter），
			// 头部只留身份信息，不再随悬浮长出按钮。
		})
		if label := replyParentLabel(r); label != "" {
			at := -1
			if a.detail != nil {
				at = replyIndexByID(a.detail.display, r.ParentID)
			}
			if at >= 0 {
				// 父评论还在已加载的列表里：点一下跳过去并高亮它。
				a.parentJump(c, label, r.ParentID, at)
			} else {
				ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}
		}
		content := r.Content
		if r.Deleted {
			content = "该评论已删除"
		}
		if strings.TrimSpace(content) == "" {
			content = "（无内容）"
		}
		a.commentText(c, content, mergeEmojiMaps(a.emojis, r.Emojis))
		// 评论底部固定一行：左边是这条评论拿到的表态 / 打赏汇总，右边是
		// 「回复」与表态、打赏两个图标入口。这一行始终占位，悬浮或出现气泡
		// 都不会改变评论高度。
		if !r.Deleted {
			a.replyFooter(c, r, closed)
		}
		// 被折叠的评论在底部补一行「展开 N 条回复」，点一下就展开楼中楼。
		if r.HasChildren && a.detail != nil && a.detail.collapsed[r.ID] {
			n := a.detail.hiddenCount[r.ID]
			expand := ui.Button(c, fmt.Sprintf("展开 %d 条回复", n)).
				Background(ui.Transparent).Border(0, t.Border).
				TextColor(t.Accent).FontSize(11).Padding(2, 4).Radius(4)
			expand.Tooltip("展开楼中楼")
			if expand.Clicked() {
				if a.detail.collapsed == nil {
					a.detail.collapsed = make(map[string]bool)
				}
				a.detail.collapsed[r.ID] = false
				a.detail.rebuildDisplay()
				if at := replyIndexByID(a.detail.display, r.ID); at >= 0 {
					a.detail.list.ScrollTo(at, ui.Start)
				}
				a.saveReplyView(a.detail)
			}
		}
		// 评论里的应用内链接是次级操作：仅悬浮整条评论时出现，保持默认
		// 状态的清爽；其所在行固定高度占位，出现时同样不会撑高整条评论。
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

// replyFooter 是评论底部固定的一行：最左边是表态（笑脸）入口，随后是这条
// 评论拿到的表态 / 打赏汇总；最右边是打赏（金币）入口与回复图标。整行始终
// 渲染，悬浮、打开气泡都不会让评论高度变化。
func (a *app) replyFooter(c *ui.Context, r replyItem, closed bool) {
	if a.detail == nil {
		return
	}
	d := a.detail
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if a.canActOn(d, r.ID) {
			a.reactionEntry(c, d, r.ID)
		}
		a.reactionSummaryInfo(c, d, r.ID, r.Rewards)
		ui.Spacer(c).Grow(1)
		if a.canRewardOn(d, r.ID) {
			a.rewardEntry(c, d, r.ID)
		}
		if !closed {
			a.replyEntry(c, d, r)
		}
	})
}

// replyEntry 是回复入口：和表态 / 打赏一样是一枚图标，放在评论底行最右端。
func (a *app) replyEntry(c *ui.Context, d *detailState, r replyItem) {
	t := c.Theme()
	b := ui.Box(c).Size(18, 18).Radius(5).Center().Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	if hovered {
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, replyIcon).Size(13, 13).TextColor(ink)
	})
	b.Label("回复").Tooltip("回复这条评论")
	if b.Clicked() {
		d.replyToID = r.ID
		d.replyToFloor = r.Floor
		d.replyToName = r.Author
		d.composerIdle = false
		d.composerField.Focus(c)
	}
}

// replyParentLabel 是楼中楼上方那条父评论引用。父评论有楼层号时用「回复 N
// 楼」，父评论本身也是楼中楼（没有 floor）时退回作者名，避免出现「回复 0
// 楼」这种并不存在的楼层。
func replyParentLabel(r replyItem) string {
	if r.ParentFloor > 0 {
		if r.ParentSummary != "" {
			return fmt.Sprintf("回复 %d 楼：%s", r.ParentFloor, r.ParentSummary)
		}
		return fmt.Sprintf("回复 %d 楼", r.ParentFloor)
	}
	who := r.ParentAuthor
	if who != "" {
		if r.ParentSummary != "" {
			return "回复 " + who + "：" + r.ParentSummary
		}
		return "回复 " + who
	}
	if r.ParentSummary != "" {
		return "回复：" + r.ParentSummary
	}
	return ""
}

// parentJump 是「回复 N 楼：…」的可点击版本：父评论已经在列表里时，
// 点一下滚到父评论并把高亮移到它身上，方便顺着对话往上读。
func (a *app) parentJump(c *ui.Context, label, parentID string, at int) {
	t := c.Theme()
	// ButtonBase 默认把子元素居中；这条引用是正文的一部分，必须和下面的
	// 评论正文左对齐。光靠 Column 的拉伸只会让按钮变宽，里面的文字仍然
	// 居中，所以显式从行首开始排。
	b := ui.ButtonBase(c).Padding(0, 0).Background(ui.Transparent).Justify(ui.Start)
	color := t.TextMuted
	if b.Hovered() {
		color = t.Accent
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(color).MaxLines(2)
	})
	b.Tooltip("跳到被回复的评论")
	if b.Clicked() && a.detail != nil {
		a.detail.focusID = parentID
		a.detail.focusPending = false
		a.detail.focusMiss = false
		a.detail.list.ScrollTo(at, ui.Start)
	}
}

// reactionBadgeTip 是表情标签的悬停说明：人数 + 我是否也选过。
func reactionBadgeTip(r reactionStat) string {
	tip := r.Emoji
	if r.Count > 0 {
		tip = fmt.Sprintf("%s 有 %d 人表态", r.Emoji, r.Count)
	}
	if r.Mine {
		tip += "，你已表态"
	}
	return tip
}

// rewardBadgeTip 是打赏标签的悬停说明：总额、笔数与打赏者。
func rewardBadgeTip(r rewardStat) string {
	tip := fmt.Sprintf("累计打赏 %d 金币", r.Total)
	if r.Count > 1 {
		tip = fmt.Sprintf("%d 笔打赏，共 %d 金币", r.Count, r.Total)
	}
	if len(r.Users) > 0 {
		tip += "：" + strings.Join(r.Users, "、")
	}
	return tip
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
	// 输入栏固定在评论卡片底部，不再为右下角的悬浮按钮让出宽度：刷新与
	// 返回顶部已经收进右上角的悬浮工具条，这里只需要普通的内边距。
	ui.Column(c).FillWidth().Padding(8, 12).Gap(4).Children(func() {
		if d.replyToID != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// 回复顶层评论时报楼层；回复楼中楼时它没有楼层，改用作者名。
				who := d.replyToName
				if d.replyToFloor > 0 {
					who = fmt.Sprintf("%d 楼", d.replyToFloor)
				}
				if who == "" {
					who = "评论"
				}
				ui.Text(c, "正在回复 "+who).FontSize(12).TextColor(t.Accent).Grow(1)
				a.replyAction(c, "取消", func() {
					d.replyToID = ""
					d.replyToFloor = 0
					d.replyToName = ""
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
	// 工具条、视图切换与两个面板都是输入框的一部分：点开它们、在面板里搜索
	// 表情，都会让文本框失焦。只看焦点决定展开与否，输入框会在点下按钮的
	// 那一帧缩回一行，刚打开的面板随之消失（评论列表重新占满、页面往下一沉）。
	// 所以面板打开或切到预览时也保持展开，直到用户自己收起。
	expanded := d.replyToID != "" || strings.TrimSpace(d.draft) != "" ||
		(focused && !d.composerIdle) ||
		d.emojiOpen || d.imageOpen || d.preview == 1
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
	box := ui.Column(c).Padding(vpad, 8).Radius(10).Background(t.Background)
	// 整个评论框也是拖放区。登记要在 Children 之前；拖进图片后草稿立刻有
	// 内容，这一帧就该展开，让用户看见刚插进来的占位符。
	over := a.editorDropZone(box, uploadTargetReply)
	expanded = expanded || strings.TrimSpace(d.draft) != ""
	if expanded {
		box = box.FillWidth()
	}
	if over {
		box.Border(1, t.Accent).Background(t.SurfaceHover)
	} else {
		box.Border(1, border)
	}
	box.Children(func() {
		if expanded {
			// 折叠态仍只有一行：工具条、视图切换与两个面板都只在展开时出现。
			// 这几行带 KeepFocus：按下工具条、分隔线与视图切换时焦点仍留在
			// 输入框里，光标与选区不动，插入标记后可以接着打字（见
			// editorToolbar），输入框也不会因为这一下失焦而缩回去。
			ui.Row(c).FillWidth().Padding(2, 4).KeepFocus().Children(func() {
				a.editorToolbar(c, &d.mdEditor, &d.draft, true)
			})
			ui.Divider(c).KeepFocus()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).KeepFocus().
				Label("评论视图").Children(func() {
				// 编辑 / 预览：两个图标按钮代替文字 Segmented，平时只显示图标，
				// 悬浮由 Tooltip 说明，选中项用底色区分；保留「评论视图」无障碍名。
				a.editorIconToggle(c, d.preview == 0, editIcon, "编辑", "编辑模式",
					func() { d.preview = 0 })
				a.editorIconToggle(c, d.preview == 1, eyeIcon, "预览", "预览模式",
					func() { d.preview = 1 })
				ui.Spacer(c).Grow(1)
				a.editorPanelButtons(c, &d.mdEditor, "插入表情", "插入图片", true)
			})
			a.editorPanels(c, uploadTargetReply, &d.mdEditor, &d.draft,
				a.replyCommunityEmojis(d), true)
			if d.preview == 1 {
				a.editorPreviewView(c, d.draft, a.replyEmojis(d),
					a.commentMd, replyPreviewHeight)
			}
		}
		// 展开时上面会多出工具条、视图切换与面板。MyGo 的节点身份由
		// 「父节点 + 子节点序号」推导，若不给这一行固定 key，多出来的兄弟
		// 会把输入框挤到另一个 ID 上，焦点随之丢失（发送后折不回一行）。
		// 输入框本身是复合控件不能带 key，key 给包着它的 Row。
		c.Key("reply-composer-row")
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.End).Children(func() {
			// 多行文本区即 Markdown 编辑器：透明背景、去边框，融入外层输入框。
			// 未输入时不 Grow，只留约 200 宽的输入框；展开时才撑满整行。
			area := ui.TextArea(c, &d.draft).Label("写评论").
				MinHeight(0).Background(ui.Transparent).
				Border(0, t.Border).Bind(&d.composerField)
			if expanded {
				area.Grow(1).Lines(4, 10).
					Placeholder("说点什么…（支持 Markdown、表情短码 :smile:，图片可直接拖进来）")
			} else {
				area.Lines(1, 8).Placeholder("写评论…")
			}
			d.syncSelection(area)
			if d.posting {
				ui.Spinner(c).Size(16, 16)
			}
			// 未登录时把框内动作换成「登录后评论」；否则为集成在框内的发送按钮。
			// 两个动作都只显示图标，文字说明交由 Tooltip（Label 仍保留文字
			// 作无障碍名与点击测试的命中名）。
			if !a.auth.loggedIn() {
				login := ui.Button(c, "").
					Background(ui.Transparent).
					Border(0, t.Border).
					TextColor(t.Text).
					Padding(4, 6).
					Radius(6)
				login.Children(func() {
					ui.Icon(c, loginIcon).Size(16, 16).TextColor(t.Text)
				})
				login.Label("登录后评论").Tooltip("登录后才能发表评论")
				if login.Clicked() {
					var replay func()
					if strings.TrimSpace(d.draft) != "" {
						replay = func() {
							if a.detail == d {
								a.submitReply()
							}
						}
					}
					a.requestLogin("登录后才能发表评论。", replay)
				}
			} else {
				send := ui.PrimaryButton(c, "").Disabled(sendDisabled)
				send.Children(func() {
					ui.Icon(c, sendIcon).Size(16, 16).TextColor(t.AccentText)
				})
				send.Label("发送").Tooltip("发送评论")
				if send.Clicked() {
					a.submitReply()
				}
			}
		})
	})
}

// replyCommunityEmojis 是当前帖子自带的社区表情表；没有正文数据时为空。
func (a *app) replyCommunityEmojis(d *detailState) map[string]string {
	if d == nil || d.topic == nil {
		return nil
	}
	return d.topic.Emojis
}

// replyEmojis 是评论预览用的完整表情表：先全局表，再叠上当前帖子的社区
// 自定义图标（同名短码以帖子为准），和详情页正文渲染走的是同一份合并。
func (a *app) replyEmojis(d *detailState) map[string]emojiInfo {
	return mergeEmojiMaps(a.emojis, a.replyCommunityEmojis(d))
}

// submitReply posts the draft through the Client. On success it clears the
// box, inserts the created comment where the current view expects it and
// scrolls to it, so the author sees it immediately in floor order.
//
// 发送失败（含校验不通过）只在窗口底部弹一条 toast：输入框保持原样、可以
// 直接改，页面上不留一行红字。
func (a *app) submitReply() {
	d := a.detail
	if d == nil || d.posting {
		return
	}
	content := strings.TrimSpace(d.draft)
	if content == "" {
		a.toast(toastMain, "评论内容不能为空")
		return
	}
	if n := editorPendingUploads(d.draft); n > 0 {
		a.toast(toastMain, "还有 "+strconv.Itoa(n)+" 张图片未处理完，请等上传结束或移除后再发送")
		return
	}
	if !a.auth.loggedIn() {
		a.toast(toastMain, "请先登录后再评论")
		return
	}
	if d.topic != nil && d.topic.CommentsClosed {
		a.toast(toastMain, "该帖子已关闭评论")
		return
	}
	parentID := d.replyToID
	parentFloor := d.replyToFloor
	parentName := d.replyToName
	parentSummary := ""
	if at := replyIndexByID(d.display, parentID); at >= 0 {
		parentSummary = truncate(commentExcerpt(d.display[at].Content), 60)
		if parentName == "" {
			parentName = d.replyList[at].Author
		}
	}
	d.posting = true
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
				// 缺 write:comment 时先补授权，回调成功后按同样的内容与父评论
				// 重发一次，用户不用重新输入。
				a.handleAuthError(err, func() {
					if a.detail == d {
						a.submitReply()
					}
				})
				a.toast(toastMain, friendlyError(err))
				if a.onReplyPosted != nil {
					a.onReplyPosted(replyItem{}, err)
				}
				return
			}
			if created.Author == "" || created.Author == "匿名" {
				if u := a.auth.username(); u != "" {
					created.Author = u
					if created.Avatar == "" {
						created.Avatar = a.auth.avatarURL()
					}
				}
			}
			// 只有顶层评论才有楼层号。回复别人的评论不占楼层，不能把
			// flat_floor 提成 floor，否则刚发的楼中楼会显示成「N 楼」。
			if parentID == "" && created.Floor == 0 && created.FlatFloor > 0 {
				created.Floor = created.FlatFloor
			}
			// 服务端没回父评论信息时用提交前的目标补齐，刚发的楼中楼
			// 立刻就能显示父评论引用并点回去；父评论没有楼层时引用作者名。
			if created.ParentID == "" {
				created.ParentID = parentID
			}
			if created.ParentFloor == 0 {
				created.ParentFloor = parentFloor
			}
			if created.ParentAuthor == "" {
				created.ParentAuthor = parentName
			}
			if created.ParentSummary == "" {
				created.ParentSummary = parentSummary
			}
			d.draft = ""
			d.replyToID = ""
			d.replyToFloor = 0
			d.replyToName = ""
			d.composerIdle = true // 发送后折回一行，把空间还给评论列表
			// 工具面板与预览会让输入框保持展开（见 replyInput），发送后一并
			// 收起，否则空输入框还占着面板的高度，折不回一行。
			d.emojiOpen, d.imageOpen, d.preview = false, false, 0
			// 按当前视图放到正确的位置：楼中楼落在父评论的子树后面，
			// 顶层与扁平时间序接在已加载评论之后，而不是插到最顶端。
			// 插到正确位置后再重建显示列表，并在显示空间里定位刚发的评论
			// 滚到视口底部（折叠会把行号打乱，必须用 display 而非 replyList）。
			d.replyList, _ = insertReply(d.replyList, created, parentID, d.flat)
			d.rebuildDisplay()
			if at := replyIndexByID(d.display, created.ID); at >= 0 {
				d.list.ScrollTo(at, ui.End)
			}
			d.replyTotal++
			a.prefetchAvatars(created.Avatar)
			// 评论会扣金币：发出全局金币刷新事件，标题栏与金币窗口一起更新。
			a.coinsChanged(coinEventReply)
			if a.onReplyPosted != nil {
				a.onReplyPosted(created, nil)
			}
		})
	}()
}
