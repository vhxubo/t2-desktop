# 本地内容屏蔽（content-filter）设计

- 状态：设计待实施（本文只有设计，没有改代码）
- 日期：2026-11-22
- 接口：`GET/PUT /v1/me/content-filter`（`read:me` / `interact`）
- 范围：读取名单、首页帖子过滤、评论区单条折叠与手动展开、用户资料卡快捷屏蔽、
  「我的」窗口里的名单管理、本地缓存与失败策略
- 非目标：服务端过滤、按 id 记录（接口只给用户名 / 节点名）、跨账号共享、
  通知与搜索的过滤（列为后续阶段）

## 1. 目标

对应四条明确要求：

1. **首页**不再出现被屏蔽账号发布的任何帖子（整行剔除，不留占位）。
2. **评论区**里被屏蔽的评论压成一行文字提示，用户可以手动展开查看；
   展开只作用于这一条，其它评论不受影响，也不需要记忆（不落盘）。
3. **用户资料卡**上有屏蔽按钮：一键屏蔽，按钮本身显示当前是否已屏蔽，
   再点一次取消。
4. 名单的**账号设置**收进「我的」窗口：三类名单的查看、增删与保存都在那里，
   账号菜单气泡给一个直达入口。

以及接口本身的约束：

5. 名单与网页端「设置 → 内容屏蔽」是同一份数据，**服务端只下发名单，过滤由客户端做**。
6. 匹配一律不区分大小写；三类名单各最多 50 条，整份 URL 编码后 ≤ 3500 字节。

## 2. 已确认决策

| 编号 | 问题 | 结论 | 理由 |
| --- | --- | --- | --- |
| 1 | 过滤发生在哪一层 | 列表**保留原始数据**（`tabState.raw`），可见列表 `items` 由它派生；评论在**渲染时**判定 | 屏蔽按钮就在应用里，刚屏蔽完必须立刻生效、取消后还要能还原；只存过滤后的数据就只能重新请求。评论必须保留原始行，否则楼层号、树形导轨、定位与本地记忆全部错位 |
| 2 | 名单按什么匹配 | 用户名 / 节点显示名 / 关键词，全部 `TrimSpace` + `ToLower` 后比较 | 接口存的就是名字，不是 id / slug；按 id 匹配会永远不命中 |
| 3 | 首页首次加载与名单的先后 | 名单未落定前最多等 5 秒，超时按「无名单」继续 | 避免先露出被屏蔽的帖子再消失；有本地缓存时这一步是瞬时的 |
| 4 | 整页都被过滤掉怎么办 | 自动续拉下一页，最多连续跳过 3 页；仍为空则显示专门的空状态 | 否则用户看到空白页，还要手动滚动触发加载 |
| 5 | 评论被屏蔽后子回复怎么办 | 子回复照常显示，只折叠这一条 | 「每次查看只针对该评论」；子回复是别人发的，不应被连带隐藏 |
| 6 | 展开状态存哪 | `detailState.revealed map[string]bool`，只在内存 | 明确要求「不需要记忆」；关掉详情即丢弃 |
| 7 | 名单从哪来、什么时候读 | 登录后读一次 + 本地缓存兜底；换账号重置；打开「我的 → 内容屏蔽」强制刷新 | 与网页端共享同一份数据，缓存只为离线与首帧 |
| 8 | 拉取失败怎么办 | fail-open：不隐藏任何内容；管理页显示原因并可重试 | 屏蔽是体验增强，不能因为一次请求失败把信息流清空 |
| 9 | 详情页正文要不要拦 | 不拦；用户资料卡里这个人的帖子列表也不拦 | 用户主动点进来的内容不该被「本地规则」挡在门外；列表已经拦过一轮 |
| 10 | 管理界面放哪 | **「我的」窗口新增「内容屏蔽」标签页**，账号菜单气泡加入口；设置窗口不再重复做一份 | 两处编辑同一份「整份替换」名单，必然互相覆盖；账号级设置收在账号窗口里最自然 |
| 11 | 资料卡按钮怎么显示状态 | 直接读共享名单快照，`屏蔽` / `已屏蔽` 两态；提交中禁用 | 不另存一份「这个人是否被屏蔽」，避免与名单不同步 |
| 12 | 屏蔽 / 取消后列表怎么办 | 不重新请求：用 `raw` 重算 `items`；评论清空展开态后重绘 | 一次点击就该立刻生效，滚动位置也尽量不动 |

## 3. 接口契约（照抄文档，不凭记忆）

### 3.1 读

```http
GET /v1/me/content-filter
Authorization: Bearer <token>
```

```json
{
  "c": 0,
  "m": "请求成功",
  "d": {
    "blocked_users": ["alice"],
    "blocked_nodes": ["职场"],
    "keywords": ["剧透"]
  },
  "t": 1791446400000
}
```

- 三个数组**恒存在**；从未设置过也是 `200` + 三个空数组，不是 `404`。
- 元素都是**字符串**：`blocked_users` 是用户名，`blocked_nodes` 是节点**显示名**，
  `keywords` 是关键词。不是用户 id，也不是节点 id / slug。
