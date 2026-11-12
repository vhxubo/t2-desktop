package main

import (
	"log"
	"net/url"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 全应用统一的用户标识：昵称旁永远画头像，头像边长按昵称字号换算，
// 字号变大时头像同步变大。标题栏、列表行、评论、通知与帖子作者块都走
// userTag，不再各自摆一个固定尺寸的 Avatar。
//
// 头像位图按地址缓存，并在数据到达时预取（见 prefetchAvatars）：渲染时
// 只读缓存，没加载完就先画昵称首字母，避免在每一帧里发起网络请求。

// userAvatarScale 是头像边长相对昵称字号的倍数。昵称的行高大约为字号的
// 1.4–1.5 倍，头像取 1.5 倍时与昵称视觉齐平：12px 昵称配 18px 头像，
// 14px 昵称配 21px 头像，11px 的列表副标题配 16.5px 头像。
const userAvatarScale float32 = 1.5

// userAvatarMinSize 是头像的最小边长：再小的字号也不至于把头像压成看不清
// 的小点。
const userAvatarMinSize float32 = 14

// userAvatarSize 返回昵称字号对应的头像边长。
func userAvatarSize(fontSize float32) float32 {
	if fontSize <= 0 {
		fontSize = 12
	}
	size := fontSize * userAvatarScale
	if size < userAvatarMinSize {
		return userAvatarMinSize
	}
	return size
}

// userTagSpec 描述一处「头像 + 昵称」。Name 是昵称，也是头像占位与无障碍
// 名称的来源；Label 是可选的显示文本（例如「@jimmy」「已登录 · jimmy」），
// 空时直接显示昵称。Size 是昵称字号，头像尺寸由它换算。
type userTagSpec struct {
	Name     string
	Avatar   string
	Label    string
	Size     float32
	Bold     bool
	Color    ui.Color
	MaxLines int
}

// userTag 画一个带头像的用户标识。Color 为零值（透明）时沿用主题正文色；
// MaxLines 为 0 / 1 时单行显示，更大时按指定行数折行（通知标题这类较长的
// 句子仍能换行）。
func (a *app) userTag(c *ui.Context, u userTagSpec) ui.Element {
	if u.Name == "" {
		return ui.Element{}
	}
	size := u.Size
	if size <= 0 {
		size = 12
	}
	avatar := userAvatarSize(size)
	gap := avatar * 0.3
	if gap < 4 {
		gap = 4
	}
	row := ui.Row(c).Gap(gap).AlignItems(ui.Center)
	row.Children(func() {
		ui.Avatar(c, u.Name, a.avatarBitmap(u.Avatar)).Size(avatar, avatar)
		label := u.Label
		if label == "" {
			label = u.Name
		}
		text := ui.Text(c, label).FontSize(size)
		if u.MaxLines > 1 {
			text.MaxLines(u.MaxLines)
		} else {
			text.SingleLine()
		}
		if u.Bold {
			text.Bold()
		}
		if u.Color != ui.Transparent {
			text.TextColor(u.Color)
		}
	})
	return row
}

// avatarEntry 是一个头像地址对应的下载状态。无窗口的 headless 测试里
// publish 直接在下载协程中执行，因此位图字段要加锁，和 articleImageEntry
// 同一处理。
type avatarEntry struct {
	mu  sync.Mutex
	bmp *ui.Bitmap
}

func (e *avatarEntry) bitmap() *ui.Bitmap {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bmp
}

// avatarCache 是头像地址到下载状态的映射。同样带锁：无窗口测试里数据
// 加载后的预取可能发生在请求协程，而渲染在测试主协程；有窗口时两者都在
// UI 线程，锁只是顺手。
type avatarCache struct {
	mu sync.Mutex
	m  map[string]*avatarEntry
}

// begin 返回地址对应的缓存项；created 为 true 表示这是第一次请求，需要
// 调用方启动下载，否则说明已缓存、正在下载或已经失败。
func (c *avatarCache) begin(raw string) (*avatarEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[raw]; ok {
		return e, false
	}
	if c.m == nil {
		c.m = make(map[string]*avatarEntry)
	}
	e := &avatarEntry{}
	c.m[raw] = e
	return e, true
}

func (c *avatarCache) get(raw string) *avatarEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[raw]
}

// normalizeAvatarURL 把服务端可能给出的相对头像地址补成绝对地址：协议相对
// 的 //host 补 https:，站内绝对路径按媒体域名补全，与表情图片同一套规则。
// 其余地址原样返回，交给调用方用 http(s) 校验拦掉。
func normalizeAvatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	case strings.HasPrefix(raw, "/"):
		return "https://r2.2libra.com" + raw
	}
	return raw
}

// avatarBitmap 只读缓存，不发起下载：头像由数据到达时的 prefetchAvatars
// 预取，还没到就先由 ui.Avatar 画昵称首字母。
func (a *app) avatarBitmap(raw string) *ui.Bitmap {
	raw = normalizeAvatarURL(raw)
	if raw == "" {
		return nil
	}
	e := a.avatars.get(raw)
	if e == nil {
		return nil
	}
	return e.bitmap()
}

// prefetchAvatars 为头像地址启动一次后台下载。同一个地址只下一次：无论
// 成功、失败还是仍在下载都记在缓存里，失败后不再重试，避免每帧重发
// 注定失败的请求。
func (a *app) prefetchAvatars(urls ...string) {
	if !a.autoLoad {
		return // headless 测试不碰网络
	}
	for _, raw := range urls {
		raw = normalizeAvatarURL(raw)
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		e, created := a.avatars.begin(raw)
		if !created {
			continue
		}
		go func() {
			bmp, err := a.loadBitmap(raw)
			e.mu.Lock()
			e.bmp = bmp
			e.mu.Unlock()
			if err != nil {
				log.Printf("头像加载失败：%v", err)
				return
			}
			a.publish(func() {}) // 走 Window.Update，让头像出现在下一帧
		}()
	}
}

// prefetchReplyAvatars 预取一页评论作者的头像。
func (a *app) prefetchReplyAvatars(replies []replyItem) {
	urls := make([]string, 0, len(replies))
	for _, r := range replies {
		urls = append(urls, r.Avatar)
	}
	a.prefetchAvatars(urls...)
}

// prefetchItemAvatars 预取列表行里会展示的用户头像：动态 / 最新评论行的
// 作者与被回复人、普通行的作者，以及通知的操作者。
func (a *app) prefetchItemAvatars(items []Item) {
	urls := make([]string, 0, len(items)*2)
	for _, it := range items {
		if it.Extra != nil {
			urls = append(urls, it.Extra.AuthorAvatar, it.Extra.ReplyToAvatar)
		}
		urls = append(urls, it.AuthorAvatar)
		if it.Notif != nil {
			v := notificationViewOf(it.Notif)
			urls = append(urls, v.ActorAvatar)
			for _, g := range v.ReactionGroups {
				for _, u := range g.Users {
					urls = append(urls, u.Avatar)
				}
			}
		}
	}
	a.prefetchAvatars(urls...)
}

// prefetchCoinTransactions 预取金币流水里打赏人等来源用户的头像。
func (a *app) prefetchCoinTransactions(txs []coinTransaction) {
	urls := make([]string, 0, len(txs))
	for _, tx := range txs {
		urls = append(urls, tx.FromAvatar)
	}
	a.prefetchAvatars(urls...)
}
