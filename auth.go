package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// There is exactly one login flow: a native/desktop public client using a
// private-use URI scheme (RFC 8252 §7.1). The OS hands the callback URL to
// the app registered for appIdentifier. client_id is the value 2Libra issued
// for this application; it is a compile-time constant, not a user setting.
// redirect_uri is fixed too, so it can never drift from the value registered
// on the 2Libra website. A desktop public client has no client_secret:
// PKCE code_verifier alone authenticates the token call.
const (
	appIdentifier      = "com.vhxubo.t2-desktop"
	defaultRedirectURI = appIdentifier + ":/callback"
	clientID           = "e3cf9b14c5104d85af2c2b2a8ce2da36"
	defaultScopes      = "read profile read:me write:comment interact read:economy"
	// Some platforms normalise :/callback into ://callback before handing
	// the URL to the app. This is the same native callback, not a second
	// login method, so it is accepted as a spelling variant only.
	alternateCallbackURI = appIdentifier + "://callback"

	// signupURL 是带邀请码的 2Libra 注册页。注册是网页流程，应用内不做；
	// 未登录用户点「注册」直接打开系统浏览器，注册完成后再回应用里「登录」
	// 授权即可。邀请码 7OzoT5x6M 由 2Libra 下发，绑定本应用的推广来源。
	signupURL = "https://2libra.com/auth/signup/7OzoT5x6M"
)

// defaultScopesList 是 defaultScopes 的逐项形式：申请新 scope 时按它拼授权
// 地址，登录窗口再按它列出「本次申请」。
var defaultScopesList = strings.Fields(defaultScopes)

type tokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	ObtainedAt   int64  `json:"obtained_at"`
	Username     string `json:"username,omitempty"`
	// Avatar is the account's avatar URL, cached so the title bar can show the
	// picture at the next start without asking the profile endpoint again.
	Avatar string `json:"avatar,omitempty"`
	// IsPro / ProKnown / Role 缓存能否用 2Libra 官方图床：Pro 会员或守护者
	// （role=guardian）都可以，而上传不该先等一次资料请求。ProKnown 区分
	// 「确认过没有权限」与「还没读到」。Role 沿用 is_pro/pro_known 这两个
	// 历史 JSON 键之外的独立键；旧令牌没有它，刷新过资料后自然补上。
	IsPro    bool   `json:"is_pro,omitempty"`
	ProKnown bool   `json:"pro_known,omitempty"`
	Role     string `json:"role,omitempty"`
}