- 没有分页，整份返回。

### 3.2 写

```http
PUT /v1/me/content-filter
Authorization: Bearer <token>
Content-Type: application/json

{"blocked_users":["alice"],"blocked_nodes":["职场"],"keywords":["剧透"]}
```

- **整份替换**；三个数组都必填，缺任意一个 `400 invalid_request`，空数组表示清空该类。
- **不需要 `Idempotency-Key`**（重复提交同一份 body 结果相同）。
- 成功 `200` + 写入后的完整名单，结构与 GET 相同，不用再读一次。
- 服务端会清洗：去首尾空白、不区分大小写去重（保留第一次出现的写法）；
  空字符串条目 → `400`；屏蔽自己 → `400`（`field: blocked_users`）。
- 限制：

| 项 | 上限 |
| --- | --- |
| 每类条数 | 50 |
| `blocked_users` 单项 | 255 字符 |
| `blocked_nodes` 单项 | 100 字符 |
| 整份名单 URL 编码后 | 3500 字节（中文按 9 字节算） |

- scope：读 `read:me`、写 `interact`。两者都已在 `auth.go` 的 `defaultScopes` 里，
  老会话不需要重新授权。

### 3.3 由接口推出来的硬约束

1. **只能读-改-写**：只想加一个用户时，也必须把另外两类原样带上，否则会被清空。
2. **本地必须先校验**：50 条、单项长度、3500 字节都在发请求前拦住，
   不要靠服务端的 `400` 当校验器。
3. **改名即失效**：对方改用户名、节点改名后，旧条目不再命中，需要重新添加。
   这不是 bug，文档明确说明；管理页要写一句提示。

## 4. 匹配规则

### 4.1 归一化

```go
func normalize(s string) string {
    return strings.ToLower(strings.TrimSpace(s))
}
```

- 只用 `ToLower`（Unicode 简单折叠），不引入 `golang.org/x/text/cases`：
  名单与内容都来自同一个站点，简单折叠足够，且行为可预测。
- 索引用 `map[string]bool`，O(1) 命中；空串不放进索引。

### 4.2 各对象读哪个字段

| 对象 | 作者 | 节点 | 文本 |
| --- | --- | --- | --- |
| 首页 / 榜单帖子（`Item`，`kindTopic`） | `Item.Author` | `Item.Node.Name` | `Item.Title`；动态流再加 `Item.Extra.Excerpt` |
| 最新评论 / 动态里的评论行（`Item`，`kindComment`） | `Item.Extra.Author`（**`Item.Author` 此时是空的**） | `Item.Node.Name` | `Item.Extra.Excerpt` / `Item.Title` |
| 详情页评论（`replyItem`） | `r.Author` | 不适用 | `r.Content`（Markdown 原文） |

注意：

- 节点只按**显示名**匹配，`Node.Name` 为空时跳过节点规则，不拿 slug 顶替。
- 作者为空（接口没给 `author`）或为「匿名」时不参与用户匹配，否则会把所有匿名内容一起隐藏。
- 已删除的评论不折叠：它本来就只显示「该评论已删除」，再盖一层提示没有意义。
- 关键词对帖子只匹配标题（列表接口不返回正文）；评论匹配正文原文。
  这是首页接口的数据限制，不是实现偷懒，文档里要写明。

### 4.3 命中优先级

同一对象可能同时命中多条规则，只显示一条原因，优先级：

```text
用户 > 节点 > 关键词
```

理由：用户屏蔽是「我不想看到这个人」，比关键词更明确；提示里只报最外层原因，
避免把用户设置的关键词反复摊在界面上。

### 4.4 行为矩阵

| 场景 | 用户 | 节点 | 关键词 | 阶段 |
| --- | --- | --- | --- | --- |
| 首页帖子列表 | 整行剔除 | 整行剔除 | 整行剔除 | 一（用户）/ 三（节点、关键词） |
| 其它帖子列表（热议、新发表、社区） | 整行剔除 | 整行剔除 | 整行剔除 | 三 |
| 最新评论 / 动态 | 整行剔除 | 整行剔除 | 整行剔除 | 三 |
| 详情页正文 | 不拦截 | 不拦截 | 不拦截 | — |
| 详情页评论区 | 折叠成一行提示，可展开 | — | 折叠成一行提示，可展开 | 二 |

## 5. 数据模型与生命周期

### 5.1 DTO 与索引

```go
// content_filter.go
const contentFilterPath = "/v1/me/content-filter"

type contentFilterDTO struct {
    BlockedUsers []string `json:"blocked_users"`
    BlockedNodes []string `json:"blocked_nodes"`
    Keywords     []string `json:"keywords"`
}

type blockReason struct {
    Kind  string // "user" / "node" / "keyword"
    Value string // 命中的名单条目，原样保留，用于提示
}

type contentFilter struct {
    raw      contentFilterDTO
    users    map[string]bool
    nodes    map[string]bool
    keywords []string // 保留原写法，匹配时再 ToLower
}
```

