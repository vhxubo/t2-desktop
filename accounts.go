package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// 多账号
//
// 本机可以同时挂多个 2Libra 账号，像聊天软件那样：每个账号有自己的主窗口、
// 自己的令牌、自己的本机缓存作用域，随时点开或收起，不用重新授权。
//
// 分工：
//
//   - AuthManager 仍然只负责「一个账号」：一份令牌、一次刷新、一次授权、
//     一条凭据（见 auth.go 的 tokenStore）。
//   - accountBook 管「本机有哪些账号」：账号索引、每个账号一条令牌、每个
//     账号一个主窗口，以及窗口没开的账号的未读角标与令牌保活。
//   - app 是「一个账号的界面」：它只读自己的 auth，只画自己的窗口。
//
// 线程：账号册里的账号列表、未读数等由 mu 保护，任何线程都能读；窗口与
// 托盘是原生资源，只在主线程（UI 线程）碰。后台循环只更新账号册里的数字，
// 不碰窗口对象，重画走 Window.Invalidate（mygo 允许从任意线程调用）。

const (
	accountsFileName = "accounts.json"
	accountsVersion  = 1

	// 窗口没开的账号也要有未读角标：一分钟轮一次未读数。窗口开着的账号由
	// 自己的 SSE 长连接推进，这里跳过。
	accountUnreadInterval = time.Minute
	// 令牌保活：每 10 分钟看一遍，快过期的提前 20 分钟就换掉，切过去时
	// 不用先等一次刷新。
	accountKeepAliveInterval  = 10 * time.Minute
	accountTokenRefreshWindow = 20 * time.Minute
	accountKeepAliveTimeout   = 30 * time.Second
	accountUnreadPollTimeout  = 15 * time.Second
	accountLoginNameFallback  = "未命名账号"
)

// accountRecord 是落盘的账号索引条目：只有非敏感的展示信息，令牌在各自的
// 凭据里（见 auth.go）。索引里没有它时不认识这个账号，索引里多一条而凭据
// 没了就显示「登录已失效」，等用户重新登录。
type accountRecord struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
	AddedAt  int64  `json:"added_at,omitempty"`
}

// accountsFile 是 accounts.json 的内容。Active 只是「上次用的那个」，用来决定
// 启动时先把哪个窗口带到前台，不影响别的账号。
type accountsFile struct {
	Version  int              `json:"version"`
	Active   string           `json:"active,omitempty"`
	Accounts []*accountRecord `json:"accounts"`
}

// account 是本机登录过的一个账号：一份会话、一个（可能没打开的）主窗口。
type account struct {
	ID       string
	Username string
	Avatar   string
	AddedAt  int64

	auth *AuthManager
	// app 是这个账号的主窗口与界面状态。窗口是懒建的：没打开时为 nil，
	// 打开窗口时新建，窗口关闭时丢弃（本机缓存都在磁盘上，重开就回来）。
	app *app
	// unread 是窗口没开时后台轮到的未读数；窗口开着时以 app.unread 为准。
	unread int
}

// name 是账号在面板、托盘与窗口标题里的展示名。昵称还没读到时给一个
// 说得过去的占位，而不是空白。
func (acc *account) name() string {
	if acc.Username != "" {
		return acc.Username
	}
	return accountLoginNameFallback
}

// record 把它变成可落盘的一条。
func (acc *account) record() *accountRecord {
	return &accountRecord{
		ID:       acc.ID,
		Username: acc.Username,
		Avatar:   acc.Avatar,
		AddedAt:  acc.AddedAt,
	}
}

// sessionChanged 报告这个账号的会话是否还在（刷新令牌被服务端拒绝后会被
// 清掉）。不在时面板显示「登录已失效」，并给出重新登录入口。
func (acc *account) sessionChanged() bool {
	return acc.auth == nil || !acc.auth.loggedIn()
}

// accountView 是界面渲染一个账号要用的快照：界面只在主线程读渲染帧，账号
// 列表却可能被后台线程改动，所以先拍一份再画。
type accountView struct {
	ID         string
	Name       string
	Avatar     string
	Open       bool
	Unread     int
	NeedsLogin bool
}

// accountLogin 是一次账号授权（添加账号或重新登录）。浏览器里点的可能是任何
// 一个 2Libra 账号，所以授权过程先挂在一个临时 AuthManager 上，令牌只留在
// 内存；拿到用户名之后才决定它属于哪个账号。
type accountLogin struct {
	auth *AuthManager
	// accountID 非空表示这是给已有账号重新登录（成功时先核对昵称）。
	accountID string
	// url 是这次的授权地址：内置浏览器窗口关掉后还能改用系统浏览器重开。
	url string
	// embedded 表示这次授权在应用内置的浏览器窗口里进行（独立会话），而不是
	// 系统浏览器。
	embedded bool
	// paste 是用户粘贴回调链接的输入框内容（浏览器没自动跳回时的兜底）。
	paste string
}