// configDir is the config directory. The token itself is kept in the OS
// credential store via mygo.Secrets (see secureTokenStore); this directory
// only holds the fallback plaintext file when no credential store is
// available, plus proxy.json and the image cache. It can be redirected in
// tests with TWOLIBRA_CONFIG_DIR.
func configDir() string {
	if d := os.Getenv("TWOLIBRA_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "T2 Desktop")
}

func readJSONFile(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type loginResult struct {
	code  string
	state string
	err   error
}

// tokenHTTPError is the token endpoint's non-2xx answer. Keeping the status
// on the error lets the refresh path tell a dead grant (4xx) from a
// temporary network or server problem (5xx, 429), which decides whether the
// stored login may be dropped.
type tokenHTTPError struct {
	status int
	msg    string
	fatal  bool // 授权本身被拒（4xx，或 200 信封里 c != 0）
}

func (e *tokenHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d：%s", e.status, e.msg)
}

// errSessionChanged means a refresh finished after the session it belonged
// to was replaced or cleared. Its token must not be stored.
var errSessionChanged = errors.New("登录状态已变化，已丢弃这次刷新的结果")

// pendingLogin lives in memory for the duration of one authorization. No
// listener, no server, no port: the OS delivers the callback URL through
// the registered URL scheme, and Complete() feeds it into result.
type pendingLogin struct {
	state     string
	verifier  string
	challenge string
	result    chan loginResult
	once      sync.Once
}

func (p *pendingLogin) deliver(r loginResult) {
	p.once.Do(func() { p.result <- r })
}

// AuthManager owns the PKCE flow, the native-scheme callback, token rotation,
// and persistent storage. All state changes are reported through onChange
// so the UI can repaint through Window.Update.
type AuthManager struct {
	mu        sync.Mutex
	refreshMu sync.Mutex
	token     *tokenData
	// epoch identifies the current session. Login and logout bump it, so a
	// refresh that started under the previous session cannot write its
	// result back after the user already logged out (or logged in again).
	epoch      uint64
	apiBase    string
	http       *http.Client
	onChange   func(error)
	pending    *pendingLogin
	lastVerify string
	// proxy 只在登录链路用来把「当前生效的代理线路」写进网络错误提示，
	// 让用户一眼看出令牌请求走的是哪条线、该去设置里改成直连还是放行。
	proxy *proxyManager
	// key 是这个会话在本机存储里的账号键（见 accounts.go）。空键是旧版单
	// 账号会话的键；添加账号时先用空键，拿到用户名后才换成正式账号 id。
	key string
	// store persists the session. Production uses the OS credential store
	// (mygo.Secrets); tests inject a file-backed store via tokenStoreFactory.
	store tokenStore
	// requestedScopes 是这次令牌实际申请过的 scope（mergeScopes 的结果）。
	// 授权页按它列出要申请的权限；申没申请到才是下次缺权限时该补什么。重新
	// 登录时用账号已有的令牌 scope 初始化，窗口里就能核对这次补到了哪些。
	requestedScopes string
	// knownScopes 是最近一次令牌携带的 scope。令牌被服务端拒绝清掉之后仍然
	// 留着（账号索引也会存一份）：2Libra 的重新授权是替换而不是累加，重新
	// 登录时必须把以前按需开通的 scope 一起申请，否则就等于撤掉它们。
	knownScopes []string
}

func newAuthManager(dir string) *AuthManager {
	if dir == "" {
		dir = configDir()
	}
	return newAuthManagerAt(dir, newProxyManager(dir))
}

// newAuthManagerAt 复用调用方已经建好的代理管理器，保证 OAuth 与 API 请求
// 走同一份代理配置和同一个连接池。
func newAuthManagerAt(dir string, proxy *proxyManager) *AuthManager {
	return newAuthManagerFor(dir, proxy, legacyTokenKey)
}

// newAuthManagerFor 建一个账号的会话：令牌从它自己的凭据里读，存储键是
// 账号 id（见 accounts.go）。
func newAuthManagerFor(dir string, proxy *proxyManager, key string) *AuthManager {
	if dir == "" {
		dir = configDir()
	}
	m := &AuthManager{
		apiBase: apiBaseURL,
		// 令牌交换在打包后可能走代理/系统防火墙，读响应体比开发机慢。给
		// 30s 余量（原 20s），超时则经 wrapAuthError 说清走的哪条线路。
		http:  proxy.Client(30 * time.Second),
		proxy: proxy,
		key:   key,
		store: tokenStoreFactory(dir, key),
	}
	// client_id and scope are compile-time constants now. A settings.json
	// left behind by an older build is ignored entirely.
	if loaded, err := m.store.load(); err == nil && loaded != nil && loaded.AccessToken != "" {
		m.token = loaded
		// 重新授权时以账号已有的令牌权限为起点：授权地址要在这个基础上补缺的
		// scope，登录窗口随后用它核对这次补到了哪些。
		m.requestedScopes = loaded.Scope
		m.knownScopes = strings.Fields(loaded.Scope)
	}
	return m
}

// setKnownScopes 记下这个账号上一份授权范围（账号索引里存的），与当前已知
// 的合并：索引可能落后于刚拿到的令牌，也可能记着令牌里已经丢掉、但重新
// 授权时要恢复的按需权限。
func (m *AuthManager) setKnownScopes(scopes []string) {
	if len(scopes) == 0 {
		return
	}
	m.mu.Lock()
	m.knownScopes = mergeScopeList(m.knownScopes, scopes)
	m.mu.Unlock()
}

// knownScopesList 返回这个账号知道的授权范围：当前令牌的 scope 加上记住的
// 上一份。重新授权用它把以前开通的按需 scope 一起带上。
func (m *AuthManager) knownScopesList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if m.token != nil {
		out = strings.Fields(m.token.Scope)
	}
	out = append(out, m.knownScopes...)
	return cleanScopeList(out)
}

// errNoToken is what tokenStore.load returns when no session is saved yet.
var errNoToken = errors.New("no stored token")

// tokenStore persists one account's login session. The production
// implementation keeps the tokens in the OS credential store through
// mygo.Secrets (Windows Credential Manager / macOS Keychain / Linux Secret
// Service) so the refresh token is never written to disk in plaintext, as the
// 2Libra terms require. fileTokenStore exists for tests and for platforms
// without a credential store, where secureTokenStore transparently falls back
// to it.
//
// 多账号：一个账号一份凭据，键是账号在本机存储里的 id（见 accounts.go）。
// 旧版本只有一个账号、一份凭据，那个账号的键是空串，仍按旧名读写；升级后
// 第一次启动会把它迁移成正式账号（见 accountBook.migrateLegacySession）。
type tokenStore interface {
	load() (*tokenData, error) // errNoToken when nothing is saved
	save(t *tokenData) error
	delete() error
}

// legacyTokenKey 是旧版单账号会话的存储键：没有账号册的时代本机只有一个
// 登录态，令牌存在 session-token / token.json 里。空键让升级后的第一次
// 启动还能读到老会话，再迁移到它自己的账号键上。
const legacyTokenKey = ""

const (
	legacyTokenSecret = "session-token"
	tokenSecretPrefix = "session-token/"
)

// tokenSecretKey 是某个账号在 OS 凭据库里的条目名。旧键保持原名。
func tokenSecretKey(key string) string {
	if key == "" {
		return legacyTokenSecret
	}
	return tokenSecretPrefix + storageKey(key)
}

// tokenFilePath 是凭据库不可用时退回的明文文件，同样保留旧名 token.json。
func tokenFilePath(dir, key string) string {
	if key == "" {
		return filepath.Join(dir, "token.json")
	}
	return filepath.Join(dir, "token-"+storageKey(key)+".json")
}

// storageKey 把账号 id 变成可以做文件名、凭据键的片段：只保留字母、数字、
// 点、下划线与连字符，其余换成下划线。账号 id 由 accountIDFor 生成，本来
// 就合规；这里再兜一次，免得以后改 id 规则时把路径拼出配置目录。
func storageKey(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

// memoryTokenStore 只在内存里保存会话：添加账号时还不知道授权回来的会是
// 哪个账号，令牌先留在这里，拿到用户名、定了账号 id 之后才换进它自己的
// 凭据（见 accountBook.adoptLogin）。
type memoryTokenStore struct {
	mu sync.Mutex
	t  *tokenData
}

func (s *memoryTokenStore) load() (*tokenData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.t == nil {
		return nil, errNoToken
	}
	cp := *s.t
	return &cp, nil
}

func (s *memoryTokenStore) save(t *tokenData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *t
	s.t = &cp
	return nil
}

func (s *memoryTokenStore) delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.t = nil
	return nil
}

// tokenStoreFactory builds the persistent token store for one account. Tests
// override it (see auth_store_test.go) with a file-backed store so runs stay
// isolated and the token file assertions keep working; the default production
// store uses the OS credential store.
var tokenStoreFactory = func(dir, key string) tokenStore {
	return newSecureTokenStore(dir, key)
}

// fileTokenStore keeps the session as token.json, the pre-Secrets behaviour.
// It is the fallback when no credential store is available and the store used
// by tests. 多账号时每个账号是 token-<id>.json，旧账号仍是 token.json。
type fileTokenStore struct{ path string }

func newFileTokenStore(dir, key string) tokenStore {
	return fileTokenStore{path: tokenFilePath(dir, key)}
}

func (s fileTokenStore) load() (*tokenData, error) {
	var t tokenData
	if err := readJSONFile(s.path, &t); err != nil {
		if os.IsNotExist(err) {
			return nil, errNoToken
		}
		return nil, err
	}
	return &t, nil
}

func (s fileTokenStore) save(t *tokenData) error {
	return writeJSONFile(s.path, t)
}

func (s fileTokenStore) delete() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// secureTokenStore keeps one account's session in the OS credential store via
// mygo.Secrets. When the store is unavailable (errors.ErrUnsupported, e.g. a
// headless Linux without libsecret) or the serialized session exceeds the
// credential store's size cap (mygo.MaxSecretSize), it falls back to
// fileTokenStore so the app still persists the login; fileOnly stays set so
// later loads read the file instead of the (empty) credential store.
type secureTokenStore struct {
	dir      string
	key      string
	mu       sync.Mutex
	file     tokenStore
	fileOnly bool
}

func newSecureTokenStore(dir, key string) tokenStore {
	return &secureTokenStore{dir: dir, key: key}
}

func (s *secureTokenStore) fileStore() tokenStore {
	if s.file == nil {
		s.file = newFileTokenStore(s.dir, s.key)
	}
	return s.file
}

func (s *secureTokenStore) load() (*tokenData, error) {
	s.mu.Lock()
	fileOnly := s.fileOnly
	s.mu.Unlock()
	if fileOnly {
		return s.fileStore().load()
	}
	b, err := mygo.Secrets.Get(tokenSecretKey(s.key))
	if err != nil {
		if errors.Is(err, mygo.ErrSecretNotFound) {
			return nil, errNoToken
		}
		if errors.Is(err, errors.ErrUnsupported) {
			s.mu.Lock()
			s.fileOnly = true
			s.mu.Unlock()
			return s.fileStore().load()
		}
		return nil, err
	}
	var t tokenData
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *secureTokenStore) save(t *tokenData) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if len(b) > mygo.MaxSecretSize {
		// The credential store caps secret size; fall back to a plaintext
		// file so an unusually large session can still persist.
		fmt.Fprintf(os.Stderr, "token: 凭据超过 %d 字节上限，退回明文文件存储\n", mygo.MaxSecretSize)
		s.mu.Lock()
		s.fileOnly = true
		s.mu.Unlock()
		return s.fileStore().save(t)
	}
	if err := mygo.Secrets.Set(tokenSecretKey(s.key), b); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			fmt.Fprintln(os.Stderr, "token: OS 凭据库不可用，已退回明文文件存储")
			s.mu.Lock()
			s.fileOnly = true
			s.mu.Unlock()
			return s.fileStore().save(t)
		}
		return err
	}
	return nil
}