- 解码走 `object` 容错读取（`o["blocked_users"]` 的字符串数组），
  不写严格 `json.Unmarshal` 到 DTO 后就假设字段齐全：v1 只承诺加字段，
  但三个数组「恒存在」仍要用容错读法兜住 `null`。
- `contentFilter` 是不可变快照：加载成功后整体替换，渲染时只读，不加锁。

### 5.2 运行状态

```go
type contentFilterState struct {
    filter  *contentFilter // nil 表示「不知道」，此时不过滤
    loaded  bool
    loading bool
    err     string
    gen     uint64
    // saving / savingUser 是写请求的在途状态：资料卡按钮与「我的」页的保存
    // 按钮都读它，期间不接受第二次提交。
    saving     bool
    savingUser string // 非空表示正在针对某个用户名做快捷屏蔽 / 取消
    // 失败退避，写法同 comment_cost.go 的 triedAt / failures
    triedAt  time.Time
    failures int
    // pendingSince 用于「首次加载最多等 5 秒」
    pendingSince time.Time
}
```

常用的几个只读判断，渲染与按钮都走它们，不各自拆字符串：

```go
func (a *app) filterBlocksUser(name string) bool // 当前快照是否屏蔽了这个用户名
func (a *app) filterBlocksNode(name string) bool
func (a *app) filterKeywordHit(text string) (string, bool)
func (a *app) filterCount() int // 三类条数之和，账号菜单气泡用
```

挂在 `app` 上：

```go
// main.go 的 app
contentFilter *contentFilterState
```

- 一个账号窗口一份（`app` 就是一个账号的界面），不放进 `appDeps`：
  名单是按账号的数据，不能进程级共享。
- 换账号 / 退出：`onAuthChanged` 里整体重建；退出后清空，避免下一个账号继承。
- 线程：请求在 goroutine 里发，结果经 `a.publish` 回 UI 线程再写状态。

### 5.3 加载时机

```text
登录成功 / 启动时已有会话
        ↓
读本地缓存（同步，localKV）→ 立刻可用于首帧过滤
        ↓
后台 GET /v1/me/content-filter（每个 app 生命周期一次）
        ↓
成功：替换快照 + 写回缓存；失败：保留缓存，记退避
```

- 缓存键：`content-filter:<scope>`，`scope` 用 `a.persistableScope()`
  （未登录 / 还没拿到昵称时不落盘，见 `offline_cache.go`）。
- 缓存值：

```go
type contentFilterCache struct {
    DTO     contentFilterDTO `json:"dto"`
    SavedAt time.Time        `json:"saved_at"`
}
```

- 「我的 → 内容屏蔽」页每次切到该标签都 `force=true` 再读一次，保证看到网页端的最新改动。
- 快捷屏蔽与保存共用同一条写路径（见 8.5），成功后都用**响应里的名单**覆盖快照。

### 5.4 首页首帧的顺序

`ensureLoaded` 里加一道闸：

```go
spec := a.specFor(a.tab)
if spec.FilterBlocked && !a.contentFilter.ready() {
    return // 下一帧再试；名单到位后 publish 会触发重建
}
```

`ready()` 的定义：

```text
未登录                      → true（没有名单，不过滤）
loaded                      → true
失败过（err != ""）          → true（fail-open）
pendingSince 超过 5 秒       → true（超时放行）
其它（正在首次加载）          → false
```

这样正常情况只多等一个很小的请求；网络异常时最多等 5 秒就放行，
不会把首页卡死。

## 6. 首页帖子过滤（阶段一）

### 6.1 接入点

`tabSpec` 增加一个开关，只给首页打开：

```go
type tabSpec struct {
    // ...
    // FilterBlocked 表示这个列表吃内容屏蔽名单（见 content_filter.go）。
    // 只给首页打开；其它榜单在阶段三再逐个评估。
    FilterBlocked bool
}

{Label: "首页", Path: "/v1/topics", ..., FilterBlocked: true},
```

`tabState` 把「拉到的」与「显示的」分开：

```go
type tabState struct {
    // raw 是接口返回并已按 Key 去重的全部条目，屏蔽与否都留着：
    // 在资料卡上屏蔽 / 取消屏蔽后要立刻重算 items，不能重新发请求。
    raw []Item
    // items 是 raw 按当前名单过滤后的可见列表；下游（foldRows、渲染、
    // 头像预取）只读它。
    items []Item
    // hidden 是 raw 里被剔除的条数，只用于页头说明。
    hidden int
    // filterSkip 是连续「整页被过滤」的页数，用来限制自动续拉。
    filterSkip int
}
```

### 6.2 loadSpec 的改动

```go
// 第 1 页：st.raw = res.Items；第 2 页起：按 Key 去重后追加进 st.raw
// （去重仍以 raw 为准，重复页不会因为「过滤后没新增」被误判）。
rawAdded := 新增到 st.raw 的条数
if page == 1 {
    st.noMore = len(res.Items) == 0
    st.filterSkip = 0
} else {
    st.noMore = rawAdded == 0 // 注意：不能用「过滤后新增」判断
}
// 每次 raw 变化后重算可见列表（无名单时就是 raw 的切片头）
st.items, st.hidden = a.filterItems(spec, st.raw)
st.refold(spec)
```

