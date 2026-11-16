package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
)

// 测试里把令牌存储换成文件实现：既隔离各次运行（互不读到对方的凭据），
// 又保留既有 token.json 的断言（存在性、落盘内容）。生产默认走 OS 凭据库。
func init() {
	tokenStoreFactory = newFileTokenStore
}

// 生产路径用 mygo.Secrets：直接验证凭据库的读写与「不存在」语义；没有凭据库
// 的平台（如缺少 libsecret 的 Linux）跳过。
func TestSecretsRoundTrip(t *testing.T) {
	const k = "t2-desktop-test-roundtrip"
	defer func() { _ = mygo.Secrets.Delete(k) }()
	if err := mygo.Secrets.Set(k, []byte("hello")); err != nil {
		t.Skipf("OS 凭据库不可用，跳过：%v", err)
	}
	got, err := mygo.Secrets.Get(k)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("凭据读回不符：got %q", got)
	}
	if err := mygo.Secrets.Delete(k); err != nil {
		t.Fatal(err)
	}
	if _, err := mygo.Secrets.Get(k); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Errorf("删除后应返回 ErrSecretNotFound，实际 %v", err)
	}
}

// 凭据超过凭据库体积上限时，secureTokenStore 应退回文件存储且不丢数据。
func TestSecureTokenStoreFallsBackToFileWhenOversize(t *testing.T) {
	dir := t.TempDir()
	s := newSecureTokenStore(dir, legacyTokenKey)
	big := &tokenData{
		AccessToken: strings.Repeat("a", mygo.MaxSecretSize+100),
		TokenType:   "Bearer",
	}
	if err := s.save(big); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); err != nil {
		t.Fatalf("超限后应退回写 token.json：%v", err)
	}
	loaded, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != big.AccessToken {
		t.Errorf("退回文件后读回的令牌不符")
	}
	if err := s.delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Error("删除后 token.json 应消失")
	}
	if _, err := s.load(); !errors.Is(err, errNoToken) {
		t.Errorf("删除后 load 应返回 errNoToken，实际 %v", err)
	}
}
