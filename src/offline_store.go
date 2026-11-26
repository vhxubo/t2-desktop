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

// localKV 是应用的本地键值缓存，落在配置目录下的 cache/ 里。
//
// 这里的数据都不大：金币页的余额与近 30 天曲线、阅读记录、帖子已读
// 状态，以及每篇帖子的正文和第一页评论。用 JSON 快照而不是 SQLite：
// 没有额外依赖、不引入数据库文件与迁移，读取是普通的 os.ReadFile，写回
// 先写 .tmp 再改名，进程中途退出也不会留下半份缓存。数据量增长到需要
// 索引或分页时再换存储。
//
// 键按用途分组（coin: / reading: / read: / detail:），值仍然是 JSON。每组
// 单独一个文件（cache/detail.json、cache/read.json …），写回只序列化并写
// 当前这一组：打开帖子写正文缓存时不再把金币、历史与评论一起整份重写，
// 界面线程不会被一个越来越大的 offline.json 拖慢。旧版单文件 offline.json
// 在第一次启动时自动拆到各分片，随后改名成 .migrated 留底。
// 键的读写都在锁内完成：无窗口测试里 publish 可能在多个网络协程里直接执行。
const localKVVersion = 1

// localKVLegacyName 是旧版单文件缓存的名字；它只在迁移时读取一次。
const localKVLegacyName = "offline.json"

type localKVEntry struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Value     json.RawMessage `json:"value"`
}

type localKVFile struct {
	Version int                     `json:"version"`
	Entries map[string]localKVEntry `json:"entries"`
}

type localKV struct {
	// dir 是 cache 目录，每组一个 <组名>.json；path 是旧版单文件的位置。
	dir  string
	path string

	mu      sync.Mutex
	entries map[string]localKVEntry
	loaded  bool
}

func newLocalKV(path string) *localKV {
	return &localKV{
		dir:     filepath.Dir(path),
		path:    path,
		entries: make(map[string]localKVEntry),
	}
}

// newOfflineKV 把离线缓存定在配置目录下的 cache/offline.json，与图片缓存
// （cache/images）同一处；测试通过 newApp 的临时目录隔离。
func newOfflineKV(dir string) *localKV {
	if dir == "" {
		dir = configDir()
	}
	return newLocalKV(filepath.Join(dir, "cache", localKVLegacyName))
}

// logCacheWriteError 统一缓存写盘失败的日志：缓存是加速手段，写不进去
// 不应该影响界面，但日志里要留下原因。
func logCacheWriteError(err error) {
	if err != nil {
		log.Printf("离线缓存写入失败：%v", err)
	}
}

// localKVShard 是一个键所属的文件组：冒号前的用途前缀。没有冒号的独立键
// （节点快捷入口、图库配置等）各成一组，文件名就是键名。
func localKVShard(key string) string {
	if i := strings.IndexByte(key, ':'); i > 0 {
		return key[:i]
	}
	return key
}

// localKVShardPath 把组名变成 cache 目录下的文件名。组名只来自代码里的
// 常量，仍然过一遍白名单，避免任何拼写问题把文件写到目录之外。
func localKVShardPath(dir, shard string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, shard)
	if safe == "" {
		safe = "misc"
	}
	return filepath.Join(dir, safe+".json")
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
	if err := s.loadShardsLocked(); err != nil {
		return err
	}
	return s.migrateLegacyLocked()
}

// loadShardsLocked 把所有分组文件合并进内存。损坏的一组另存为 .bad 后跳过，
// 不影响其它组。
func (s *localKV) loadShardsLocked() error {
	items, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		name := item.Name()
		if !strings.HasSuffix(name, ".json") || name == localKVLegacyName {
			continue
		}
		path := filepath.Join(s.dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("离线缓存分组 %s 读取失败：%v", name, err)
			continue
		}
		var f localKVFile
		if err := json.Unmarshal(b, &f); err != nil {
			saveBadCacheFile(path, err)
			continue
		}
		for key, entry := range f.Entries {
			s.entries[key] = entry
		}
	}
	return nil
}

// migrateLegacyLocked 把旧版单文件 offline.json 按组拆开。迁移失败时保留原
// 文件，下次启动再试；内存里的数据已经可用。
func (s *localKV) migrateLegacyLocked() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f localKVFile
	if err := json.Unmarshal(b, &f); err != nil {
		saveBadCacheFile(s.path, err)
		return nil
	}
	shards := make(map[string]bool)
	for key, entry := range f.Entries {
		// 分组文件里已有的键更新（上次迁移写了一半就中断时，别让旧值盖回去）。
		if _, exists := s.entries[key]; exists {
			continue
		}
		s.entries[key] = entry
		shards[localKVShard(key)] = true
	}
	for shard := range shards {
		if err := s.saveShardLocked(shard); err != nil {
			return err
		}
	}
	if err := os.Rename(s.path, s.path+".migrated"); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("旧版离线缓存改名失败：%v", err)
	}
	return nil
}

func saveBadCacheFile(path string, err error) {
	bad := path + ".bad"
	if renameErr := os.Rename(path, bad); renameErr == nil {
		log.Printf("离线缓存损坏，已另存为 %s：%v", bad, err)
	} else {
		log.Printf("离线缓存损坏，且无法另存（%v）：%v", renameErr, err)
	}
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

// put 写一个键并立即落盘。只重写这个键所属的分组文件，不会把其它组的
// 金币、历史与评论一起序列化。写盘失败会返回错误，但内存里的值仍可用。
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
	return s.saveShardLocked(localKVShard(key))
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
	return s.saveShardLocked(localKVShard(key))
}

// clearPrefixes 删除所有以任一前缀开头的键，返回删除的条目数。只重写受
// 影响的分组文件；设置窗口的「清空离线缓存」与退出账号的清理都走这里。
func (s *localKV) clearPrefixes(prefixes ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0, err
	}
	removed := 0
	shards := make(map[string]bool)
	for key := range s.entries {
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				delete(s.entries, key)
				shards[localKVShard(key)] = true
				removed++
				break
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}
	for shard := range shards {
		if err := s.saveShardLocked(shard); err != nil {
			return removed, err
		}
	}
	return removed, nil
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
	shards := make(map[string]bool)
	for _, k := range keys[keep:] {
		delete(s.entries, k.key)
		shards[localKVShard(k.key)] = true
	}
	for shard := range shards {
		logCacheWriteError(s.saveShardLocked(shard))
	}
}

// size 返回 cache 目录里全部分组文件的字节数。设置窗口只读这一项，不扫描
// 图片缓存目录。旧版 offline.json 在迁移前也算在内。
func (s *localKV) size() int64 {
	items, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
			continue
		}
		if info, err := item.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// saveShardLocked 只把这个组名下的条目序列化到自己的文件。组已经空了就
// 删掉文件，避免留下一份空快照。调用方必须持有 s.mu。
func (s *localKV) saveShardLocked(shard string) error {
	entries := make(map[string]localKVEntry)
	for key, entry := range s.entries {
		if localKVShard(key) == shard {
			entries[key] = entry
		}
	}
	path := localKVShardPath(s.dir, shard)
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeJSONFile(path, localKVFile{Version: localKVVersion, Entries: entries})
}