func (s *secureTokenStore) delete() error {
	s.mu.Lock()
	fileOnly := s.fileOnly
	s.mu.Unlock()
	if fileOnly {
		return s.fileStore().delete()
	}
	err := mygo.Secrets.Delete(tokenSecretKey(s.key))
	if err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
		if errors.Is(err, errors.ErrUnsupported) {
			s.mu.Lock()
			s.fileOnly = true
			s.mu.Unlock()
			return s.fileStore().delete()
		}
		return err
	}
	return nil
}

func (m *AuthManager) webBase() string { return strings.TrimSuffix(m.apiBase, "/api") }

func (m *AuthManager) accessToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return ""
	}
	return m.token.AccessToken
}

func (m *AuthManager) loggedIn() bool { return m.accessToken() != "" }

func (m *AuthManager) username() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return ""
	}
	return m.token.Username
}

// scopes 返回当前令牌携带的授权范围。「我的」窗口的「权限与账号」把这份
// 列表原样展示给用户：只显示服务端真的授予了什么，不拼一份期望中的清单。
func (m *AuthManager) scopes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return nil
	}
	return strings.Fields(m.token.Scope)
}

// requestedScopesList 返回这次授权地址里真正申请过的 scope。授权回来的令牌
// 里的 scope 可能少几项（用户拒绝、在站内授权页逐项关掉了某些权限），两者
// 一对比就知道这次授权有没有真的拿到要申请的权限。
func (m *AuthManager) requestedScopesList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Fields(m.requestedScopes)
}