// accountBook 是本机的账号册：共享服务 + 账号列表 + 窗口与托盘的调度。
type accountBook struct {
	dir       string
	proxy     *proxyManager
	theme     *themeManager
	images    *imageCache
	kv        *localKV
	closeMode *closeModeStore

	mu       sync.Mutex
	accounts []*account
	active   string
	pending  *accountLogin
	// loginMsg 是面板上「添加账号」的进行 / 结果说明。
	loginMsg string

	// 以下只在主线程读写。
	panelWin   *mygo.Window
	confirmOut string // 面板里正在确认「退出这个账号」的账号 id
	// loginWin / loginView 是内置浏览器授权窗口（见 login_window.go）；
	// loginErr 是它起不来或页面加载失败时的说明。只在主线程读写。
	loginWin  *mygo.Window
	loginView *mygo.WebView
	loginErr  string
	// loginURL 是内置浏览器最近一次提交的地址。站点登录完成后往往停在自己的
	// 页面上，授权流程不会自己继续：界面据此露出「重新授权」（见
	// loginLeftAuthorizePage）。
	loginURL   string
	guest      *app
	tray       *mygo.Tray
	trayReady  bool
	quitting   bool
	schemeWarn string
	updates    bool
	// webview 表示系统 webview 可用（WebView2 / WKWebView / WebKitGTK），
	// 内置浏览器授权窗口才建得出来。应用就绪时问一次。
	webview bool
	// imgHTTP 是账号面板预取头像用的客户端：与窗口里的图片一样走配置的代理。
	imgHTTP *http.Client
	// notify / openURL / quitApp 是可以替换的系统动作：无头测试里换成记录
	// 调用，不碰原生窗口、浏览器与主循环。
	notify  func(nativeNotice) error
	openURL func(string) error
	quitApp func()
	// mainLoop 表示应用主循环已经跑起来了：只有这时才能把工作投到主线程。
	mainLoop bool
	// afterReady 表示 mygo 应用已经就绪，可以创建原生窗口了。
	afterReady bool

	bgMu   sync.Mutex
	bgStop chan struct{}

	// 测试钩子：账号列表变化（新增、退出、会话失效）时触发。生产为 nil。
	onAccountsChanged func()
	// bgClient 造后台轮询未读数用的客户端。测试里换成指向 httptest 的客户端，
	// 生产走共享代理的 transport。
	bgClient func(acc *account) *Client
	// newLoginAuth 造一次账号授权用的临时会话（令牌先只放内存）。测试里换成
	// 指向 httptest 的实现，生产用真实 API 与共享代理。
	newLoginAuth func() *AuthManager
}

// newAccountBook 建账号册：装载共享服务、读账号索引、迁移旧版单账号会话。
// 它只读本机文件，不碰原生窗口，可以在 main 的最前面调用。
func newAccountBook(dir string) *accountBook {
	if dir == "" {
		dir = configDir()
	}
	b := &accountBook{
		dir:       dir,
		proxy:     newProxyManager(dir),
		theme:     newThemeManager(dir),
		images:    newImageCache(dir),
		kv:        newOfflineKV(dir),
		closeMode: newCloseModeStore(dir),
		openURL:   func(u string) error { return mygo.Shell.OpenExternal(u) },
	}
	b.imgHTTP = b.proxy.Client(20 * time.Second)
	b.quitApp = b.quitProgram
	b.bgClient = func(acc *account) *Client {
		return &Client{Auth: acc.auth, Proxy: b.proxy}
	}
	b.newLoginAuth = func() *AuthManager {
		m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
		m.store = &memoryTokenStore{}
		return m
	}
	if err := b.kv.load(); err != nil {
		log.Printf("离线缓存不可用：%v", err)
	}
	if err := b.load(); err != nil {
		log.Printf("账号索引不可用：%v", err)
	}
	return b
}

/* ------------------------------------------------------------------
   账号索引
------------------------------------------------------------------ */

// load 读 accounts.json，再迁移旧版单账号会话。主文件损坏时先试上一次写成功
// 的 .bak；两个都坏才另存一份 .bad 副本并按空账号册继续 —— 账号索引坏了不该
// 让应用起不来。
func (b *accountBook) load() error {
	path := filepath.Join(b.dir, accountsFileName)
	var f accountsFile
	recovered := false
	err := readJSONFile(path, &f)
	if err != nil && !os.IsNotExist(err) {
		if backupErr := readJSONFile(accountsBackupPath(path), &f); backupErr == nil {
			// 主文件坏了但备份还在：把坏文件挪走（别让它下次被当成备份），
			// 稍后用读回来的内容重写一份主文件。
			log.Printf("账号索引损坏，已从备份读回账号列表：%v", err)
			bad := path + ".bad"
			if renameErr := os.Rename(path, bad); renameErr == nil {
				log.Printf("损坏的账号索引已另存为 %s", bad)
			}
			recovered = true
		} else {
			bad := path + ".bad"
			if renameErr := os.Rename(path, bad); renameErr == nil {
				log.Printf("账号索引损坏，已另存为 %s：%v", bad, err)
			} else {
				log.Printf("账号索引损坏，且无法另存（%v）：%v", renameErr, err)
			}
			f = accountsFile{}
		}
	}
	b.mu.Lock()
	b.accounts = nil
	b.active = f.Active
	for _, rec := range f.Accounts {
		if rec == nil || rec.ID == "" {
			continue
		}
		if b.accountLocked(rec.ID) != nil {
			continue // 索引里重复的条目：只认第一条
		}
		b.accounts = append(b.accounts, b.accountFromRecordLocked(rec))
	}
	b.mu.Unlock()
	for _, acc := range b.snapshotAccounts() {
		b.bindSession(acc)
	}
	if recovered {
		if err := b.save(); err != nil {
			log.Printf("账号索引重写失败：%v", err)
		}
	}
	return b.migrateLegacySession()
}

// accountsBackupPath 是账号索引上一次写成功的那一份。
func accountsBackupPath(path string) string { return path + ".bak" }

// accountFromRecordLocked 按一条索引建账号及其会话。调用方持有 mu。
func (b *accountBook) accountFromRecordLocked(rec *accountRecord) *account {
	return &account{
		ID:       rec.ID,
		Username: rec.Username,
		Avatar:   rec.Avatar,
		AddedAt:  rec.AddedAt,
		auth:     newAuthManagerFor(b.dir, b.proxy, rec.ID),
	}
}