要点：

- `noMore` 必须按**原始**响应判断。如果按过滤后的新增条数判断，
  一个整页都被屏蔽的页会被误判成「到底」，后面的页永远拉不到。
- `refold` 与 `prefetchItemAvatars` 只面对 `items`，被屏蔽的作者头像不会被下载。
  续拉时只对**本次新增且可见**的条目预取。
- 置顶帖同样受规则约束：屏蔽的是「这个人」，置顶不构成例外。
- 无限滚动的启动条件从 `len(st.items) > 0` 改成 `len(st.raw) > 0`：
  首屏全被过滤时 `items` 是空的，但用户仍应能继续往下拉。

### 6.3 整页被过滤时的续拉

```go
// 在 publish 回调末尾（此时 st.loading 已复位）
if spec.FilterBlocked && len(newVisible) == 0 && rawAdded > 0 &&
    st.filterSkip < contentFilterSkipPages && hasMorePages(st) {
    st.filterSkip++
    a.loadFor(owner, idx, page+1)
}
```

- `contentFilterSkipPages = 3`：连续三页都被屏蔽就先停下，避免为了找一条内容
  把配额打光。
- 续拉失败照旧走 `st.moreErr`，不影响已显示的列表。


### 6.4 页头计数

`feedCountLabel` 现在读服务端的 `st.total`，它**包含**被屏蔽的帖子。
过滤打开时改成：

```text
共 50 条 · 已屏蔽 3
```

- 总数仍是服务端口径（诚实），后面补一个已加载页里被剔除的条数。
- 没有隐藏项时不加后缀，界面保持原样。
- 这条后缀同时是「为什么列表变少了」的唯一解释，不再另加说明文字。

### 6.5 全部被隐藏的空状态

`body` 的「暂无内容」分支要先判断 `st.hidden > 0`：

```text
标题：内容已按屏蔽规则隐藏
说明：当前列表的条目都在你的屏蔽名单里。
按钮：管理屏蔽名单
```

按钮打开「我的」窗口的「内容屏蔽」标签页（见 8.2）。不能用「暂无内容」：
那会让人以为站点真的没有帖子。

### 6.6 屏蔽 / 取消后的即时重算

快捷屏蔽成功后不重新请求列表：

```go
// 遍历所有已加载的 FilterBlocked tab
st.items, st.hidden = a.filterItems(spec, st.raw)
st.refold(spec)
st.list.Key = ... // 行数变化，Key 按新 items 重建
```

- 取消屏蔽同样走这里，之前被剔除的条目会原样回来——这就是保留 `raw` 的原因。
- 当前详情页评论区不需要重算数据：`replyList` 一直在，渲染时按新快照判定；
  只需清空 `revealed`（见 7.2）并重绘。
- 其它账号窗口各有自己的 `app` 与快照，不跨窗口推送；它们的名单在下一次
  登录 / 进入页面时自然更新。


## 7. 评论区单条折叠（阶段二）

### 7.1 为什么不在数据层删

`replyList` 是前序摊平的评论树，楼层号、树形导轨、`display` 索引、
`stayID` / `stayIdx` 的阅读位置、`focusID` 定位、离线缓存全部基于它。
把被屏蔽的评论从 `replyList` 里删掉会同时破坏这些不变量。

因此：**数据不动，渲染时判定**。被屏蔽的评论仍然占一行，只是这一行画成提示。

### 7.2 状态

```go
// detail_ui.go 的 detailState
// revealed 记录本次浏览里被手动展开的评论 id。只在内存，不落盘：
// 关掉详情、换一篇帖子就丢弃（「不需要记忆」）。
revealed map[string]bool
```

- 键是评论 uuid，所以同一页里多条被屏蔽的评论互不影响。
- 刷新评论不清空（评论 id 没变）；名单重新加载后清空，避免展开态与规则不一致。
- 不写进 `replyViewMemory`，也不进 `offline.json`。

### 7.3 判定与渲染

在 `replyRow` 的最前面插入一个早退分支：

```go
func (a *app) replyRow(c *ui.Context, r replyItem, tree replyTreeGuide, focusID string, isNew bool) {
    reason, blocked := a.contentFilter.commentBlocked(r.Author, r.Content)
    if blocked && !a.detail.revealed[r.ID] {
        a.blockedReplyRow(c, r, tree, reason)
        return
    }
    // ... 原有渲染
}
```

`blockedReplyRow` 的要求：

- 保留与正常行相同的 `Padding` 左缩进（`12 + Depth*replyTreeIndent`）和树形导轨，
  这样评论树的形状不会因为折叠而断开。
- 一行内容：原因 + 「展开」按钮。高度与普通行接近（建议 32 DIP），
  不做卡片、不加图标堆叠。
- 「展开」只做一件事：

```go
if a.detail.revealed == nil {
    a.detail.revealed = make(map[string]bool)
}
a.detail.revealed[r.ID] = true
```

