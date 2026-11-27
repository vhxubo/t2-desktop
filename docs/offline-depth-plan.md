# 离线与弱网纵深方案

> 状态：已实施（列表首屏缓存、离线标注、评论扩展两页）。静默刷新复用既有实现，未新增刷新逻辑。
>
> 范围：信息流列表首屏缓存、离线/弱网读取与标注、评论缓存扩展到前两页。
> 不包含：通知快捷回复（另案）、动画 WebP、多账号。

---

## 一、定位与必要性

桌面客户端相对网页版最硬的差异化是「没网也能读、弱网不卡、回来不丢位置」。实施前详情页已经能本地缓存（`detail_cache.go`），但**信息流列表页完全没有缓存**：断网启动时首页、今日热议、新发表要么空列表、要么错误页。

这属于「只做了半条链路」：用户从列表进入详情有缓存，但停在列表本身时没有内容可看。补上列表缓存，是把「离线可用」从详情延伸到入口页，投入集中在数据层、复用现有离线态机，收益直接可见。

什么时候不该做：如果用户全程在线，收益主要是「启动与切页更快」而非「断网可用」。但对该产品定位而言，「断网也能读」本身就是卖点，值得做。

---

## 二、现状与本次落点

| 能力 | 实施前 | 本次落点 | 位置 |
| --- | --- | --- | --- |
| 详情正文 + 第一页评论缓存 | 已有 | 评论上限放宽到前两页（约 100 条） | `detail_cache.go` |
| 金币 / 阅读历史 / 稍后再看 / 已读 | 已有 | 不变 | `offline_cache.go` |
| 信息流列表缓存 | **缺失** | 新增 `feed:` 分组，只存首屏 | `feed_cache.go` |
| 离线态机 | 已有 | 复用，不新增状态机 | `netstatus.go` |
| 静默刷新与保滚动 | 已有 | 复用，仅在成功后写缓存 | `main.go` 的 `refreshTabSilently` / `mergeFreshPage` |
| 本地 KV | 拆文件、按前缀分组 | 新增 `feed` 分片，自然并入 | `offline_store.go` |

实施前的关键落差：

1. 列表页无缓存，断网时首屏没有任何内容。
2. `loadSpec` 第 1 页成功即 `ScrollTo(0, ui.Start)`，不能直接用于「不打断」的刷新；这一能力已由 `refreshTabSilently` / `mergeFreshPage` 提供。
3. 本地 KV 写入是**同步落盘**（`offline_store.go` 的 `put`），而 `a.publish` 在 UI 线程执行——缓存写入不能压在 UI 回调里。

---

## 三、实现说明

### 3.1 列表首屏缓存（`feed:` 分片）

沿用 KV 的「按前缀拆文件」机制，不引入数据库：

- 前缀：`feed:`，落成 `cache/feed.json` 一个分片。
- 键：`feed:<scope>:<path>?<query>`。
  - `scope`：`NeedsAuth` 且已登录时取小写账号名，公开列表用 `local`。
  - `path?query` 原样来自 `tabSpec`，首页的节点筛选已写进 Query，同一 tab 的不同筛选各自成键。
- 值：第 1 页的 `[]Item` + `Total`/`TotalPages` + `SavedAt`。只存首屏，续拉页不写入。
- 上界：单键 256 KB，超出跳过并记日志；最多保留 64 个 tab 的缓存（`prune`）。
- 通知不进这份缓存：它有自己的加载与静默合并链路。

### 3.2 离线/弱网读取与标注

- 进入 tab（`loadSpec` `page == 1`）且尚无内容时，先 `preloadFeedCache` 摆上缓存，再照常发请求。预载**不设置 `loaded`**，否则 `ensureLoaded` 会以为已经加载完成而不再刷新。
- 首屏失败且来自缓存、且属于网络问题（`errOffline` 或 `isNetworkError`）时，保留列表、不换成错误页，页头标注「网络不可用，显示 X 的本地缓存」（`feedHeader` 的 `feedCacheNote`）。
- 失败但无缓存时，行为与原先一致（错误页）。
- 复用 `netstatus.go` 的离线态与 `formatCacheTime`，不新增状态机。

### 3.3 静默刷新（复用既有实现）

`refreshTabSilently` / `silentRefreshTab` / `mergeFreshPage` 已经实现按 Key 合并、保页码与保滚动，`refresh_keep_scroll_test.go` 覆盖。本方案不新写刷新逻辑，只在 `mergeFreshPage` 成功后调用 `saveCachedFeed`，让缓存跟上最近一次成功刷新；失败路径保持原样，不打断用户。