// save 把账号索引写回 accounts.json。覆盖前先把上一份留成 .bak：账号列表是
// 本机唯一一份「有哪些账号」的索引（令牌本身在各自的凭据里），主文件万一被
// 外部写坏，还能从备份读回来，不用逐个重新授权。进程中途退出最坏是丢了这一
// 条改动。
func (b *accountBook) save() error {
	b.mu.Lock()
	f := accountsFile{Version: accountsVersion, Active: b.active}
	for _, acc := range b.accounts {
		f.Accounts = append(f.Accounts, acc.record())
	}
	b.mu.Unlock()
	path := filepath.Join(b.dir, accountsFileName)
	if err := copyFile(path, accountsBackupPath(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("账号索引备份失败：%v", err)
	}
	return writeJSONFile(path, f)
}

// copyFile 把 src 原样复制到 dst；src 不存在时返回 os.ErrNotExist（由调用方
// 决定这算不算问题）。
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// migrateLegacySession 把旧版的单账号会话迁移成一个正式账号。旧版本本机
// 只有一个登录态，令牌存在 session-token / token.json 里，也没有账号索引；
// 升级后不能让它凭空掉线：先把它换到账号自己的凭据上，再删旧键，中途失败
// 最多是两份令牌，不会丢登录。
func (b *accountBook) migrateLegacySession() error {
	legacy := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	if !legacy.loggedIn() {
		return nil
	}
	id := accountIDFor(legacy.username())
	if existing := b.accountByUsername(legacy.username()); existing != nil {
		// 索引里已经有这个账号：旧凭据只是升级前留下的副本，删掉即可。
		return legacy.store.delete()
	}
	old := legacy.store
	if err := legacy.rekey(tokenStoreFactory(b.dir, id), id); err != nil {
		return fmt.Errorf("迁移旧登录态失败：%w", err)
	}
	if err := old.delete(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("迁移旧登录态：删除旧凭据失败：%v", err)
	}
	acc := &account{
		ID:       id,
		Username: legacy.username(),
		Avatar:   legacy.avatarURL(),
		AddedAt:  time.Now().Unix(),
		auth:     legacy,
	}
	b.mu.Lock()
	b.accounts = append(b.accounts, acc)
	if b.active == "" {
		b.active = id
	}
	b.mu.Unlock()
	b.bindSession(acc)
	log.Printf("已把旧版单账号会话迁移为账号 %s（%s）", id, acc.name())
	return b.save()
}

// accountIDFor 用一个账号的昵称算出它在本机存储里的 id：可读的昵称前缀加上
// 昵称的短哈希。昵称相同就一定相同，所以同一个人重新登录会覆盖同一份凭据；
// 昵称规范化后撞车（大小写、标点）也不会互相覆盖。昵称还没读到时退回随机 id。
func accountIDFor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "user-" + randomHex(4)
	}
	var prefix strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			prefix.WriteRune(r)
		default:
			prefix.WriteByte('_')
		}
	}
	p := prefix.String()
	if len(p) > 24 {
		p = p[:24]
	}
	sum := sha256.Sum256([]byte(name))
	return p + "-" + hex.EncodeToString(sum[:3])
}

// randomHex 返回 n 字节的随机十六进制串，用作还没有昵称的账号 id。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败没有可用的退路：宁可崩，也不生成一个可能重复的 id
		// 去覆盖别人的凭据。
		panic(err)
	}
	return hex.EncodeToString(buf)
}

/* ------------------------------------------------------------------
   账号列表（线程安全）
------------------------------------------------------------------ */

// snapshotAccounts 返回账号列表的一份拷贝，调用方可以随便遍历。
func (b *accountBook) snapshotAccounts() []*account {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*account(nil), b.accounts...)
}

// account 按 id 取账号。
func (b *accountBook) account(id string) *account {
	if id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.accountLocked(id)
}

func (b *accountBook) accountLocked(id string) *account {
	for _, acc := range b.accounts {
		if acc.ID == id {
			return acc
		}
	}
	return nil
}

// accountByUsername 按昵称找账号（大小写不敏感）。同一个 2Libra 账号重新
// 授权时靠它合并，而不是多开一个窗口。
func (b *accountBook) accountByUsername(name string) *account {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, acc := range b.accounts {
		if strings.ToLower(strings.TrimSpace(acc.Username)) == name {
			return acc
		}
	}
	return nil
}

// accountsView 是面板与托盘要的那份快照：带窗口是否开着、未读数与会话是否
// 还有效。界面只读快照；未读数会被后台轮询改写，所以整段读取都在锁内。
func (b *accountBook) accountsView() []accountView {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]accountView, 0, len(b.accounts))
	for _, acc := range b.accounts {
		v := accountView{
			ID:         acc.ID,
			Name:       acc.name(),
			Avatar:     acc.Avatar,
			Open:       acc.app != nil,
			Unread:     acc.unread,
			NeedsLogin: acc.sessionChanged(),
		}
		if acc.app != nil {
			// 窗口开着时以窗口里的未读数为准（它由 SSE 与角标请求维护）。
			v.Unread = acc.app.unread
		}
		out = append(out, v)
	}
	return out
}

// accountCount 是账号册里有多少个账号。
func (b *accountBook) accountCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.accounts)
}

// setActive 记下最近用过的账号并落盘。
func (b *accountBook) setActive(id string) {
	b.mu.Lock()
	changed := b.active != id
	b.active = id
	b.mu.Unlock()
	if changed {
		if err := b.save(); err != nil {
			log.Printf("账号索引写入失败：%v", err)
		}
	}
}

/* ------------------------------------------------------------------
   会话变化与账号册的同步
------------------------------------------------------------------ */

// bindSession 把账号的会话变化接到当前应该处理它的一方：窗口开着时是窗口
// （它要重画、要弹授权），窗口没开时是账号册（只更新索引与面板）。
func (b *accountBook) bindSession(acc *account) {
	if acc == nil || acc.auth == nil {
		return
	}
	if acc.app != nil {
		acc.app.bindAuth()
		return
	}
	acc.auth.onChange = func(error) { b.sessionChanged(acc) }
}

// sessionChanged 是窗口没开的账号的会话变化：令牌被拒后把它标成「登录已
// 失效」，面板据此给出重新登录入口。
func (b *accountBook) sessionChanged(acc *account) {
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}