不请求接口、不滚动、不影响其它行。

- 展开后的正常行在头部时间后面补一个很轻的「收起」文字按钮，
  写回 `revealed[r.ID] = false`；没有这个入口，用户只能靠离开页面复原。

### 7.4 固定文案（测试断言用）

| 场景 | 文案 |
| --- | --- |
| 用户命中 | `已屏蔽 alice 的评论` |
| 关键词命中 | `已屏蔽含「剧透」的评论` |
| 展开按钮 | `展开` |
| 展开后的收起 | `收起` |
| 空列表（全部被过滤） | `内容已按屏蔽规则隐藏` |

- 提示里**不出现评论正文**，否则屏蔽就失去意义。
- 关键词只显示命中的那个词，不显示上下文。

### 7.5 与现有机制的关系

| 机制 | 影响 |
| --- | --- |
| 楼中楼折叠（`collapsed`） | 两套独立：被屏蔽的行先画提示；它若有子回复且被折叠，子回复仍按原逻辑隐藏。提示行不显示「折叠 / 展开楼中楼」按钮 |
| 阅读位置记忆（`stayID` / `stayIdx`） | 不受影响：行数与顺序都没变 |
| 定位（`focusID`） | 定位到被屏蔽评论时照常滚过去并显示提示，用户可以就地展开 |
| 「新」标记 | 被屏蔽的提示行不画「新」角标，避免把注意力引向隐藏内容 |
| 通知已读（`markNotificationSeen`） | 不受影响：目标行仍在 `display` 里，滚到可视区即算看到 |
| 离线缓存 | 缓存的是原始评论，展开态不缓存；离线打开同样按名单折叠 |
| 评论头像预取 | 被屏蔽的评论跳过预取；展开时按需加载（`imageCache` 本来就是按需） |
| 回复 / 表态 / 打赏 | 提示行不提供这些入口；展开后恢复正常 |

## 8. 屏蔽入口与管理（阶段三）

### 8.1 用户资料卡的屏蔽按钮

位置：`userCardHeader` 右侧，关注按钮的右边，两个按钮同一行（`Gap(8)`）。

```text
[头像] 昵称 Lv.6 [PRO] [徽章]        [关注ta] [屏蔽]
       @alice · 注册于 …                      ↑ 已屏蔽时变成「已屏蔽」
```

- 状态直接读共享快照，不另存：

```go
blocked := a.filterBlocksUser(cs.username)
```

- 三种外观：

| 状态 | 文案 | 行为 |
| --- | --- | --- |
| 未屏蔽 | `屏蔽` | 点击写入名单 |
| 已屏蔽 | `已屏蔽` | 点击移出名单（取消屏蔽） |
| 提交中 | `处理中…`，禁用 | 不接受第二次点击 |

- Tooltip：未屏蔽「不再显示这个人的帖子和评论」；已屏蔽「点击取消屏蔽」。
- 自己的资料卡不画这个按钮（`a.userCardSelf()`），与关注按钮同一套判断。
- 未登录也画：点击先走 `requestLogin`，登录后回到资料卡再点一次即可。
  没有账号就没有可写的名单，不能假装成功。
- 不加二次确认：屏蔽可逆，确认框只会让「手滑」变成两步；结果用 toast 报告。
- 这个人的资料卡里「ta的帖子 / 回复」列表**不按名单过滤**：用户是主动点进来看的，
  和详情页正文同一个道理。

写路径：

```go
func (a *app) setUserBlocked(name string, blocked bool) {
    // 1. 未登录：requestLogin；2. 自己：直接返回
    // 3. 在快照上增删（保留原写法、去重），走 8.5 的 save
    // 4. 成功后 applyFilterToLoadedTabs() + clearRevealed() + 重绘资料卡
}
```

- 失败时按钮回到原状态，toast 显示原因；不做乐观切换，避免界面显示
  「已屏蔽」而服务端其实没写进去。

### 8.2 「我的」窗口 → 内容屏蔽

「我的」窗口现在有 6 个标签（概览 / 个人简介 / 我的内容 / 我的回复 / 徽章 /
权限与账号）。新增第 7 个：

```go
const profileTabFilter // 标签「内容屏蔽」，图标 lucide.EyeOff()
```

`profileTabLabels` 与 `profileTabIcons` 必须同时加，长度断言已有测试会兜住。

页面结构：

```text
内容屏蔽
与网页端「设置 → 内容屏蔽」是同一份名单；对方改名后需要重新添加。

屏蔽用户（3/50）
  [alice ×] [bob ×]
  [ 输入用户名              ] [添加]

屏蔽节点（0/50）
  [ 输入节点名              ] [添加]

屏蔽关键词（1/50）
  [剧透 ×]
  [ 输入关键词              ] [添加]

[撤销改动]                                  [保存]
```

- 三个区块共用一套 chip 编辑器（`contentFilterChips`），只有标题、上限、
  占位文案不同；不写三份重复代码。