// requestedScopePreview 是「点前往授权之后会申请哪些 scope」：默认范围加上
// extra，和 BeginScopes 拼授权地址用的是同一个 mergeScopes。授权弹窗据此
// 先告诉用户这次会申请什么，不必等浏览器打开才看见。
func (m *AuthManager) requestedScopePreview(extra []string) []string {
	return strings.Fields(mergeScopes(extra))
}

// scopeLabel 是 scope 在界面上的中文说明。profile.go 的「我的 · 权限与账号」
// 与登录窗口的待申请清单共用这一份，文案两边一致。
func scopeLabel(scope string) string {
	if label, ok := scopeLabels[scope]; ok {
		return label
	}
	return ""
}

// grantedAndMissing 对比这次申请过的 scope 与令牌里实际拿到的，返回已授予与
// 没拿到两部分（都保序）。用户没拿到的那些才是下次授权该再申请的东西。
func grantedAndMissing(requested, granted []string) (got, missing []string) {
	have := make(map[string]struct{}, len(granted))
	for _, s := range granted {
		have[s] = struct{}{}
	}
	for _, s := range requested {
		if _, ok := have[s]; ok {
			got = append(got, s)
			continue
		}
		missing = append(missing, s)
	}
	return got, missing
}
func (m *AuthManager) avatarURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return ""
	}
	return m.token.Avatar
}

// setAvatar 记下刚换好的头像地址并落盘。空串表示重置成默认头像。只改展示
// 资料，不动令牌与 epoch：这不是一次会话变化，不需要触发重新授权。
func (m *AuthManager) setAvatar(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil || m.token.Avatar == url {
		return
	}
	m.token.Avatar = url
	_ = m.store.save(m.token)
}

// Cancel drops the in-flight authorization when the user gives up on it, so a
// late browser redirect cannot sign the session back in.
func (m *AuthManager) Cancel() { m.cancelPending("已取消登录") }

// awaitingLogin 报告这个会话正在等一次授权回调。多账号时回调只带 code 与
// state，账号册靠它认出该把链接交给哪一个窗口。
func (m *AuthManager) awaitingLogin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending != nil
}

func (m *AuthManager) verifyStatus() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastVerify
}

// storeToken installs a new session. It bumps the session epoch so any
// in-flight refresh started under the old session drops its result instead
// of resurrecting a token the user just logged out of.
func (m *AuthManager) storeToken(t tokenData) error { return m.storeTokenForEpoch(t, 0) }

// storeTokenForEpoch stores t only while the session it was fetched for is
// still current (epoch 0 means "always store"). Both the in-memory state and
// token.json change under the same lock, so a concurrent logout cannot be
// undone by a slower refresh: whichever ran first wins.
func (m *AuthManager) storeTokenForEpoch(t tokenData, epoch uint64) error {
	if t.ObtainedAt == 0 {
		t.ObtainedAt = time.Now().Unix()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if epoch != 0 && epoch != m.epoch {
		return errSessionChanged
	}
	if m.token != nil && t.Username == "" {
		t.Username = m.token.Username
	}
	// 刷新令牌不会带回账号资料：官方图床权限（Pro / 守护者）沿用上一次
	// 读到的结果，免得每次刷新后官方图床又变回「还没确认」。
	if m.token != nil && !t.ProKnown {
		t.IsPro, t.ProKnown, t.Role = m.token.IsPro, m.token.ProKnown, m.token.Role
	}
	if scopes := strings.Fields(t.Scope); len(scopes) > 0 {
		m.knownScopes = mergeScopeList(m.knownScopes, scopes)
	}
	m.epoch++
	m.token = &t
	return m.store.save(&t)
}

// proStatus 返回缓存的 Pro 状态与「是否已确认」。没登录时两者都是零值。
// 只表示 is_pro，不含守护者角色；判断能否用官方图床请用 officialHostAccess。
func (m *AuthManager) proStatus() (pro, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return false, false
	}
	return m.token.IsPro, m.token.ProKnown
}

// officialHostAccess 返回缓存的账号能否用官方图床，以及是否已确认。Pro 会员
// 与守护者（role=guardian）都有权限，任一命中即可。没登录时两者都是零值。
func (m *AuthManager) officialHostAccess() (allowed, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return false, false
	}
	return m.token.IsPro || m.token.Role == roleGuardian, m.token.ProKnown
}

// setOfficialHostAccess 记下刚读到的 is_pro 与 role 并落盘。role 为空（例如
// 公开资料没带这个字段）时沿用已有角色。须在后台调用：主线程等待凭据库时
// 会处理其他 UI 回调，那些回调也需要读取会话。
func (m *AuthManager) setOfficialHostAccess(pro bool, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return
	}
	if role == "" {
		role = m.token.Role
	}
	if m.token.ProKnown && m.token.IsPro == pro && m.token.Role == role {
		return
	}
	m.token.IsPro, m.token.ProKnown, m.token.Role = pro, true, role
	_ = m.store.save(m.token)
}

func (m *AuthManager) clearToken() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = nil
	m.lastVerify = ""
	m.epoch++
	return m.store.delete()
}