// noteAuthChange 是窗口里的会话变化：把昵称、头像同步回账号索引，并把
// 「登录是否还有效」的变化反映到面板上。只在主线程调用。
func (b *accountBook) noteAuthChange(acc *account) {
	if acc == nil || acc.auth == nil {
		return
	}
	name := acc.auth.username()
	avatar := acc.auth.avatarURL()
	changed := false
	b.mu.Lock()
	if name != "" && acc.Username != name {
		acc.Username, changed = name, true
	}
	if avatar != "" && acc.Avatar != avatar {
		acc.Avatar, changed = avatar, true
	}
	if acc.auth.loggedIn() {
		// 重新登录成功后未读数重新从服务端读，旧的先清掉。
		acc.unread = 0
	}
	b.mu.Unlock()
	if changed {
		if err := b.save(); err != nil {
			log.Printf("账号索引写入失败：%v", err)
		}
	}
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}

func (b *accountBook) changedHook() func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.onAccountsChanged
}

/* ------------------------------------------------------------------
   添加账号 / 重新登录：授权走系统浏览器或内置浏览器
------------------------------------------------------------------ */

// startAddAccount 开始一次「添加账号」授权。被授权的可能是任何一个 2Libra
// 账号 —— 已有的会换成新的会话，新的会开一个自己的主窗口。同一时刻只允许
// 一次账号授权：回调只带 code 与 state，多开会分不清哪一次回来了。
//
// 走哪条路看账号册：本机还没有账号时用系统浏览器（那里可能一个账号都没登，
// 直接授权最快）；已经有账号时用内置浏览器的独立会话，否则系统浏览器会把
// 授权码发给网页里正登着的那个账号，用户得先去网页端退出登录。
func (b *accountBook) startAddAccount() {
	if b.pendingLogin() != nil {
		b.setLoginMsg("已经有一次授权在进行，请先完成它，或点「取消」。")
		b.showPanel()
		return
	}
	// 令牌先只放内存：这时还不知道授权回来的是哪个账号，拿不到它的存储键。
	b.beginAccountLogin(b.newLoginAuth(), "", nil)
}

// reloginAccount 给一个令牌已失效的账号重新授权。授权也用一份临时会话：
// 授权回来的昵称会和这个账号核对，不是同一个人就另算新账号，绝不把别人的
// 令牌装到它头上（见 persistLogin）。
func (b *accountBook) reloginAccount(id string) {
	acc := b.account(id)
	if acc == nil {
		return
	}
	if b.pendingLogin() != nil {
		b.setLoginMsg("已经有一次授权在进行，请先完成或取消它。")
		b.showPanel()
		return
	}
	b.beginAccountLogin(b.newLoginAuth(), id, nil)
}

// beginAccountLogin 是上面两条入口的共同实现：认领全局唯一的那次授权、拿
// 授权地址，然后决定在系统浏览器还是内置浏览器里打开。extraScopes 是补权限
// 时要追加的 scope。
func (b *accountBook) beginAccountLogin(auth *AuthManager, targetID string, extraScopes []string) {
	auth.onChange = func(err error) { b.finishAccountLogin(auth, err) }
	// 一次只允许一次授权在等回调：别的账号窗口正在补授权的话先让位。
	b.claimLogin(auth)
	authorize, err := auth.BeginScopes(context.Background(), extraScopes)
	if err != nil {
		b.setLoginMsg("无法开始授权：" + friendlyError(err))
		b.showPanel()
		return
	}
	p := &accountLogin{auth: auth, accountID: targetID, url: authorize}
	embedded := b.embeddedLoginReady(targetID)
	p.embedded = embedded
	b.mu.Lock()
	b.pending = p
	b.loginMsg = loginWaitingMessage(embedded)
	b.mu.Unlock()
	b.showPanel()
	if !embedded {
		if err := b.openURL(authorize); err != nil {
			b.setLoginMsg("打开浏览器失败：" + err.Error())
			b.redrawPanel()
			return
		}
		return
	}
	if err := b.openLoginWindow(authorize); err != nil {
		// 内置浏览器起不来（缺 WebView2 运行时、平台不支持）：收起那个窗口、
		// 退回系统浏览器 —— 用户至少还有一条能走通的路。
		b.closeLoginWindow()
		b.mu.Lock()
		if b.pending == p {
			b.pending.embedded = false
		}
		b.mu.Unlock()
		if err := b.openURL(authorize); err != nil {
			b.setLoginMsg("内置浏览器与系统浏览器都打不开授权页：" + err.Error())
			b.redrawPanel()
			return
		}
		b.setLoginMsg(loginWaitingMessage(false))
	}
	b.redrawPanel()
}

// loginWaitingMessage 是面板上等待授权时的那句话，按授权方式分开说。
func loginWaitingMessage(embedded bool) string {
	if embedded {
		return "已在内置浏览器窗口里打开授权页：那个窗口是独立会话，可以直接登录要挂的另一个账号。"
	}
	return "已在浏览器打开授权页，同意后会自动跳回。"
}

// embeddedLoginReady 报告这次授权该不该用内置浏览器：系统 webview 可用、
// 应用已就绪，并且要么是本机已经有账号（添加账号时不能再借系统浏览器的
// 登录态），要么是给已有账号重新登录（targetID 非空）。
func (b *accountBook) embeddedLoginReady(targetID string) bool {
	if !b.webview || !b.afterReady {
		return false
	}
	return targetID != "" || b.accountCount() > 0
}

// finishAccountLogin 是一次账号授权的结束（成功或失败）。它从授权协程回到
// 主线程：成功要把会话落进账号册（必要时开窗口），失败只把原因摆到面板上。
func (b *accountBook) finishAccountLogin(auth *AuthManager, err error) {
	b.onMain(func() {
		p := b.takePending(auth)
		if p == nil {
			// 被取代或已取消的那一次：不报告，也不动账号册。
			return
		}
		// 内置浏览器窗口的使命就是这次授权：结束就收起。
		b.closeLoginWindow()
		if err != nil {
			b.setLoginMsg(friendlyError(err))
			if p.accountID != "" {
				b.bindSession(b.account(p.accountID))
			}
			return
		}
		acc, err := b.persistLogin(p)
		if err != nil {
			b.setLoginMsg(err.Error())
			return
		}
		b.openWindow(acc.ID)
	})
}