- chip 的 × 只改本地草稿，点「保存」才 PUT；「撤销改动」回到服务端快照。
- 有改动时标题旁给一个「未保存」小标记；保存成功后消失。
- 每块右上角 `3/50`；整份编码后超过 80% 才显示字节数，平时不打扰。
- 顶部一句说明是必要的：改名失效、与网页端同源，这两件事不写用户不会知道。
- 进入该标签时 `ensureContentFilter(force: true)`：先看到网页端最新一份。
- 加载中显示骨架 / 转圈；失败显示原因 + 「重试」，已有缓存时列表照常可读，
  但保存按钮禁用（避免拿旧基线覆盖网页端的新改动）。
- 未登录（理论上进不来）显示「登录后管理屏蔽名单」。

「我的」窗口的打开入口需要支持指定标签：

```go
func (a *app) showProfileTab(tab int) // 设置 ps.tab 后再 showProfileWindow
```

### 8.3 账号菜单气泡入口

`profileMenuPanel` 在「我的」上面加一行：

```go
a.profileMenuRow(c, lucide.EyeOff(), "内容屏蔽", "打开内容屏蔽设置",
    "管理屏蔽的用户、节点与关键词", func() {
        if n := a.filterCount(); n > 0 {
            ui.Text(c, fmt.Sprintf("%d 项", n)).FontSize(11).TextColor(tk.TextSecondary)
        }
    }, func() { a.showProfileTab(profileTabFilter) })
```

- 右侧只在有名单时显示总数（用户 + 节点 + 关键词），没有就不占位。
- 点这一行 = 收起气泡 + 打开「我的」窗口并直接落在「内容屏蔽」标签。
- 不在设置窗口里重复做一份：两个入口编辑同一份整份替换名单，很容易互相覆盖。

### 8.4 详情页快捷屏蔽

- 详情页右下角「更多」菜单加一项「屏蔽作者」：不是自己、且当前未屏蔽时出现。
- 已屏蔽时这一项变成「取消屏蔽」。
- 与资料卡按钮共用 `setUserBlocked`，成功后当前评论区立即按新名单重绘。

### 8.5 保存与同步

```go
func (c *Client) saveContentFilter(ctx context.Context, body contentFilterDTO) (contentFilterDTO, error) {
    return withAuthRetry(c, ctx, func(token string) (contentFilterDTO, error) {
        d, err := c.putJSON(ctx, c.apiURL(contentFilterPath), token, body)
        // 解码返回的完整名单
    })
}
```

- `api.go` 增加 `putJSON`，直接委托 `writeJSON(ctx, http.MethodPut, u, token, "", payload)`：
  这个端点不需要 `Idempotency-Key`，传空串。
- 成功用**响应里的名单**覆盖本地（服务端会 trim / 去重，以它为准），
  同时写回缓存，不用再 GET 一次。
- 成功后统一做三件事：

```text
1. applyFilterToLoadedTabs()  // 所有已加载的 FilterBlocked 列表按 raw 重算
2. clearRevealed()            // 当前详情 + 详情栈 + 各详情窗口的 revealed 清空
3. 重绘资料卡 / 「我的」窗口 / 主窗口
```

- 失败：
  - `400 invalid_request`：读 `field` 指出是哪一类，把服务端文案显示在对应块下面；
  - `403`：交给 `handleAuthError` 走授权补全，成功后重放这次保存；
  - `429`：沿用共享闸门的秒数提示；
  - 网络错误：toast 报原因，草稿与 dirty 保留。
- 整份替换的语义要在界面上说清：保存会覆盖网页端的改动。做法是**进入标签页时先 GET**，
  让用户看到的就是最新一份；不在保存前再做一次 diff（多一次请求，收益有限）。

### 8.6 校验（发请求前）

```text
1. 逐项 TrimSpace；空项不允许添加（服务端会 400）
2. 大小写不敏感去重，保留第一次出现的写法（与服务端清洗规则一致）
3. 每类 ≤ 50 条
4. 用户单项 ≤ 255 字符、节点单项 ≤ 100 字符（按 rune 数）
5. 不能屏蔽自己：与 a.auth.username() 做 EqualFold 比较
6. 整份编码后 ≤ 3500 字节：
   sum(len(url.QueryEscape(item))) + 固定开销（每条 4、每类 20）
```

第 6 条用 `url.QueryEscape` 估算，正好对上文档「中文按 9 字节」的口径
（一个中文字 UTF-8 三字节，编码成 `%XX%XX%XX` 九字节）。
估算超限就拦下并提示「名单过长，请删减后再保存」，不把 `400` 留给服务端。

### 8.7 状态一致性

| 场景 | 结果 |
| --- | --- |
| 资料卡屏蔽 alice，首页正开着 | 首页立刻少掉 alice 的帖子，滚动位置不动 |
| 资料卡取消屏蔽 alice | 之前剔除的帖子从 `raw` 还原，无需重新请求 |
| 在「我的」里删掉关键词并保存 | 已加载列表与当前评论区按新名单重算 |
| 网页端改了名单 | 下次进入「内容屏蔽」标签或重新登录时同步；期间本地按旧快照过滤 |
| 两个账号窗口 | 各自一份快照，互不影响 |
| 同一账号开详情窗口 | 共用同一个 `app`，屏蔽后各详情窗口的 `revealed` 一并清空 |