// rekey 把这个会话换到另一个账号的凭据上，并把当前令牌写过去。添加账号时
// 授权回来的账号在拿到用户名之前并不确定，令牌先放在内存里；确定之后才
// 换到它自己的存储键。
func (m *AuthManager) rekey(store tokenStore, key string) error {
	m.mu.Lock()
	m.store = store
	m.key = key
	var snapshot *tokenData
	if m.token != nil {
		cp := *m.token
		snapshot = &cp
	}
	m.mu.Unlock()
	if snapshot == nil {
		return nil
	}
	return store.save(snapshot)
}

// expiry 返回访问令牌的过期时间。令牌没记得到期信息时返回零值，调用方据此
// 跳过提前刷新。
func (m *AuthManager) expiry() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil || m.token.ExpiresIn <= 0 || m.token.ObtainedAt <= 0 {
		return time.Time{}
	}
	return time.Unix(m.token.ObtainedAt, 0).Add(time.Duration(m.token.ExpiresIn) * time.Second)
}

// refreshIfExpiring 在令牌 within 之内就要过期时提前刷新一次。多账号挂在
// 后台时靠它保持登录：窗口没开的账号也不会等到用户点开才开始换令牌。
// 令牌还早着、或根本没登录时直接返回。
func (m *AuthManager) refreshIfExpiring(ctx context.Context, within time.Duration) error {
	cur := m.accessToken()
	if cur == "" {
		return nil
	}
	if at := m.expiry(); at.IsZero() || time.Until(at) > within {
		return nil
	}
	if _, err := m.refresh(ctx, cur); err != nil {
		if errors.Is(err, errSessionChanged) {
			return nil // 期间换过账号：这次结果本来就不该用
		}
		return err
	}
	return nil
}

// detach 断开会话变化的回调。重新授权会换掉窗口的 AuthManager；旧会话迟到的
// 刷新失败 / 401 不能再把新会话的界面切回登录页。
func (m *AuthManager) detach() {
	m.setOnChange(nil)
}

// setOnChange 在锁内换会话变化回调：notify 也在同一把锁下取值，避免重新
// 绑定与后台刷新并发时读到写了一半的状态。
func (m *AuthManager) setOnChange(fn func(error)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *AuthManager) notify(err error) {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable here
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newPKCE() (verifier, challenge string) {
	verifier = randomURLSafe(48) // 64 chars, within 43~128 of [A-Za-z0-9-._~]
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:]) // 43 chars, S256
	return
}

// hasCallbackScheme reports whether raw uses the app's own URL scheme,
// whatever its path. It separates "the OS delivered something of ours that we
// cannot use" from "the OS delivered some other app's link", which is what
// decides whether a delivery is worth reporting to the user.
func hasCallbackScheme(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Scheme, appIdentifier)
}

// isCallbackLink accepts only the app's registered native callback URL.
// Loopback redirects, foreign schemes and any other path are rejected:
// there is exactly one login flow and one callback to validate.
func isCallbackLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, appIdentifier) {
		return false
	}
	// Rebuild the URL without query/fragment so only the registered
	// callback target (plus the harmless :// spelling variant) matches.
	base := u.Scheme + ":"
	if u.Opaque != "" {
		base += u.Opaque
	} else {
		if u.Host != "" {
			base += "//" + u.Host
		}
		base += u.Path
	}
	return base == defaultRedirectURI || base == alternateCallbackURI
}

func (m *AuthManager) authorizeURL(p *pendingLogin, scope string) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", defaultRedirectURI)
	v.Set("response_type", "code")
	v.Set("scope", scope)
	v.Set("state", p.state)
	v.Set("code_challenge", p.challenge)
	v.Set("code_challenge_method", "S256")
	return m.webBase() + "/oauth/authorize?" + v.Encode()
}

// embeddedAuthorizeURL 给内置浏览器的原生授权加 redirect_mode=app：自定义协议
// 回调直接 302，webview 能在导航发生前拦下链接并拿到 code，不必停在落地页等
// 用户再点一次「打开应用」（oauth.md 第四节 ④）。系统浏览器不能用它 —— 没有
// 用户手势时浏览器会拒绝唤起外部协议，页面会停在「处理中」。
func embeddedAuthorizeURL(raw string) string {
	return withRedirectMode(raw, "app")
}

// systemAuthorizeURL 去掉 redirect_mode，回到系统浏览器要的落地页默认行为。
func systemAuthorizeURL(raw string) string {
	return withRedirectMode(raw, "")
}