// persistLogin 把一次成功授权落进账号册。给已有账号重新授权时先核对身份：
// 授权回来的是另一个账号的话，就当新账号添进去，原账号继续标着要重新登录 ——
// 绝不把别人的令牌装到它头上。
func (b *accountBook) persistLogin(p *accountLogin) (*account, error) {
	name := p.auth.username()
	if p.accountID != "" {
		if target := b.account(p.accountID); target != nil {
			if target.Username == "" || name == "" || strings.EqualFold(target.Username, name) {
				if err := b.applySession(target, p.auth); err != nil {
					return nil, err
				}
				b.setLoginMsg("已重新登录：" + target.name())
				return target, nil
			}
			b.setLoginMsg("授权回来的是「" + name + "」，不是「" + target.name() +
				"」。已把它添成另一个账号；原来那个仍需要重新登录。")
			return b.adoptLogin(p.auth)
		}
	}
	acc, err := b.adoptLogin(p.auth)
	if err == nil && p.accountID == "" {
		b.setLoginMsg("已登录：" + acc.name())
	}
	return acc, err
}

// applySession 把一份新令牌装到已有账号上（重新授权成功）：凭据换到它的
// 账号键、窗口改读新会话，账号索引里的昵称与头像跟着更新。
func (b *accountBook) applySession(acc *account, auth *AuthManager) error {
	if err := auth.rekey(tokenStoreFactory(b.dir, acc.ID), acc.ID); err != nil {
		return fmt.Errorf("保存令牌失败：%w", err)
	}
	auth.onChange = nil
	b.mu.Lock()
	acc.auth = auth
	acc.Username = orString(auth.username(), acc.Username)
	acc.Avatar = orString(auth.avatarURL(), acc.Avatar)
	acc.unread = 0
	b.mu.Unlock()
	if app := acc.app; app != nil {
		// 窗口开着：换掉它的会话，界面按新令牌重建。
		app.replaceAuth(auth)
	}
	b.bindSession(acc)
	if err := b.save(); err != nil {
		return fmt.Errorf("账号索引写入失败：%w", err)
	}
	log.Printf("已重新登录账号 %s（%s）", acc.ID, acc.name())
	return nil
}

// adoptLogin 把一次成功授权落成一个账号：同一昵称的账号已经在本机时换成
// 它的会话，否则新增一条。只在主线程调用。
func (b *accountBook) adoptLogin(auth *AuthManager) (*account, error) {
	name := auth.username()
	existing := b.accountByUsername(name)
	if existing == nil && name != "" {
		existing = b.account(accountIDFor(name))
	}
	if existing != nil {
		if err := b.applySession(existing, auth); err != nil {
			return nil, err
		}
		return existing, nil
	}

	id := accountIDFor(name)
	if b.account(id) != nil {
		// 极端情况：同一个昵称算出了已存在的 id，或者随机 id 撞车。换一个随机
		// 后缀，绝不覆盖别人的凭据。
		id = id + "-" + randomHex(2)
	}
	if err := auth.rekey(tokenStoreFactory(b.dir, id), id); err != nil {
		return nil, fmt.Errorf("保存令牌失败：%w", err)
	}
	auth.onChange = nil
	acc := &account{
		ID:       id,
		Username: name,
		Avatar:   auth.avatarURL(),
		AddedAt:  time.Now().Unix(),
		auth:     auth,
	}
	b.mu.Lock()
	b.accounts = append(b.accounts, acc)
	b.active = id
	b.mu.Unlock()
	b.bindSession(acc)
	if err := b.save(); err != nil {
		return nil, fmt.Errorf("账号索引写入失败：%w", err)
	}
	log.Printf("已添加账号 %s（%s）", id, acc.name())
	return acc, nil
}

// orString 返回第一个非空值：账号索引里的展示信息宁可用旧的，也不被空值
// 覆盖（身份接口偶发失败时不该把昵称清掉）。
func orString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// pendingLogin 返回正在进行的添加授权，没有时为空。
func (b *accountBook) pendingLogin() *accountLogin {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending
}

// loginStatus 返回面板上要显示的那句话。
func (b *accountBook) loginStatus() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loginMsg
}

func (b *accountBook) setLoginMsg(msg string) {
	b.mu.Lock()
	b.loginMsg = msg
	b.mu.Unlock()
	b.redrawPanel()
}

// takePending 收掉正在进行的授权；不是当前这一次时什么都不做（被取代的
// 授权不能盖掉新一次的提示）。
func (b *accountBook) takePending(auth *AuthManager) *accountLogin {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil || b.pending.auth != auth {
		return nil
	}
	p := b.pending
	b.pending = nil
	return p
}

// cancelLogin 放弃正在进行的账号授权（添加账号或重新登录）：丢掉 PKCE 会话、
// 收起内置浏览器窗口，迟到的回调也不能再把账号签进来。
func (b *accountBook) cancelLogin() {
	p := b.pendingLogin()
	if p == nil {
		return
	}
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
	p.auth.Cancel()
	b.closeLoginWindow()
	if p.accountID != "" {
		// 重新登录被取消：把会话的处理函数交还给窗口 / 账号册。
		b.bindSession(b.account(p.accountID))
	}
	b.setLoginMsg("已取消这次授权")
	b.redrawPanel()
}

// completeCallback 把系统投来的回调链接交给正在等它的那次授权。返回 false
// 表示当时没有在等授权的账号（例如应用是被这次点击刚拉起来的，PKCE 会话
// 不在本进程）。
func (b *accountBook) completeCallback(raw string) bool {
	p := b.pendingLogin()
	if p == nil {
		// 账号窗口里正在补授权时回调由窗口自己处理（见 app.handleCallbackURL），
		// 这里只兜住账号册这一侧。
		return false
	}
	if err := p.auth.Complete(raw); err != nil {
		b.setLoginMsg("回调无法完成登录：" + err.Error())
		return true
	}
	b.setLoginMsg("已收到回调，正在用授权码换取令牌…")
	return true
}

