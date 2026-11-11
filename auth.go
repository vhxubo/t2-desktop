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
// the app registered for appIdentifier. client_id is the value 2Libra issued
// for this application; it is a compile-time constant, not a user setting.
// redirect_uri is fixed too, so it can never drift from the value registered
// on the 2Libra website. A desktop public client has no client_secret:
// PKCE code_verifier alone authenticates the token call.
const (
	appIdentifier      = "com.vhxubo.t2-desktop"
	defaultRedirectURI = appIdentifier + ":/callback"
	clientID           = "e471281af4d84ecfae77c39f94bcf372"
	defaultScopes      = "read profile read:me write:comment"

	// Some platforms normalise :/callback into ://callback before handing
	// the URL to the app. This is the same native callback, not a second
	// login method, so it is accepted as a spelling variant only.
	alternateCallbackURI = appIdentifier + "://callback"
)

type tokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	ObtainedAt   int64  `json:"obtained_at"`
	Username     string `json:"username,omitempty"`
}

// configDir is where the token lives. It stays outside the repo and can be
// redirected in tests with TWOLIBRA_CONFIG_DIR.
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
		dir:     dir,
		apiBase: apiBaseURL,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
	// client_id and scope are compile-time constants now. A settings.json
	// left behind by an older build is ignored entirely.
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
	m.epoch++
	m.token = &t
	return writeJSONFile(filepath.Join(m.dir, "token.json"), t)
}

func (m *AuthManager) clearToken() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = nil
	m.lastVerify = ""
	m.epoch++
	return os.Remove(filepath.Join(m.dir, "token.json"))
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

func (m *AuthManager) authorizeURL(p *pendingLogin) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", defaultRedirectURI)
	v.Set("response_type", "code")
	v.Set("scope", defaultScopes)
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

	m.mu.Lock()
	m.pending = p
	m.mu.Unlock()
	go m.awaitLogin(ctx, p)
	return m.authorizeURL(p), nil
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

// fetchIdentity reads GET /api/oauth/me (profile scope) for the display
// name, then falls back to GET /api/v1/me/summary.
func (m *AuthManager) fetchIdentity(ctx context.Context, access string) {
	for _, p := range []string{"/oauth/me", "/v1/me/summary"} {
		name, err := m.identityFrom(ctx, m.apiBase+p, access)
		if err == nil && name != "" {
			// 只把姓名写回它所属的那次登录：期间若已退出或换了账号，
			// 这里写回会把别人的令牌又落盘。写盘也在锁内，与 refresh 的
			// 令牌落盘串行，不会互相覆盖。
			m.mu.Lock()
			if m.token != nil && m.token.AccessToken == access {
				m.token.Username = name
				_ = writeJSONFile(filepath.Join(m.dir, "token.json"), *m.token)
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