func withRedirectMode(raw, mode string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if mode == "" {
		q.Del("redirect_mode")
	} else {
		q.Set("redirect_mode", mode)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// mergeScopes unions the default scopes with the ones a failed call asked for,
// keeping first-seen order. A server 403 can name a scope this build never
// requested (a revoked `write:comment`, a newer endpoint's scope); the next
// authorization must ask for it explicitly or the browser page cannot grant it.
func mergeScopes(extra []string) string {
	seen := make(map[string]struct{}, len(extra)+len(defaultScopesList)+4)
	merged := make([]string, 0, len(extra)+len(defaultScopesList)+4)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		merged = append(merged, s)
	}
	for _, s := range defaultScopesList {
		add(s)
	}
	for _, s := range extra {
		add(s)
	}
	return strings.Join(merged, " ")
}

// Begin creates the PKCE pair and returns the authorize URL. Nothing is
// listened on locally: the OS delivers the callback URL through the
// registered scheme. A goroutine waits for the callback, exchanges the
// code and reports the outcome via onChange.
func (m *AuthManager) Begin(ctx context.Context) (string, error) {
	return m.BeginScopes(ctx, nil)
}

// BeginScopes is Begin with additional scopes on top of defaultScopes. The
// global authorization prompt uses it to ask for exactly the range the refused
// call needs.
func (m *AuthManager) BeginScopes(ctx context.Context, extraScopes []string) (string, error) {
	// A second click on 「登录」 supersedes the previous attempt: its PKCE
	// pair is thrown away so a late callback cannot complete a flow the
	// user already replaced.
	m.cancelPending("上一次登录已取消")

	verifier, challenge := newPKCE()
	p := &pendingLogin{
		state:     randomURLSafe(16),
		verifier:  verifier,
		challenge: challenge,
		result:    make(chan loginResult, 1),
	}

	// 以这个会话已经拿到的 scope 为起点：2Libra 的重新授权是替换而不是
	// 累加，重新登录时只申请默认范围就等于把以前按需开通的 write:topic /
	// write:economy / upload 全撤掉。extraScopes 里再叠这次缺的。
	previous := m.knownScopesList()
	m.mu.Lock()
	m.pending = p
	m.requestedScopes = mergeScopes(mergeScopeList(previous, extraScopes))
	m.mu.Unlock()
	go m.awaitLogin(ctx, p)
	return m.authorizeURL(p, m.requestedScopes), nil
}

// cancelPending drops the in-flight authorization, if any. Its goroutine
// receives an error and, because it is no longer the current attempt, stays
// silent: whatever cancelled it (logout, a new attempt) reports the state.
func (m *AuthManager) cancelPending(reason string) {
	m.mu.Lock()
	p := m.pending
	m.pending = nil
	m.mu.Unlock()
	if p != nil {
		p.deliver(loginResult{err: errors.New(reason)})
	}
}

func (m *AuthManager) awaitLogin(ctx context.Context, p *pendingLogin) {
	defer m.clearPending(p)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	var err error
	select {
	case r := <-p.result:
		err = m.finishLogin(ctx, p, r)
	case <-timer.C:
		err = errors.New("授权超时（5 分钟），请重新发起登录")
	case <-ctx.Done():
		err = ctx.Err()
	}
	// Only the attempt that is still current may report. A superseded or
	// cancelled attempt must not overwrite the message of the one that
	// replaced it (or of the logout that cancelled it).
	if m.isCurrent(p) {
		m.notify(err)
	}
}

func (m *AuthManager) isCurrent(p *pendingLogin) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending == p
}

func (m *AuthManager) clearPending(p *pendingLogin) {
	m.mu.Lock()
	if m.pending == p {
		m.pending = nil
	}
	m.mu.Unlock()
}

// Complete accepts a callback URL from the system deep link
// (com.vhxubo.t2-desktop:/callback?…) or the same URL pasted by the user
// when the browser did not jump back automatically. It is one flow: the
// pasted link must be the native callback, not a token or a loopback URL.
func (m *AuthManager) Complete(rawURL string) error {
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil {
		return errors.New("请先点击「打开浏览器授权」，再粘贴回调链接")
	}
	raw := strings.TrimSpace(rawURL)
	u, err := url.Parse(raw)
	if err != nil || !isCallbackLink(raw) {
		return fmt.Errorf("这不是本应用的回调链接，应形如 %s?code=...&state=...", defaultRedirectURI)
	}
	q := u.Query()
	if q.Get("state") != p.state {
		return errors.New("回调链接与当前授权不匹配，请使用这次授权的链接")
	}
	if e := q.Get("error"); e != "" {
		p.deliver(loginResult{err: fmt.Errorf("授权被拒绝：%s", e)})
		return nil
	}
	if q.Get("code") == "" {
		return errors.New("回调链接里没有 code 参数")
	}
	p.deliver(loginResult{code: q.Get("code"), state: q.Get("state")})
	return nil
}

func (m *AuthManager) finishLogin(ctx context.Context, p *pendingLogin, r loginResult) error {
	if r.err != nil {
		return r.err
	}
	if r.state != p.state {
		return errors.New("state 校验失败，已拒绝该回调（防 CSRF）")
	}
	t, err := m.exchangeCode(ctx, p, r.code)
	if err != nil {
		return err
	}
	// 令牌端点理论上会回 scope；个别情况下没回时按这次实际申请的清单记，
	// 免得刚补到的权限被当成没授予。
	if t.Scope == "" {
		t.Scope = strings.Join(m.requestedScopesList(), " ")
	}
	if err := m.storeToken(t); err != nil {
		return fmt.Errorf("令牌已获取但保存失败：%w", err)
	}
	requested := m.requestedScopesList()
	granted := strings.Fields(t.Scope)
	if len(granted) == 0 {
		// 令牌端点没回 scope 时无法判断授予了什么：不要凭空报「全部未授予」。
		log.Printf("授权完成，令牌响应没有 scope 信息（申请：%s）", strings.Join(requested, " "))
	} else if _, missing := grantedAndMissing(requested, granted); len(missing) > 0 {
		// 申请了但没授予：授权页可能没列出它（应用未开通 / 审核范围不含），
		// 也可能用户在站内「授权应用」页逐项关掉了。把差别写进日志，用户不用
		// 猜是请求没带上，还是服务端没给。
		log.Printf("授权完成，但以下 scope 未授予：%s（申请：%s；拿到：%s）",
			strings.Join(missing, " "), strings.Join(requested, " "), strings.Join(granted, " "))
	} else {
		log.Printf("授权完成，服务端授予 scope：%s", strings.Join(granted, " "))
	}
	m.fetchIdentity(ctx, t.AccessToken)
	_ = m.verify(ctx, t.AccessToken)
	return nil
}