/* ------------------------------------------------------------------
   窗口
------------------------------------------------------------------ */

// openWindow 打开（或带到前台）一个账号的主窗口。窗口是一份界面状态：没有
// 就建一个，有就只拉到最前面。最近用过的账号立刻记下，建窗口排到下一轮主线程
// 处理 —— 打开窗口是原生资源的操作，不能在别的窗口构建这一帧的过程中做。
func (b *accountBook) openWindow(id string) {
	b.setActive(id)
	b.postMain(func() { b.openWindowNow(id) })
}

// openWindowNow 在主线程上真正建出账号窗口。
func (b *accountBook) openWindowNow(id string) {
	acc := b.account(id)
	if acc == nil {
		return
	}
	if acc.app != nil {
		acc.app.raiseMainWindow()
		return
	}
	if !b.afterReady {
		// 应用还没就绪（启动阶段先记下要开的窗口，见 restoreWindows）。
		return
	}
	app := newAccountApp(b, acc)
	acc.app = app
	b.closeGuestWindow()
	app.openMainWindow()
	b.redrawPanel()
}

// windowClosed 是一个账号窗口关掉后的收尾：界面状态整个丢掉（本机缓存都在
// 磁盘上），账号本身留着 —— 它还在账号册里，随时可以再打开。
func (b *accountBook) windowClosed(app *app) {
	if app.accountID != "" {
		if acc := b.account(app.accountID); acc != nil && acc.app == app {
			acc.app = nil
			b.bindSession(acc)
			// 窗口关掉后未读角标改由后台轮询维护，先沿用窗口里的最后一个值。
			b.mu.Lock()
			acc.unread = app.unread
			b.mu.Unlock()
		}
	}
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}

// logoutAccount 退出一个账号：清掉它的令牌、把它从账号册里摘掉，并关掉它的
// 所有窗口。账号册空了就回到未登录窗口。只在主线程调用。
func (b *accountBook) logoutAccount(id string) {
	acc := b.account(id)
	if acc == nil {
		return
	}
	if p := b.pendingLogin(); p != nil && p.accountID == id {
		b.cancelLogin()
	}
	// 这是最后一个账号：先把未登录窗口摆出来，再关它的窗口。窗口是最后一个
	// 关掉的，mygo 会直接结束应用 —— 那样用户就看不到可以重新登录的窗口了。
	if b.accountCount() == 1 && b.afterReady {
		b.openGuestWindowNow()
	}
	if app := acc.app; app != nil {
		acc.app = nil
		app.closeAllWindows()
	}
	// 这个账号可能还有一次授权在等回调（窗口里的补权限登录）：一并作废，
	// 否则迟到的回调会把刚退出的账号又签回来。
	acc.auth.Cancel()
	acc.auth.onChange = nil
	// 清内存并删掉这份凭据；删不掉只记日志 —— 索引照样摘掉，别让一份删不掉
	// 的凭据把账号留在列表里。
	if err := acc.auth.clearToken(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("退出账号 %s：删除凭据失败：%v", id, err)
	}
	b.mu.Lock()
	out := b.accounts[:0]
	for _, it := range b.accounts {
		if it.ID != id {
			out = append(out, it)
		}
	}
	b.accounts = out
	if b.active == id {
		b.active = ""
		if len(out) > 0 {
			b.active = out[0].ID
		}
	}
	b.mu.Unlock()
	log.Printf("已退出账号 %s", id)
	if err := b.save(); err != nil {
		log.Printf("账号索引写入失败：%v", err)
	}
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}

// restoreWindows 按账号册把窗口开出来：每个账号一个主窗口，最近用过的那个
// 提到前台。没有账号时开未登录窗口 —— 不登录也能浏览公开内容。
func (b *accountBook) restoreWindows() {
	b.afterReady = true
	accounts := b.snapshotAccounts()
	if len(accounts) == 0 {
		b.openGuestWindowNow()
		return
	}
	active := b.activeAccountID()
	for _, acc := range accounts {
		b.openWindowNow(acc.ID)
	}
	if active != "" {
		if acc := b.account(active); acc != nil && acc.app != nil {
			acc.app.raiseMainWindow()
		}
	}
	b.redrawPanel()
}

// activeAccountID 返回最近用过的账号 id。
func (b *accountBook) activeAccountID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// openGuestWindow 打开未登录窗口：账号册空着时它是唯一的窗口，登录入口在
// 侧边栏底部与账号面板里。
func (b *accountBook) openGuestWindow() {
	b.postMain(b.openGuestWindowNow)
}

// openGuestWindowNow 在主线程上真正建出未登录窗口。
func (b *accountBook) openGuestWindowNow() {
	if b.guest != nil {
		b.guest.raiseMainWindow()
		return
	}
	if !b.afterReady {
		return
	}
	guest := newGuestApp(b)
	b.guest = guest
	guest.openMainWindow()
}

// guestWindowClosed 是未登录窗口关掉后的收尾。
func (b *accountBook) guestWindowClosed(app *app) {
	if b.guest == app {
		b.guest = nil
	}
}

// closeGuestWindow 关掉未登录窗口：第一个账号的窗口开起来之后它就没用了，
// 两个窗口并排只会让人以为登录失败。
func (b *accountBook) closeGuestWindow() {
	guest := b.guest
	if guest == nil {
		return
	}
	b.guest = nil
	guest.closeAllWindows()
}

// guestApp 返回未登录窗口的界面状态，没有时为 nil。
func (b *accountBook) guestApp() *app { return b.guest }

// raiseAccountWindow 把一个账号的窗口带到前台（托盘、面板、通知点击都走它）。
func (b *accountBook) raiseAccountWindow(id string) {
	if acc := b.account(id); acc != nil && acc.app != nil {
		acc.app.raiseMainWindow()
		return
	}
	b.openWindow(id)
}

/* ------------------------------------------------------------------
   后台：未读角标与令牌保活
------------------------------------------------------------------ */

