package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// localKV 是应用的本地键值缓存，落在配置目录下的 cache/offline.json。
//
// 这里的数据都很小：金币页的余额与近 30 天曲线、阅读记录，以及每篇帖子
// 的正文和第一页评论。用一份 JSON 快照而不是 SQLite：没有额外依赖、
// 不引入数据库文件与迁移，读取是普通的 os.ReadFile，写回先写 .tmp 再改名，
// 进程中途退出也不会留下半份缓存。数据量增长到需要索引或分页时再换存储。
//
// 键按用途分组（coin: / reading: / detail:），值仍然是 JSON。载入与写回
// 都在锁内完成：无窗口测试里 publish 可能在多个网络协程里直接执行。
const localKVVersion = 1

type localKVEntry struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Value     json.RawMessage `json:"value"`
}

type localKVFile struct {
	Version int                     `json:"version"`
	Entries map[string]localKVEntry `json:"entries"`
}

type localKV struct {
	path string

	mu      sync.Mutex
	entries map[string]localKVEntry
	loaded  bool
}

func newLocalKV(path string) *localKV {
	return &localKV{path: path, entries: make(map[string]localKVEntry)}
}

// newOfflineKV 把离线缓存定在配置目录下的 cache/offline.json，与图片缓存
// （cache/images）同一处；测试通过 newApp 的临时目录隔离。
func newOfflineKV(dir string) *localKV {
	if dir == "" {
		dir = configDir()
	}
	return newLocalKV(filepath.Join(dir, "cache", "offline.json"))
}

// logCacheWriteError 统一缓存写盘失败的日志：缓存是加速手段，写不进去
// 不应该影响界面，但日志里要留下原因。
func logCacheWriteError(err error) {
	if err != nil {
		log.Printf("离线缓存写入失败：%v", err)
	}
}

// load 读一次磁盘。文件不存在是正常的空缓存；文件损坏时留一份 .bad 副本，
// 然后从空缓存继续，缓存问题不该让应用启动失败。
func (s *localKV) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *localKV) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.loaded = true
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f localKVFile
	if err := json.Unmarshal(b, &f); err != nil {
		bad := s.path + ".bad"
		if renameErr := os.Rename(s.path, bad); renameErr == nil {
			log.Printf("离线缓存损坏，已另存为 %s：%v", bad, err)
		} else {
			log.Printf("离线缓存损坏，且无法另存（%v）：%v", renameErr, err)
		}
		return nil
	}
	if f.Entries != nil {
		s.entries = f.Entries
	}
	return nil
}

// get 读一个键并解码到 dst。返回写入时间与是否命中；解码失败按未命中处理，
// 不让一条坏数据挡住其它缓存。
func (s *localKV) get(key string, dst any) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		log.Printf("离线缓存读取失败：%v", err)
		return time.Time{}, false
	}
	e, ok := s.entries[key]
	if !ok || len(e.Value) == 0 {
		return time.Time{}, false
	}
	if err := json.Unmarshal(e.Value, dst); err != nil {
		log.Printf("离线缓存条目 %s 解码失败：%v", key, err)
		return time.Time{}, false
	}
	return e.UpdatedAt, true
}

// has 只判断键是否存在，不关心值能否解成调用方的类型。
func (s *localKV) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return false
	}
	_, ok := s.entries[key]
	return ok
}

// put 写一个键并立即落盘。写盘失败会返回错误，但内存里的值仍可用。
func (s *localKV) put(key string, v any, at time.Time) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	s.entries[key] = localKVEntry{UpdatedAt: at, Value: raw}
	return s.saveLocked()
}

func (s *localKV) delete(key string) error {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	if _, ok := s.entries[key]; !ok {
		return nil
	}
	delete(s.entries, key)
	return s.saveLocked()
}

// clearPrefixes 删除所有以任一前缀开头的键，返回删除的条目数。一次写盘，
// 设置窗口的「清空离线缓存」只改一个文件。
func (s *localKV) clearPrefixes(prefixes ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0, err
	}
	removed := 0
	for key := range s.entries {
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				delete(s.entries, key)
				removed++
				break
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, s.saveLocked()
}

// countPrefix 返回某个分组的条目数，供设置窗口显示「缓存了多少篇正文」。
func (s *localKV) countPrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0
	}
	n := 0
	for key := range s.entries {
		if strings.HasPrefix(key, prefix) {
			n++
		}
	}
	return n
}

// prune 只保留某前缀下最新的 keep 条。阅读记录有上限，正文缓存也跟着
// 保留最近打开的那些，不会随着使用无限增长。
func (s *localKV) prune(prefix string, keep int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil || keep < 0 {
		return
	}
	type keyed struct {
		key string
		at  time.Time
	}
	var keys []keyed
	for key, e := range s.entries {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, keyed{key: key, at: e.UpdatedAt})
		}
	}
	if len(keys) <= keep {
		return
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].at.After(keys[j].at) })
	for _, k := range keys[keep:] {
		delete(s.entries, k.key)
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("离线缓存清理失败：%v", err)
	}
}

// size 返回缓存文件的字节数。设置窗口只读这一项，不扫描目录。
func (s *localKV) size() int64 {
	info, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *localKV) saveLocked() error {
	f := localKVFile{Version: localKVVersion, Entries: s.entries}
	return writeJSONFile(s.path, f)
}
