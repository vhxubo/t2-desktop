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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// There is exactly one login flow: a native/desktop public client using a
// private-use URI scheme (RFC 8252 §7.1). The OS hands the callback URL to
// the app registered for appIdentifier; the app id doubles as client_id.
// redirect_uri is a fixed constant, not a user setting, so it can never
// drift from the value registered on the 2Libra website.
const (
	appIdentifier      = "com.vhxubo.t2-desktop"
	defaultRedirectURI = appIdentifier + ":/callback"
	defaultScopes      = "read profile read:me"

	// Some platforms normalise :/callback into ://callback before handing
	// the URL to the app. This is the same native callback, not a second
	// login method, so it is accepted as a spelling variant only.
	alternateCallbackURI = appIdentifier + "://callback"
)

// Settings holds the public-client OAuth configuration. A desktop app has
// no client_secret: PKCE code_verifier alone authenticates the token call.
// redirect_uri and a loopback port deliberately do not exist here.
type Settings struct {
	ClientID string `json:"client_id"`
	Scopes   string `json:"scopes"`
}

type tokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	ObtainedAt   int64  `json:"obtained_at"`
	Username     string `json:"username,omitempty"`
}

func defaultSettings() Settings {
	return Settings{
		ClientID: envOr("TWOLIBRA_CLIENT_ID", appIdentifier),
		Scopes:   envOr("TWOLIBRA_SCOPES", defaultScopes),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// configDir is where tokens and settings live. It stays outside the repo
// and can be redirected in tests with TWOLIBRA_CONFIG_DIR.
func configDir() string {
	if d := os.Getenv("TWOLIBRA_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "2libra-native-reader")
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

// pendingLogin lives in memory for the duration of one authorization. No
// listener, no server, no port: the OS delivers the callback URL through
// the registered URL scheme, and Complete() feeds it into result.
type pendingLogin struct {
	state     string
	verifier  string
	challenge string
	clientID  string
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
	mu         sync.Mutex
	refreshMu  sync.Mutex
	settings   Settings
	token      *tokenData
	dir        string
	apiBase    string
	http       *http.Client
	onChange   func(error)
	pending    *pendingLogin
	lastVerify string
}

func newAuthManager(dir string) *AuthManager {
	if dir == "" {
		dir = configDir()
	}
	m := &AuthManager{
		settings: defaultSettings(),
		dir:      dir,
		apiBase:  apiBaseURL,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
	// Legacy settings.json files may still contain "redirect_uri" and "port".
	// Those JSON keys are simply ignored now: there is no alternative flow to
	// migrate to, the callback is always defaultRedirectURI.
	var saved Settings
	if err := readJSONFile(filepath.Join(dir, "settings.json"), &saved); err == nil {
		if saved.ClientID != "" {
			m.settings.ClientID = saved.ClientID
		}
		if saved.Scopes != "" {
			m.settings.Scopes = saved.Scopes
		}
	}
	// Explicit environment values always win over the saved file.
	if v := os.Getenv("TWOLIBRA_CLIENT_ID"); v != "" {
		m.settings.ClientID = v
	}
	if v := os.Getenv("TWOLIBRA_SCOPES"); v != "" {
		m.settings.Scopes = v
	}
	var t tokenData
	if err := readJSONFile(filepath.Join(dir, "token.json"), &t); err == nil && t.AccessToken != "" {
		m.token = &t
	}
	return m
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

func (m *AuthManager) settingsSnapshot() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

func (m *AuthManager) verifyStatus() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastVerify
}

func (m *AuthManager) updateSettings(s Settings) error {
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.Scopes = strings.TrimSpace(s.Scopes)
	if s.ClientID == "" {
		return errors.New("client_id 不能为空")
	}
	if s.Scopes == "" {
		s.Scopes = defaultScopes
	}
	m.mu.Lock()
	m.settings = s
	dir := m.dir
	m.mu.Unlock()
	return writeJSONFile(filepath.Join(dir, "settings.json"), s)
}

func (m *AuthManager) storeToken(t tokenData) error {
	if t.ObtainedAt == 0 {
		t.ObtainedAt = time.Now().Unix()
	}
	m.mu.Lock()
	if m.token != nil && t.Username == "" {
		t.Username = m.token.Username
	}
	m.token = &t
	dir := m.dir
	m.mu.Unlock()
	return writeJSONFile(filepath.Join(dir, "token.json"), t)
}

func (m *AuthManager) clearToken() error {
	m.mu.Lock()
	m.token = nil
	m.lastVerify = ""
	dir := m.dir
	m.mu.Unlock()
	return os.Remove(filepath.Join(dir, "token.json"))
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

func (m *AuthManager) authorizeURL(p *pendingLogin, s Settings) string {
	v := url.Values{}
	v.Set("client_id", s.ClientID)
	v.Set("redirect_uri", defaultRedirectURI)
	v.Set("response_type", "code")
	v.Set("scope", s.Scopes)
	v.Set("state", p.state)
	v.Set("code_challenge", p.challenge)
	v.Set("code_challenge_method", "S256")
	return m.webBase() + "/oauth/authorize?" + v.Encode()
}

// Begin creates the PKCE pair and returns the authorize URL. Nothing is
// listened on locally: the OS delivers the callback URL through the
// registered scheme. A goroutine waits for the callback, exchanges the
// code and reports the outcome via onChange.
func (m *AuthManager) Begin(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.pending != nil {
		old := m.pending
		m.pending = nil
		old.deliver(loginResult{err: errors.New("上一次登录已取消")})
	}
	s := m.settings
	m.mu.Unlock()

	if s.ClientID == "" {
		return "", errors.New("请先在「设置」里填写 client_id（2Libra 网页申请的原生/桌面端应用 ID）")
	}
	verifier, challenge := newPKCE()
	p := &pendingLogin{
		state:     randomURLSafe(16),
		verifier:  verifier,
		challenge: challenge,
		clientID:  s.ClientID,
		result:    make(chan loginResult, 1),
	}

	m.mu.Lock()
	m.pending = p
	m.mu.Unlock()
	go m.awaitLogin(ctx, p)
	return m.authorizeURL(p, s), nil
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
	m.notify(err)
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
	if err := m.storeToken(t); err != nil {
		return fmt.Errorf("令牌已获取但保存失败：%w", err)
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
		return empty, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return empty, err
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
		return empty, fmt.Errorf("HTTP %d：%s", resp.StatusCode, msg)
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

func (m *AuthManager) exchangeCode(ctx context.Context, p *pendingLogin, code string) (tokenData, error) {
	// Use the exact client_id snapshot from the authorize step; redirect_uri
	// is the same fixed constant used to build that URL.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", defaultRedirectURI)
	form.Set("client_id", p.clientID)
	form.Set("code_verifier", p.verifier) // 公开客户端：不带 client_secret
	return m.postForm(ctx, form)
}

func (m *AuthManager) Logout() {
	if err := m.clearToken(); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.notify(err)
		return
	}
	m.notify(nil)
}

// refresh is single-flight: concurrent 401s share one token rotation, and
// a caller holding an already-stale token simply gets the current one.
func (m *AuthManager) refresh(ctx context.Context, stale string) (string, error) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	cur := m.token
	clientID := m.settings.ClientID
	m.mu.Unlock()
	if cur == nil {
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
	if err := m.storeToken(t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

func (m *AuthManager) invalidate(err error) {
	if clearErr := m.clearToken(); clearErr != nil && !errors.Is(clearErr, os.ErrNotExist) {
		err = fmt.Errorf("%w；清理令牌失败：%v", err, clearErr)
	}
	m.notify(err)
}

// fetchIdentity reads GET /api/oauth/me (profile scope) for the display
// name, then falls back to GET /api/v1/me/summary.
func (m *AuthManager) fetchIdentity(ctx context.Context, access string) {
	for _, p := range []string{"/oauth/me", "/v1/me/summary"} {
		name, err := m.identityFrom(ctx, m.apiBase+p, access)
		if err == nil && name != "" {
			m.mu.Lock()
			if m.token != nil {
				m.token.Username = name
				t := *m.token
				dir := m.dir
				m.mu.Unlock()
				_ = writeJSONFile(filepath.Join(dir, "token.json"), t)
				return
			}
			m.mu.Unlock()
			return
		}
	}
}

func (m *AuthManager) identityFrom(ctx context.Context, u, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 || env.C != 0 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, env.Code)
	}
	var d struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		User     struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(env.D, &d); err != nil {
		return "", err
	}
	if d.Username != "" {
		return d.Username, nil
	}
	if d.User.Username != "" {
		return d.User.Username, nil
	}
	return d.Name, nil
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