### 3.4 评论缓存扩展到前两页

- `saveCachedDetail` 在 `replyPage <= 2` 时写当前累计评论，超过 100 条截断；`replyPage > 2` 时沿用缓存里已有的前两页。
- `preloadDetailCache` 已有逻辑直接摆出多页评论，`replyPage`/`replyTotal` 一并恢复。
- 付费正文仍不落盘（`cacheableTopic` 的剥离逻辑不变）。

### 3.5 约束

- **账号作用域**：`feed:` 键含 `scope` 段。`NeedsAuth` 的动态列表用账号名；公开列表用 `local`。退出登录时异步清理当前账号的 `feed:<name>:` 前缀，换账号读不到上一账号的私有列表。
- **UI 线程不做同步落盘**：有窗口时 `saveCachedFeed` 在后台协程完成序列化与 `put`；无窗口（无头测试、主窗口已关闭）时没有可阻塞的 UI 线程，直接同步写完，避免后台协程在测试目录删除后落盘。
- **不闪、不回顶**：预载与静默刷新都不强制回顶，滚动锚点由既有 `ListState` 的 Key 对齐保持。
- **不扩大敏感面**：列表缓存只存公开字段与作者标识，不存令牌、不存付费正文。

---

## 四、验收条件（可检验）

1. **断网冷启动可读**：断网启动后，首页、新发表、今日热议各自显示上次成功加载的首屏（≥ 20 条），页面标注缓存时间；不是空列表，也不是错误页。
2. **有网刷新不打断**：在线时从其他 tab 切回，列表在后台刷新成功后内容更新，且滚动位置不回顶；刷新失败不弹错、列表保持原样。
3. **缓存写入不阻塞 UI**：窗口存在时，列表加载成功的回包路径上 UI 线程不出现同步 JSON 落盘；无窗口场景同步写完，测试可确定性断言。
4. **缓存有上界**：单键不超过 256 KB，超出跳过；只保留最近 64 个列表键；退出账号后该账号的 `feed:<name>:` 键被清理。
5. **账号作用域**：`feed:` 键结构含 `scope` 段；`NeedsAuth` tab 的键为账号名，公开列表为 `local`（`TestFeedCacheKeyScope`）。
6. **评论已读页可离线**：某帖读到第二页后断网重开，能显示前两页评论；付费正文仍不出现在 `cache/detail.json`。
7. **滚动锚点保持**：静默刷新前后，顶部可见帖子的 Key 不变（既有 `refresh_keep_scroll_test.go`）。
8. **回归**：`go test ./...` 全绿；既有无头列表测试、`detail_cache_test.go`、`offline_store` 相关测试不回归。

---

## 五、实施结果

1. `offline_cache.go`：新增 `feedCachePrefix`。
2. 新增 `feed_cache.go`：`feedCacheKey` / `readCachedFeed` / `preloadFeedCache` / `saveCachedFeed` / `markFeedOffline` / `clearFeedCacheState`。
3. `main.go`：`tabState` 增加 `cacheNote`/`cachedAt`/`fromCache`；`loadSpec` 首屏预载与离线保留；成功后写缓存；`mergeFreshPage` 成功后写缓存；`feedHeader` 显示离线说明；`logoutAccount` 异步清理账号缓存。
4. `detail_cache.go`：评论上限从 1 页放宽到前两页，单篇上限 100 条。
5. 测试：新增 `feed_cache_test.go`（读写、预载不置 loaded、断网保留、键作用域、清标记）；全量 `go test ./...` 通过。

当前没有独立的「清空离线缓存」设置入口，因此未改设置页；退出账号与清空阅读历史各管自己的缓存范围。

---

## 六、风险与回滚

- **陈旧内容误导**：靠页头标注时间 + 静默刷新覆盖缓解，不把旧内容当成刚刷新。
- **缓存膨胀**：单键 256 KB 上限 + 最多 64 个键 + 只存首屏；`prune` 兜底。
- **刷新竞态**：沿用既有 `reqSeq`/`gen` 与 `refreshGen`，旧回包丢弃，刷新中不重复发。
- **回滚**：删除 `feed:` 前缀并去掉 `saveCachedFeed`/`preloadFeedCache` 调用即可退化为「无列表缓存」，其他逻辑不受影响。