## 9. 失败、限流与离线

| 情况 | 行为 |
| --- | --- |
| GET 失败、有缓存 | 用缓存过滤；「我的 → 内容屏蔽」显示「名单可能不是最新」+ 重试 |
| GET 失败、无缓存 | 不过滤（fail-open）；记录 `triedAt` / `failures`，按 30s→1m→5m 退避重试 |
| 401 | `withAuthRetry` 刷新令牌后重试一次；刷新失败按现有逻辑清会话 |
| 403 `insufficient_scope` | `handleAuthError` 弹授权，成功后重放加载 / 保存 |
| 429 | 共享 `rateLimitGate` 统一退避，不单独重试 |
| PUT 失败 | 保留草稿与 dirty 标记，toast 报原因；按钮不做乐观切换 |
| 离线 | 只用缓存；管理页只读，保存与快捷屏蔽禁用并说明原因 |
| 缓存损坏 | 当作没有缓存，删除该键，只记日志 |

## 10. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `content_filter.go`（新增） | DTO、索引、归一化、`matchItem` / `matchComment`、校验、加载与保存、缓存读写、`setUserBlocked`、`applyFilterToLoadedTabs` |
| `content_filter_ui.go`（新增，阶段三） | 「我的」页的「内容屏蔽」标签、chip 编辑器、字节 / 条数校验提示 |
| `api.go` | 增加 `putJSON`（委托 `writeJSON`） |
| `main.go` | `app.contentFilter` 字段；`tabSpec.FilterBlocked`；`tabState.raw` / `hidden` / `filterSkip`；`loadSpec` 派生可见列表与续拉；`feedCountLabel`；`ensureLoaded` 闸门；`onAuthChanged` 重置与预加载；空状态分支 |
| `detail_ui.go` | `detailState.revealed`；`replyRow` 早退分支；`blockedReplyRow`；`newDetailState` 初始化 |
| `user_card.go` / `user_card_ui.go` | 资料头右侧的「屏蔽 / 已屏蔽」按钮；`setUserBlocked` 调用；提交中与失败状态；本人不显示 |
| `profile.go` | 新标签常量与 `profileTabLabels`；`showProfileTab`；切到该标签时强制刷新名单 |
| `profile_ui.go` | 第 7 个标签的图标与页面挂载；账号菜单气泡新增「内容屏蔽」行 |
| `offline_cache.go` | 复用 `persistableScope`，不改逻辑 |
| 测试 | `content_filter_test.go`、`content_filter_ui_test.go`、`user_card_test.go`、`profile_test.go`、`main_test.go` / `detail_ui_test.go` 增补、`proxy_test.go` 增补 |

## 11. 测试计划

### 11.1 单元（`content_filter_test.go`）

- 解码：正常三数组、空数组、`null`、缺字段、未知字段、字段类型异常。
- 归一化：大小写、首尾空白、全角空格、中文关键词。
- 命中：用户名 / 节点显示名 / 关键词；作者为空与「匿名」不命中；`Node.Name` 为空不命中节点。
- 优先级：同时命中用户与关键词时只报用户。
- 校验：50 条边界、255 / 100 字符边界、空项、自己、3500 字节边界。
- 保存：断言 `PUT` 路径、`Authorization`、`Content-Type`、body 三个数组齐全、
  **没有 `Idempotency-Key`**、返回覆盖本地。

### 11.2 首页（`main_test.go` 增补）

- `httptest` 返回 3 条帖子，其中 1 条作者在名单里：断言 `st.raw` 有 3 条、
  `st.items` 只剩 2 条、`st.hidden == 1`。
- 整页都被过滤：断言自动拉了第 2 页，且 `noMore` 没有被误置。
- 名单 GET 失败：断言列表照常显示（fail-open），并在 5 秒闸门后放行。
- 页头计数：断言「共 N 条 · 已屏蔽 M」。
- 换账号：断言旧名单不会套到新账号上。
- 屏蔽后重算：先加载 3 条，再把其中一位作者加进名单，断言不重新请求、
  `items` 立刻变 2 条；取消屏蔽后回到 3 条。

### 11.3 评论区（`detail_ui_test.go` 增补）

- 被屏蔽评论只出现一行提示，正文文本在界面树里找不到。
- 点「展开」后该条正文出现，**其它**被屏蔽评论仍是提示。
- 点「收起」恢复提示。
- 关闭详情再打开：又是提示（不记忆）。
- 楼中楼：父评论被屏蔽时子回复仍可见；折叠 / 展开子回复正常。
- 定位到被屏蔽评论：滚到该行并显示提示，不崩、不错位。
- 关键词命中：提示里出现「含「剧透」」，正文不出现。
- 在资料卡屏蔽当前评论作者后，当前详情页立即折叠，`revealed` 被清空。

### 11.4 用户资料卡（`user_card_test.go` 增补）