// startBackground 起两个后台循环：给窗口没开的账号轮询未读数，以及给所有
// 账号的令牌提前续期。只在应用就绪后调用一次，退出时由 stopBackground 停。
func (b *accountBook) startBackground() {
	b.bgMu.Lock()
	if b.bgStop != nil {
		b.bgMu.Unlock()
		return
	}
	stop := make(chan struct{})
	b.bgStop = stop
	b.bgMu.Unlock()
	go b.unreadLoop(stop)
	go b.keepAliveLoop(stop)
}

// stopBackground 停掉后台循环。退出流程里调用，重复调用无害。
func (b *accountBook) stopBackground() {
	b.bgMu.Lock()
	stop := b.bgStop
	b.bgStop = nil
	b.bgMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func (b *accountBook) unreadLoop(stop <-chan struct{}) {
	// 先歇一拍再轮：启动时每个窗口都会自己读一次未读，不必紧接着再发一轮。
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			b.pollUnread()
			timer.Reset(accountUnreadInterval)
		}
	}
}

// pollUnread 给窗口没开的账号读一次未读数。窗口开着的账号由它自己的 SSE
// 与角标负责，这里跳过：同一份数据不重复拉。
func (b *accountBook) pollUnread() {
	for _, acc := range b.snapshotAccounts() {
		if acc.app != nil || acc.sessionChanged() {
			continue
		}
		go b.pollAccountUnread(acc)
	}
}

func (b *accountBook) pollAccountUnread(acc *account) {
	ctx, cancel := context.WithTimeout(context.Background(), accountUnreadPollTimeout)
	defer cancel()
	n, err := b.bgClient(acc).fetchUnreadNotificationCount(ctx)
	if err != nil {
		return // 令牌问题由 withAuthRetry 清到位；网络错误下一轮再试
	}
	b.mu.Lock()
	if b.accountLocked(acc.ID) != nil {
		acc.unread = n
	}
	b.mu.Unlock()
	b.redrawPanel()
}

func (b *accountBook) keepAliveLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(accountKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b.keepAliveOnce()
		}
	}
}

// keepAliveOnce 给每个账号看一次令牌：快过期就换掉，被服务端明确拒绝的
// 标成「登录已失效」。窗口没开的账号靠这一条保持在登录状态，切过去就能用。
func (b *accountBook) keepAliveOnce() {
	for _, acc := range b.snapshotAccounts() {
		if acc.sessionChanged() {
			continue
		}
		go b.keepAliveAccount(acc)
	}
}

func (b *accountBook) keepAliveAccount(acc *account) {
	ctx, cancel := context.WithTimeout(context.Background(), accountKeepAliveTimeout)
	defer cancel()
	err := acc.auth.refreshIfExpiring(ctx, accountTokenRefreshWindow)
	if err == nil || !authFailure(err) {
		// 网络抖动、服务端 5xx 都只影响这一次：令牌还在，下一轮再试。
		return
	}
	// 服务端明确拒绝（invalid_grant 之类）：这个账号要重新授权。清掉令牌，
	// 窗口开着时窗口会弹出授权，没开时面板上标成「登录已失效」。
	acc.auth.invalidate(err)
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}

/* ------------------------------------------------------------------
   主线程投递
------------------------------------------------------------------ */

// onMain 把 fn 投到主线程执行并等它结束；应用还没进主循环（无头测试）时就地
// 执行。
func (b *accountBook) onMain(fn func()) {
	if !b.mainLoop {
		fn()
		return
	}
	mygo.RunOnMain(fn)
}

// postMain 把 fn 排到主线程的下一次事件处理里（不阻塞当前这一帧）。打开窗口
// 这类原生资源的操作走这条路：和 showSettings 经 Window.Update 打开设置窗口
// 是同一个道理，不能在别的窗口构建这一帧的过程中建出新窗口。
func (b *accountBook) postMain(fn func()) {
	if win := b.anyWindow(); win != nil {
		win.Update(fn)
		return
	}
	b.onMain(fn)
}

// anyWindow 返回任意一个还活着的窗口：账号面板与内置浏览器窗口的请求借它排
// 到主循环上。
func (b *accountBook) anyWindow() *mygo.Window {
	if b.loginWin != nil {
		return b.loginWin
	}
	if b.panelWin != nil {
		return b.panelWin
	}
	if b.guest != nil && b.guest.win != nil {
		return b.guest.win
	}
	for _, acc := range b.snapshotAccounts() {
		if acc.app != nil && acc.app.win != nil {
			return acc.app.win
		}
	}
	return nil
}

/* ------------------------------------------------------------------
   给界面用的查询
------------------------------------------------------------------ */

// schemeWarning 是「系统没有把回调协议关联到本应用」的说明，仅在这种情况下
// 非空：它决定了浏览器授权后能不能自动跳回。
func (b *accountBook) schemeWarning() string { return b.schemeWarn }

// trayIsReady 报告托盘图标真的建出来了：只有这时「关闭窗口收进托盘」才敢
// 藏窗口，否则用户既没有图标也回不到窗口。
func (b *accountBook) trayIsReady() bool { return b.trayReady }

// isQuitting 表示已经在退出流程里：这时窗口的关闭不能再被拦成「收进托盘」。
func (b *accountBook) isQuitting() bool { return b.quitting }

// summary 是账号面板与托盘里的一句话汇总：几个账号、一共几条未读。
func (b *accountBook) summary() string {
	accounts := b.accountsView()
	if len(accounts) == 0 {
		return "未登录"
	}
	unread := 0
	for _, v := range accounts {
		unread += v.Unread
	}
	text := fmt.Sprintf("%d 个账号", len(accounts))
	if unread > 0 {
		text += fmt.Sprintf("，%d 条未读", unread)
	}
	return text
}

/* ------------------------------------------------------------------
   会话变化与账号册的同步（按 id）
------------------------------------------------------------------ */

