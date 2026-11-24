package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
)

const firstLoginTestUsername = "first-login-test"

func TestMain(tests *testing.M) {
	if dir := os.Getenv("T2_TEST_NATIVE_FIRST_LOGIN"); dir != "" {
		if err := testNativeFirstLogin(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(tests.Run())
}

func TestNativeFirstLogin(t *testing.T) {
	if os.Getenv("T2_TEST_WINDOWS_UI") == "" {
		t.Skip("设置 T2_TEST_WINDOWS_UI=1 在交互式 Windows 桌面运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	t.Cleanup(func() {
		secrets := mygo.Secrets.Service(firstLoginTestAppName(dir))
		if err := secrets.Delete(tokenSecretKey(accountIDFor(firstLoginTestUsername))); err != nil {
			t.Errorf("清理测试凭据失败：%v", err)
		}
	})
	command := exec.CommandContext(ctx, os.Args[0])
	command.Env = append(os.Environ(), "T2_TEST_NATIVE_FIRST_LOGIN="+dir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("首次授权后窗口未响应或启动失败：%v\n%s", err, output)
	}
}

func firstLoginTestAppName(dir string) string {
	return "t2-native-login-" + filepath.Base(filepath.Dir(dir))
}

type firstLoginTokenStore struct {
	tokenStore
	onProSave func()
}

func (store firstLoginTokenStore) save(token *tokenData) error {
	if token.ProKnown {
		store.onProSave()
	}
	return store.tokenStore.save(token)
}

func testNativeFirstLogin(dir string) error {
	mygo.App.SetName(firstLoginTestAppName(dir))
	mygo.App.SetPath(mygo.PathUserData, dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api") {
		case "/oauth/token":
			fmt.Fprint(w, `{"c":0,"d":{"access_token":"T","token_type":"Bearer","expires_in":3600}}`)
		case "/oauth/me", "/v1/me/summary":
			fmt.Fprintf(w, `{"c":0,"d":{"username":%q}}`, firstLoginTestUsername)
		case "/v1/users/" + firstLoginTestUsername:
			fmt.Fprintf(w, `{"c":0,"d":{"username":%q,"is_pro":true}}`, firstLoginTestUsername)
		default:
			fmt.Fprint(w, `{"c":0,"d":[]}`)
		}
	}))
	defer server.Close()

	var onProSave func()
	tokenStoreFactory = func(dir, key string) tokenStore {
		if key == legacyTokenKey {
			return &memoryTokenStore{}
		}
		return firstLoginTokenStore{newSecureTokenStore(dir, key), func() { onProSave() }}
	}
	book := newAccountBook(dir)
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		return err
	}
	transport := book.proxy.Transport()
	transport.RegisterProtocol("https", firstLoginTransport{base: proxyURL, next: server.Client().Transport})
	book.openURL = func(raw string) error {
		authorize, err := url.Parse(raw)
		if err != nil {
			return err
		}
		callback := defaultRedirectURI + "?code=fixture&state=" + authorize.Query().Get("state")
		book.guest.win.Update(func() { book.handleDeepLink(callback) })
		return nil
	}

	var uiThread atomic.Uint32
	var completed atomic.Bool
	result := make(chan error, 1)
	mygo.App.OnWindowCreated(func(win *mygo.Window) {
		acc := book.accountByUsername(firstLoginTestUsername)
		if acc == nil || acc.app == nil {
			return
		}
		onProSave = func() {
			if windows.GetCurrentThreadId() == uiThread.Load() {
				select {
				case result <- fmt.Errorf("首次授权后在 UI 线程写入 Pro 状态"):
				default:
				}
				return
			}
			win.Update(func() {
				allowed, known := acc.auth.officialHostAccess()
				if allowed && known && win.IsVisible() && book.guest == nil {
					completed.Store(true)
					mygo.App.Quit()
				}
			})
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { book.markQuitting() })
	mygo.App.WhenReady(func() {
		uiThread.Store(windows.GetCurrentThreadId())
		book.mainLoop = true
		book.restoreWindows()
		time.AfterFunc(time.Second, func() {
			mygo.RunOnMain(func() { book.startAddAccount() })
		})
	})
	time.AfterFunc(25*time.Second, func() {
		mygo.App.Quit()
	})
	if err := mygo.App.Run(); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	default:
	}
	if !completed.Load() {
		return fmt.Errorf("首次授权后账号窗口没有完成会话读取")
	}
	return nil
}

type firstLoginTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (transport firstLoginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.URL.Scheme = transport.base.Scheme
	copy.URL.Host = transport.base.Host
	return transport.next.RoundTrip(copy)
}