func (m *AuthManager) postForm(ctx context.Context, form url.Values) (tokenData, error) {
	var empty tokenData
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.apiBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return empty, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return empty, m.wrapAuthError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// "Client Timeout ... while reading body" 是在这里读 Body 时触发的，
		// 不在上面的 Do() 里；这里也要包装，否则用户看到的就是那句含糊的
		// context deadline exceeded，正是打包后登录报错的现场。
		return empty, m.wrapAuthError(err)
	}
	// The token endpoint uses the same envelope; tokens live in d, and it
	// does not return a `code` field (that only exists under /api/v1/*).
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return empty, fmt.Errorf("令牌响应不是 {c,m,d,t} 信封：%w", err)
	}
	if resp.StatusCode >= 400 || env.C != 0 {
		msg := env.M
		if msg == "" {
			msg = "令牌请求失败"
		}
		return empty, &tokenHTTPError{
			status: resp.StatusCode,
			msg:    msg,
			// 4xx 与「HTTP 200 但信封 c != 0」都是授权层面的拒绝；
			// 5xx / 429 / 网络错误则可能只是暂时的。
			fatal: resp.StatusCode == http.StatusBadRequest ||
				resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusForbidden ||
				(resp.StatusCode == http.StatusOK && env.C != 0),
		}
	}
	var t tokenData
	if err := json.Unmarshal(env.D, &t); err != nil {
		return empty, err
	}
	if t.AccessToken == "" {
		return empty, errors.New("响应 d 里没有 access_token")
	}
	return t, nil
}

// wrapAuthError 把登录链路的网络错误说成人能看懂的原因，并附上当前生效的
// 代理线路。打包后首次登录若卡在「读取响应体超时」，多半是代理或系统防火墙
// 把到 2Libra 的出网连接拦住了：用户能在提示里直接看到走的是哪条线、该去
// 设置里改成直连还是放行应用，而不是对着一句含糊的 deadline 发懵。
func (m *AuthManager) wrapAuthError(err error) error {
	if err == nil {
		return nil
	}
	via := ""
	isDirect := false
	if m.proxy != nil {
		cfg, status := m.proxy.Snapshot()
		via = status
		isDirect = cfg.Mode == proxyDirect
	}
	var ne net.Error
	timedOut := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
	if timedOut {
		switch {
		case via == "":
			return fmt.Errorf("令牌请求超时：请检查网络，或在设置里把代理改为「直连」后重试（%w）", err)
		case isDirect:
			return fmt.Errorf("令牌请求超时：当前已是直连，请检查网络连通性，或确认系统防火墙放行了本应用出网（%w）", err)
		default:
			return fmt.Errorf("令牌请求超时：当前走 %q，请确认该代理能连通 2Libra，或改为「直连」后重试（%w）", via, err)
		}
	}
	if errors.As(err, &ne) {
		if via == "" || isDirect {
			return fmt.Errorf("令牌请求失败：%s。多为系统防火墙拦截了本应用出网，请放行后重试", err.Error())
		}
		return fmt.Errorf("令牌请求失败：%s（当前走 %q）", err.Error(), via)
	}
	return err
}

func (m *AuthManager) exchangeCode(ctx context.Context, p *pendingLogin, code string) (tokenData, error) {
	// Both client_id and redirect_uri are the same fixed constants used to
	// build the authorize URL.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", defaultRedirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", p.verifier) // 公开客户端：不带 client_secret
	return m.postForm(ctx, form)
}

func (m *AuthManager) Logout() {
	// 退出登录必须压过还在等待的回调：否则用户在浏览器里同意授权后，
	// 迟到的回调会把刚退出的会话又签回来。
	m.cancelPending("已退出登录，未完成的授权已取消")
	if err := m.clearToken(); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.notify(err)
		return
	}
	// 主动退出是清空账号：下次登录从默认范围重新开始，不把上一个账号按需
	// 开通的 scope 带进来。服务端拒绝（invalidate）则相反，要留着 scope 以便
	// 重新授权时恢复。
	m.mu.Lock()
	m.knownScopes = nil
	m.mu.Unlock()
	m.notify(nil)
}