// noteAuthChanged 把一个窗口的会话变化同步进账号索引：昵称、头像与「登录
// 是否还有效」。只在主线程调用（窗口的 onAuthChanged 就在主线程）。
func (b *accountBook) noteAuthChanged(id string) {
	if acc := b.account(id); acc != nil {
		b.noteAuthChange(acc)
	}
}

// claimLogin 让这次授权成为全局唯一在等回调的那一次：同时进行的别的授权
// （另一个账号窗口在补权限、账号册在添加账号）先取消 —— 回调只带 code 与
// state，分不清是哪一次回来了。
func (b *accountBook) claimLogin(m *AuthManager) {
	if m == nil {
		return
	}
	b.mu.Lock()
	var dropped *accountLogin
	if b.pending != nil && b.pending.auth != m {
		dropped = b.pending
		b.pending = nil
	}
	b.mu.Unlock()
	if dropped != nil {
		dropped.auth.Cancel()
		// 被取代的那次授权可能正开在内置浏览器窗口里：窗口一并收起，别留一个
		// 已经作废的授权页在屏幕上。
		b.closeLoginWindow()
	}
	for _, acc := range b.snapshotAccounts() {
		if acc.auth != nil && acc.auth != m {
			acc.auth.Cancel()
		}
	}
	if b.guest != nil && b.guest.auth != m {
		b.guest.auth.Cancel()
	}
}

// appAwaitingLogin 返回正在等授权回调的账号窗口（一次授权只可能有一个，
// 见 claimLogin）。
func (b *accountBook) appAwaitingLogin() *app {
	for _, acc := range b.snapshotAccounts() {
		if acc.app != nil && acc.app.auth != nil && acc.app.auth.awaitingLogin() {
			return acc.app
		}
	}
	if b.guest != nil && b.guest.auth != nil && b.guest.auth.awaitingLogin() {
		return b.guest
	}
	return nil
}

/* ------------------------------------------------------------------
   深链、通知点击、系统事件
------------------------------------------------------------------ */

// handleDeepLink 接收系统投来的深链（OAuth 回调）。它把链接交给正在等它的
// 那一次授权：账号册自己的「添加账号」，或某个账号窗口的补授权。
//
// 每条链接都打到日志里：系统根本没投递、和投递了一个拼写不一样的地址，
// 在窗口里看起来都是「什么都没发生」，只有日志分得清。
func (b *accountBook) handleDeepLink(raw string) {
	raw = strings.TrimSpace(raw)
	if !hasCallbackScheme(raw) {
		log.Printf("深链：忽略非本应用协议的链接 %q", raw)
		return
	}
	log.Printf("深链：收到 %q", raw)
	// 账号册这一侧在等（添加账号 / 重新登录）。
	if b.completeCallback(raw) {
		b.showPanel()
		return
	}
	// 某个账号窗口在补授权（缺权限后的重新授权）。
	if app := b.appAwaitingLogin(); app != nil {
		app.raiseMainWindow()
		app.publish(func() { app.handleCallbackURL(raw) })
		return
	}
	// 没有人在等：多半是这条链接把应用刚拉起来，PKCE 会话不在本进程。
	b.setLoginMsg("收到的回调没有对应的授权（应用可能是被这个链接刚启动的）。请在「账号」面板里重新发起一次授权。")
	b.showPanel()
}

// openNotice 处理原生通知的点击：先找到消息属于哪个账号，再把那个窗口拉到
// 前台并打开目标。系统通知可能比进程活得久，找不到归属时退回站点页面。
func (b *accountBook) openNotice(id string) {
	b.onMain(func() {
		for _, acc := range b.snapshotAccounts() {
			app := acc.app
			if app == nil {
				continue
			}
			if target := app.noticeTargets[id]; target != "" {
				b.raiseAccountWindow(acc.ID)
				app.publish(func() { app.openNotificationTarget(target) })
				return
			}
		}
		if b.guest != nil {
			if target := b.guest.noticeTargets[id]; target != "" {
				b.guest.raiseMainWindow()
				b.guest.publish(func() { b.guest.openNotificationTarget(target) })
				return
			}
		}
		// 冷启动后系统才把点击交回来：映射没了，回到站内消息中心。
		if err := b.openURL(siteBaseURL + "/notifications"); err != nil {
			log.Printf("打开消息中心失败：%v", err)
		}
	})
}

// onBecameActive 是应用回到前台时：每个开着的窗口都校准一次未读数与金币数。
// 站内读掉消息不会产生通知事件，只能靠这一次校准跟上。
func (b *accountBook) onBecameActive() {
	b.onMain(func() {
		for _, acc := range b.snapshotAccounts() {
			if acc.app != nil {
				app := acc.app
				app.publish(app.onBecameActive)
			}
		}
		if b.guest != nil {
			b.guest.publish(b.guest.onBecameActive)
		}
	})
}

// markQuitting 标记「正在退出」：所有窗口的关闭都不该再被拦成「收进托盘」，
// 后台循环与内置浏览器授权也要停下。
func (b *accountBook) markQuitting() {
	b.quitting = true
	b.stopBackground()
	// 正在等授权的内置浏览器窗口：退出时一并放弃，迟到的回调不会再签账号。
	b.cancelLogin()
	b.closeLoginWindow()
	for _, acc := range b.snapshotAccounts() {
		if acc.app != nil {
			acc.app.quitting = true
		}
	}
	if b.guest != nil {
		b.guest.quitting = true
	}
}

// quitProgram 退出整个应用：先立「退出中」，再交给 mygo 退出。窗口的
// quitApp 都指向这里，所以托盘、标题栏与系统菜单走的是同一条路。
func (b *accountBook) quitProgram() {
	b.markQuitting()
	mygo.App.Quit()
}

// windowOrigin 给同时开出的多个账号窗口错开位置：全叠在屏幕中央的话，看
// 起来就像只开了一个窗口。
func (b *accountBook) windowOrigin(id string) (int, int) {
	for i, acc := range b.snapshotAccounts() {
		if acc.ID == id {
			return 80 + 32*i, 60 + 32*i
		}
	}
	return 0, 0
}