- 未屏蔽时按钮是「屏蔽」；名单里已有这个人时是「已屏蔽」。
- 点击「屏蔽」：断言发出 `PUT /v1/me/content-filter`，body 里带这个用户名，
  成功后按钮变「已屏蔽」，toast 出现。
- 再点一次：body 里不再有这个名字，按钮变回「屏蔽」。
- 提交中按钮禁用且显示「处理中…」；失败时按钮回到原状态，名单不变。
- 自己的资料卡没有这个按钮。
- 未登录点击：走登录流程，不发 PUT。
- 资料卡里这个人的帖子列表不被过滤（与详情页正文一致）。

### 11.5 「我的」页（`content_filter_ui_test.go` / `profile_test.go` 增补）

- 标签数量与 `profileTabLabels` / `profileTabIcons` 长度一致，第 7 个是「内容屏蔽」。
- 三块各自显示 `n/50`；添加重复项（大小写不同）不新增；空项不能添加。
- 删除一项后出现「未保存」，点「撤销改动」恢复；点「保存」发出 PUT。
- 超过 50 条 / 超长单项 / 超过 3500 字节时保存按钮禁用并给出原因。
- 保存成功后用响应覆盖本地，缓存写入 `content-filter:<scope>`。
- 账号菜单「内容屏蔽」行：有名单时显示「N 项」，点击打开「我的」窗口并落在该标签。
- 名单 GET 失败：显示原因与「重试」，保存禁用。

### 11.6 代理

- 沿用 `proxy_test.go` 的假代理写法，断言 GET 与 PUT 都经过配置的代理
  （这是 `AGENTS.md` 的硬规则，新增出口必须补）。

## 12. 落地顺序

| 阶段 | 内容 | 完成标志 |
| --- | --- | --- |
| 一 | 名单读取 + 缓存 + `raw` / `items` 派生 + 首页按用户剔除 + 分页续拉 + 计数 | 首页看不到被屏蔽账号的帖子，测试全绿 |
| 二 | 评论区折叠 + 单条展开 / 收起 | 两条核心要求都可用，展开互不影响 |
| 三 | 用户资料卡屏蔽按钮 + 「我的 → 内容屏蔽」+ 账号菜单入口 + 详情页快捷屏蔽 | 名单能在应用内维护，屏蔽 / 取消即时生效 |
| 四 | 节点 / 关键词、其它列表（热议 / 最新评论 / 动态） | 规则覆盖全部列表 |

阶段一、二不依赖管理界面：名单先在网页端配置，应用只读；阶段三把管理搬进应用内。

## 13. 风险与取舍

| 风险 | 说明 | 处理 |
| --- | --- | --- |
| 按名字匹配 | 对方改名后条目失效；同名不同人理论上无法区分 | 文档已明确；管理页提示「改名后需重新添加」 |
| 关键词误伤 | 短词（如「的」）会命中大量内容 | 客户端只能忠实执行；空状态与页头计数让用户知道发生了什么 |
| 首页关键词只能匹配标题 | 列表接口不返回正文 | 文档写明；正文级过滤留给详情页（本就不拦） |
| 信息流变稀 | 大量屏蔽时首页可能只剩几条 | 自动续拉 + 空状态；不伪造内容 |
| 整份替换覆盖网页端 | 两端同时编辑会后写覆盖 | 进入标签页先 GET；文案说明 |
| 资料卡误触 | 一键屏蔽没有确认框 | 可逆、有 toast；再点一次即取消；不做二次确认避免两步操作 |
| 列表行数变化 | 屏蔽后 `items` 变短，滚动位置可能跳 | 用 `Key` 对齐可见行；不整页刷新、不回到顶部 |
| 隐私 | 名单是账号数据，缓存落在本机 | 复用 `localKV` 的账号作用域；不写日志、不进错误文案 |
| 首帧等待 | 首次加载多等一个请求 | 有缓存时瞬时；无缓存最多 5 秒后 fail-open |
| 评论展开态与刷新 | 刷新后评论 id 不变，展开态保留 | 明确「同一次浏览内保留，关页即忘」 |

## 附录 A：常量与文案

```go
const (
    contentFilterPath       = "/v1/me/content-filter"
    contentFilterMaxPerList = 50
    contentFilterMaxUserLen = 255
    contentFilterMaxNodeLen = 100
    contentFilterMaxBytes   = 3500
    contentFilterSkipPages  = 3
    contentFilterWait       = 5 * time.Second
)
```

固定文案：

- `已屏蔽 %s 的评论`
- `已屏蔽含「%s」的评论`
- `展开`
- `收起`
- `内容已按屏蔽规则隐藏`
- `当前列表的条目都在你的屏蔽名单里。`
- `共 %d 条 · 已屏蔽 %d`
- 资料卡按钮：`屏蔽` / `已屏蔽` / `处理中…`
- 资料卡 toast：`已屏蔽 %s` / `已取消屏蔽 %s`
- 「我的」标签：`内容屏蔽`
- 管理页说明：`与网页端「设置 → 内容屏蔽」是同一份名单；对方改名后需要重新添加。`
- 保存失败：`名单过长，请删减后再保存`

