package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 每个用途组单独一个文件：写详情只重写 detail.json，不会把金币、历史与
// 评论一起序列化；另一个实例从各自的分组文件读回来。
func TestLocalKVWritesEachShardSeparately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	path := filepath.Join(dir, localKVLegacyName)
	kv := newLocalKV(path)
	if err := kv.put(detailCachePrefix+"A", "正文", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := kv.put("coin:local", 12, time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"detail.json", "coin.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("没有写出分组文件 %s：%v", name, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("不应该再写旧版单文件 %s", path)
	}

	other := newLocalKV(path)
	var text string
	if _, ok := other.get(detailCachePrefix+"A", &text); !ok || text != "正文" {
		t.Fatalf("详情分组没有读回来：%q %v", text, ok)
	}
	var n int
	if _, ok := other.get("coin:local", &n); !ok || n != 12 {
		t.Fatalf("金币分组没有读回来：%d %v", n, ok)
	}
}

// 同一个键连续写入时最后一次生效；clearPrefixes 返回后文件里已经没有
// 被清掉的键。
func TestLocalKVClearsPrefixes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", localKVLegacyName)
	kv := newLocalKV(path)
	for i := 0; i < 20; i++ {
		if err := kv.put("coin:local", i, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	other := newLocalKV(path)
	var n int
	if _, ok := other.get("coin:local", &n); !ok || n != 19 {
		t.Fatalf("连续写入应以最后一次为准：%d %v", n, ok)
	}

	removed, err := kv.clearPrefixes("coin:")
	if err != nil || removed != 1 {
		t.Fatalf("清理结果 = %d / %v", removed, err)
	}
	other = newLocalKV(path)
	if _, ok := other.get("coin:local", &n); ok {
		t.Error("clearPrefixes 返回后文件里不该还有这个键")
	}
}

// 旧版单文件 offline.json 在第一次启动时拆进各分组文件，原文件改名留底。
func TestOfflineKVMigratesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	legacy := filepath.Join(cacheDir, localKVLegacyName)
	raw, err := json.Marshal(localKVFile{Version: localKVVersion, Entries: map[string]localKVEntry{
		"coin:local": {UpdatedAt: time.Now(), Value: json.RawMessage(`28`)},
		"read:local": {UpdatedAt: time.Now(), Value: json.RawMessage(`{"a":true}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	kv := newOfflineKV(dir)
	var n int
	if _, ok := kv.get("coin:local", &n); !ok || n != 28 {
		t.Fatalf("迁移后的金币缓存读不到：%d %v", n, ok)
	}
	for _, name := range []string{"coin.json", "read.json"} {
		if _, err := os.Stat(filepath.Join(cacheDir, name)); err != nil {
			t.Errorf("迁移后没有生成 %s：%v", name, err)
		}
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("旧版文件没有改名留底：%v", err)
	}
}