// refresh is single-flight: concurrent 401s share one token rotation, and
// a caller holding an already-stale token simply gets the current one.
func (m *AuthManager) refresh(ctx context.Context, stale string) (string, error) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	// 复制一份快照：token 的字段会被 fetchIdentity 等协程在锁内改写，
	// 锁外只能读副本，否则与它们构成数据竞争。
	m.mu.Lock()
	var cur tokenData
	haveToken := m.token != nil
	if haveToken {
		cur = *m.token
	}
	epoch := m.epoch
	m.mu.Unlock()
	if !haveToken {
		return "", errors.New("未登录")
	}
	if cur.AccessToken != stale {
		return cur.AccessToken, nil // another request already rotated it
	}
	if cur.RefreshToken == "" {
		return "", errors.New("access_token 已过期且没有 refresh_token，请重新登录")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cur.RefreshToken)
	form.Set("client_id", clientID) // 公开客户端不带 client_secret
	t, err := m.postForm(ctx, form)
	if err != nil {
		return "", fmt.Errorf("刷新令牌失败：%w", err)
	}
	// Every refresh rotates refresh_token: replace it immediately.
	if t.RefreshToken == "" {
		t.RefreshToken = cur.RefreshToken
	}
	// 刷新令牌的响应偶尔不带 scope：沿用当前令牌的，别把权限清单洗成空。
	if t.Scope == "" {
		t.Scope = cur.Scope
	}
	// 刷新期间用户可能已经退出或重新登录：那就丢弃这次结果，绝不把旧
	// 会话写回 token.json。
	if err := m.storeTokenForEpoch(t, epoch); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// authFailure reports whether a failed token request means the login is
// really gone (invalid/expired grant, revoked authorization) rather than a
// temporary network or server problem. Only the former may clear the stored
// session: being logged out because the Wi-Fi blinked is a login bug.
func authFailure(err error) bool {
	var te *tokenHTTPError
	return errors.As(err, &te) && te.fatal
}

// invalidate drops the session and reports why, so the UI can send the user
// back to the login page with an actionable message. A login the user is
// already doing is left alone: it is how they get a new session.
func (m *AuthManager) invalidate(err error) {
	if clearErr := m.clearToken(); clearErr != nil && !errors.Is(clearErr, os.ErrNotExist) {
		err = fmt.Errorf("%w；清理令牌失败：%v", err, clearErr)
	}
	m.notify(err)
}

// invalidateFor 是「这次请求用的旧令牌被拒」的失效处理：只有当前会话仍是
// 那个旧令牌时才清掉。重新授权已经换上新令牌时，迟到的 401 / 刷新失败不能
// 把刚拿到的授权再清掉。
func (m *AuthManager) invalidateFor(stale string, err error) {
	m.mu.Lock()
	cur := ""
	if m.token != nil {
		cur = m.token.AccessToken
	}
	m.mu.Unlock()
	if cur == "" || (stale != "" && cur != stale) {
		return
	}
	m.invalidate(err)
}

// fetchIdentity reads GET /api/oauth/me (profile scope) for the display name
// and avatar, then falls back to GET /api/v1/me/summary.
func (m *AuthManager) fetchIdentity(ctx context.Context, access string) {
	for _, p := range []string{"/oauth/me", "/v1/me/summary"} {
		id, err := m.identityFrom(ctx, m.apiBase+p, access)
		if err != nil || (id.name == "" && id.avatar == "") {
			continue
		}
		// 只把身份写回它所属的那次登录：期间若已退出或换了账号，
		// 这里写回会把别人的令牌又落盘。写盘也在锁内，与 refresh 的
		// 令牌落盘串行，不会互相覆盖。
		m.mu.Lock()
		if m.token != nil && m.token.AccessToken == access {
			if id.name != "" {
				m.token.Username = id.name
			}
			if id.avatar != "" {
				m.token.Avatar = id.avatar
			}
			_ = m.store.save(m.token)
		}
		m.mu.Unlock()
		return
	}
}

// identity is what the profile endpoints say about the signed-in account.
type identity struct {
	name   string
	avatar string
}

func (m *AuthManager) identityFrom(ctx context.Context, u, access string) (identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return identity{}, err
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return identity{}, err
	}
	if resp.StatusCode >= 400 || env.C != 0 {
		return identity{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, env.Code)
	}
	var d any
	if err := json.Unmarshal(env.D, &d); err != nil {
		return identity{}, err
	}
	// /oauth/me puts the account at the top level, /v1/me/summary nests it
	// under user, and the avatar key is spelled both ways: search the whole
	// response breadth first instead of guessing one shape.
	return identity{
		name:   firstString(d, "username", "nickname", "name"),
		avatar: firstString(d, "avatar_url", "avatarUrl", "avatar"),
	}, nil
}

// firstString returns the first non-empty string among keys in v, breadth
// first, so a nested account object is found without knowing its exact shape.
func firstString(v any, keys ...string) string {
	queue := []any{v}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		switch t := cur.(type) {
		case map[string]any:
			for _, k := range keys {
				if s, ok := t[k].(string); ok && s != "" {
					return s
				}
			}
			for _, child := range t {
				if _, ok := child.(map[string]any); ok {
					queue = append(queue, child)
				}
			}
		case []any:
			for _, child := range t {
				if _, ok := child.(map[string]any); ok {
					queue = append(queue, child)
				}
			}
		}
	}
	return ""
}

// verify is the documented connectivity check: GET /api/v1/me/summary.
func (m *AuthManager) verify(ctx context.Context, access string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiBase+"/v1/me/summary", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		m.setVerify("连通性检查失败：" + err.Error())
		return err
	}
	defer resp.Body.Close()
	var env envelope
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env)
	if resp.StatusCode >= 400 || env.C != 0 {
		e := &apiError{Status: resp.StatusCode, Code: env.Code, Message: env.M}
		m.setVerify("GET /v1/me/summary：" + e.friendly())
		return e
	}
	m.setVerify("GET /v1/me/summary 连通正常")
	return nil
}

func (m *AuthManager) setVerify(s string) {
	m.mu.Lock()
	m.lastVerify = s
	m.mu.Unlock()
}
