# 本地草稿与草稿箱设计

- 状态：已实施
- 日期：2026-11-22
- 实现：`draft.go`（DTO / 存储 / 指纹 / 自动保存 / 恢复）、`draft_ui.go`（草稿箱页面）、
  `draft_test.go` 与 `draft_ui_test.go`（单元与无头 UI 测试）
- 范围：发帖 / 动态草稿、评论与回复草稿、侧边栏草稿箱入口、主窗口内嵌草稿箱页、自动保存与恢复
- 约束：草稿箱**不新开 mygo 窗口**，嵌在主窗口右侧内容区；界面用 lobe-mygo；不新增任何网络出口

## 1. 目标

对应用户提的四条要求：

1. 侧边栏有「草稿箱」入口，点进去在主窗口里看，不弹新窗口。
2. 支持一键清空当前账号的所有草稿（带确认，不可撤销）。
3. 草稿按账号隔离；不只发帖有草稿，评论 / 回复也有。
4. 自动保存到磁盘：输入到一半进程异常退出（崩溃、断电、任务管理器结束），重新打开后能从草稿箱进去恢复原样——包括「发一条新评论」还是「回复某一层楼」这个状态。

## 2. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 草稿箱位置 | 主窗口右侧内容区（`pageName = "drafts"`），标题栏与侧边栏保留，不新开窗口 |
| 2 | 账号隔离粒度 | 一个账号一个文件：`drafts/<accountID>.json`；未登录窗口 `guest`，旧版单账号会话 `local` |
| 3 | 发帖草稿数量 | 多条。每条有自己的 id；发帖 FAB 接着「最近编辑的那条」写，草稿箱里可「写新帖」另起一条 |
| 4 | 回复草稿数量 | 每篇帖子最多一条（同一篇的评论框只有一份状态）；不同帖子各一条，上限 200 条 |
| 5 | 自动保存节奏 | 停手 800ms 落盘；连续打字时最长 5s 必落盘。崩溃最多丢最近 5s |
| 6 | 清空范围 | 当前账号的全部草稿（发帖 / 动态 / 回复）。其它账号的文件不碰 |
| 7 | 退出账号是否删草稿 | 不删。账号 id 由昵称哈希得出（`accountIDFor`），重新登录同一昵称就能取回 |
| 8 | 恢复时的回复身份 | 只恢复「回谁」，不恢复「换个账号回复」（`replyAsID` 是临时借用，不落盘） |
| 9 | 未上传图片 | 保存时剥离 `![x](upload:N)` 占位符：上传任务不跨进程，留着会让草稿永远发不出去 |
| 10 | 清空 / 删除后内存里那份 | 一并清掉 live 状态，否则下一次自动保存会把刚删的内容又写回去 |

补充解释：

- 「不新开窗口」不等于「不占页面」：草稿箱是主窗口的一个页面，切走时和列表页一样整块换掉，右下角的 FAB 列随之收起（那里没有可刷新、可发帖的列表）。
- 多份发帖草稿是「写新帖」的副产品，不做草稿管理面板（重命名、置顶、分组都不做）。
- 清空只作用于当前账号，是因为一个窗口就是一个账号：别的账号的草稿不在这个窗口里，也就不该被这个窗口的按钮删掉。

## 3. 现状与代码位置

| 现有实现 | 位置 | 本次改动 |
| --- | --- | --- |
| 主窗口页面切换（list / detail / login） | `main.go` 的 `view` | 增加 `drafts` 分支：只替换右侧内容区 |
| 侧边栏导航项 | `main.go` 的 `sidebarNavItems` / `sidebarRail` / `handleSidebarNavItem` | 导航列表末尾（分割线之前）加「草稿箱」；折叠态同一份数据自动有 |
| 导航项点击分发 | `main.go` 的 `handleSidebarAction` | 增加 `case "drafts"` |
| 切页 | `main.go` 的 `selectTab` | 切回浏览页时把 `pageName` 拉回 `list` |
| 发帖窗口草稿 | `compose.go` 的 `composeState`，`compose_ui.go` 的 `showComposeWindow` / `closeComposeWindow` | 状态里加 `draftID`；关窗、切草稿、发布、清空时落盘 / 删条目 |
| 发布成功清表单 | `compose.go` 的 `resetComposeForm`，`compose_ui.go` 的发布回调 | 清表单同时删掉这份草稿条目 |
| 评论草稿 | `detail_ui.go` 的 `detailState.draft` / `replyTo` / `composerIdle` | 加指纹与「恢复后聚焦」标记；自动保存与发送成功后删除 |
| 独立写评论窗口 | `reply_window.go` | 关窗时落盘；恢复时不需要另开 |
| 本地落盘工具 | `auth.go` 的 `readJSONFile` / `writeJSONFile`（先 `.tmp` 再改名） | 直接复用，不引入数据库 |
| 离线缓存 | `offline_store.go` 的 `localKV` | **不复用**：它是一份 `cache/offline.json`，里面装着正文与评论缓存，逐键打字整份重写代价太大 |
| 账号生命周期 | `app_window.go` 的 `finalizeAccountWindow`，`accounts.go` 的 `markQuitting` / `logoutAccount` | 收尾前落盘；退出账号不删草稿文件 |
| 账号标识 | `accounts.go` 的 `accountIDFor` | 草稿作用域直接用它 |

## 4. 数据模型

### 4.1 文件与作用域

```text
<配置目录>/drafts/<scope>.json
```

`scope` 的取值：

| 场景 | scope | 说明 |
| --- | --- | --- |
| 账号窗口 | `a.accountID`（`accountIDFor(昵称)`） | 一个账号一份，互不可见 |
| 未登录窗口 | `guest` | 未登录也能写草稿（发帖窗口会先要求授权），但不与任何账号合并 |
| 旧版单账号会话（`accountID == ""` 且已登录） | `local` | 升级前的兜底路径，正常情况下会被 `migrateLegacySession` 换成正式账号 |

- 文件名只由 `accountIDFor` 生成，字符集是 `[a-z0-9_-]`，不需要再转义。
- 每个账号窗口各自持有一份 `draftStore`（同一个账号只会有一扇主窗口），不存在两个进程内实例写同一个文件的情况；`draftStore` 内部仍加 `sync.Mutex` 并复用 `writeJSONFile` 的原子写，防止定时器与退出路径同时落盘。
- 文件损坏（半截 JSON、手工改坏）时另存为 `<scope>.json.bad`，从空草稿继续，只记日志——草稿读不出来不该让应用起不来。

### 4.2 文件结构

```json
{
  "version": 1,
  "account": "alice-1a2b3c",
  "entries": [
    {
      "id": "compose-3f9c1d2e",
      "kind": "compose",
      "mode": 0,
      "created_at": "2026-11-22T09:12:03+08:00",
      "updated_at": "2026-11-22T09:20:41+08:00",
      "title": "本地草稿应该怎么存",
      "body": "想聊聊落盘节奏……",
      "node_id": "uuid-work",
      "node_label": "职场",
      "anonymous": false,
      "modules": {
        "vote": {
          "type_index": 0,
          "title": "存哪",
          "options": ["一个文件", "每份一个文件"],
          "checked": [false, false],
          "max_choices": 1,
          "deadline_on": false
        }
      }
    },
    {
      "id": "reply-7k2m9p",
      "kind": "reply",
      "created_at": "2026-11-22T08:40:00+08:00",
      "updated_at": "2026-11-22T08:41:12+08:00",
      "short_id": "abc123",
      "post_title": "2Libra 客户端更新日志",
      "text": "第 3 条我试了，确实……",
      "reply_to": {
        "id": "cmt-88",
        "level": 2,
        "parent_id": "cmt-12",
        "floor": 12,
        "author": "老王",
        "author_id": "u-9"
      }
    }
  ]
}
```

字段规则：

- `version` 不认识时按空草稿处理并记日志，不猜测结构。
- `kind` 只有 `compose`（发帖 / 动态）与 `reply`（评论 / 回复）两种；`mode` 沿用 `composeModePost` / `composeModeMoment`。
- 时间一律 RFC3339，显示时交给 `relativeTime`。
- `reply_to` 零值表示「发一条新的顶层评论」；有值时就是「回复某一层楼」，恢复后评论框上方那条「正在回复 12 楼」的提示原样回来。
- 未知字段忽略（与 API 解码同一套容错口径），以后加字段不涨版本号。

### 4.3 DTO 与运行态的映射

`composeState` / `detailState` 里全是 `ui.Handle`、定时器、`mdEditor` 这类不可序列化的东西，所以**单独定义一份 DTO**，双向显式映射，不直接给运行态结构挂 json tag。

```go
// draftEntry 是磁盘上的一条草稿。
type draftEntry struct {
    ID        string    `json:"id"`
    Kind      string    `json:"kind"` // compose | reply
    Mode      int       `json:"mode,omitempty"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`

    // compose
    Title     string               `json:"title,omitempty"`
    Body      string               `json:"body,omitempty"`
    Moment    string               `json:"moment,omitempty"`
    NodeID    string               `json:"node_id,omitempty"`
    NodeLabel string               `json:"node_label,omitempty"`
    Anonymous bool                 `json:"anonymous,omitempty"`
    Modules   *composeDraftModules `json:"modules,omitempty"`

    // reply
    ShortID   string      `json:"short_id,omitempty"`
    PostTitle string      `json:"post_title,omitempty"`
    Text      string      `json:"text,omitempty"`
    ReplyTo   draftTarget `json:"reply_to,omitempty"`
}

// composeDraftModules 是右侧栏那些附加组件。每个块只在它自己的开关打开时写入。
type composeDraftModules struct {
    Vote       *composeDraftVote       `json:"vote,omitempty"`
    Score      *composeDraftScore      `json:"score,omitempty"`
    Bet        *composeDraftBet        `json:"bet,omitempty"`
    Redeem     *composeDraftRedeem     `json:"redeem,omitempty"`
    Pool       *composeDraftPool       `json:"pool,omitempty"`
    Guard      *composeDraftGuard      `json:"guard,omitempty"`
    Restricted *composeDraftRestricted `json:"restricted,omitempty"`
}
```

映射函数（都在 `draft.go`，可单测）：

| 方向 | 函数 | 说明 |
| --- | --- | --- |
| 运行态 → DTO | `(st *composeState) draftEntry() draftEntry` | 各 `xxxOpen` 为假时不写对应块；`Body` 先经 `stripUploadPlaceholders` |
| DTO → 运行态 | `(st *composeState) applyDraft(e draftEntry)` | 全量覆盖：`*st = *newComposeState()` 后逐字段填回，最后清幂等键、停节点轮询、清回执 |
| 运行态 → DTO | `(d *detailState) draftEntry() draftEntry` | 带上 `d.topic.Title` 作为 `PostTitle` 快照；`replyTo` 全字段照抄 |
| DTO → 运行态 | `(d *detailState) applyReplyDraft(e draftEntry)` | 写 `draft` / `replyTo`，`composerIdle = false`，`focusComposer = true` |

`applyDraft` 的注意点（顺序错了就是难查的线上问题）：

1. 先 `a.stopComposeNodeWatch()` 之类停掉旧表单在跑的轮询，再覆盖状态。
2. 覆盖后清 `key` / `keySig`：旧幂等键配新内容会被服务端判成 `idempotency_key_reuse`。
3. 清 `okTitle` / `okShortID` / `okURL` / `okPending`：那是上一帖的回执。
4. 清 `mdEditor` 的选区、面板与预览态（`*st = *newComposeState()` 已包含）。
5. `draftID = e.ID`，`draftFP` / `draftSavedFP` 都设成刚算出的指纹，恢复这一下不该立刻再写一次盘。
6. `focusTitle = strings.TrimSpace(e.Title) == ""`：有标题就把光标放进正文。

### 4.4 上限与清理

| 项 | 上限 | 超出时 |
| --- | --- | --- |
| 单条正文 / 动态 | `composeContentMax` / `composeMomentMax`（与表单一致） | 保存前截断，记日志 |
| 回复草稿条数 | 200 | 按 `updated_at` 丢最旧的 |
| 文件大小 | 4 MiB | 丢最旧的回复草稿直到装下 |
| 发帖草稿条数 | 50 | 同上 |

清理只在保存时做，不做定时任务：草稿是用户内容，静默删除必须留日志。

### 4.5 指纹：判断「要不要落盘」

不给每个输入框挂 `OnChange`——发帖表单有二十多个输入点（标题、正文、动态、投票选项、竞猜选项、兑换码、说明、隐藏内容、门槛数值……），漏一个就丢一块内容。改成**帧尾算指纹**：

```go
// draftFingerprint 把这份草稿的可存字段按固定顺序哈希成一个 64 位值。
// 顺序必须与 draftEntry 的字段一一对应，改字段时两边一起改。
func (st *composeState) draftFingerprint() uint64
func (d *detailState) draftFingerprint() uint64
```

- 用 `hash/fnv` 的 64 位变体，字符串按 `字段值 + 0` 写入，数值与布尔用固定字节，避免「12|3」与「1|23」撞在一起。
- 发帖窗口每次重建都会走到帧尾（字数计数器、预览、按钮状态都依赖重建，输入必然触发重建），所以插入表情、粘贴、图片占位符替换、选节点、开关组件这些非键入改动同样能被捕捉。
- 单帧成本：正文上限 20000 字，哈希一遍是几十微秒量级，远低于一次布局。

运行态各加两个字段：

```go
type composeState struct {
    // ...
    draftID      string // 这份表单对应哪条草稿；空表示还没落过盘
    draftFP      uint64 // 本帧的指纹
    draftSavedFP uint64 // 上次落盘时的指纹
}

type detailState struct {
    // ...
    draftFP        uint64
    draftSavedFP   uint64
    focusComposer  bool // 从草稿箱恢复后的一次性聚焦
}
```

## 5. 自动保存

### 5.1 触发链

```text
composeWindowView / detailWindowView / replyWindowView / view 的帧尾
        ↓  a.draftTick()
算指纹 → 与 draftSavedFP 不同 → draftPending = true，记 dirtySince
        ↓
没有定时器就挂一个 AfterFunc（800ms，且不超过 dirtySince+5s）
        ↓  a.publish(...)
a.flushDrafts()：把改动写成 DTO、删掉空条目、a.drafts.save() 落盘
```

`draftTick` 是幂等的：同一帧调几次都只是重算指纹。

### 5.2 为什么是 800ms / 5s

- 800ms 是「停手就存」：正常打字的停顿都在这个量级，用户关窗口、切草稿、被系统杀掉时最多丢一句话。
- 5s 上限是给「一直不停打」的场景兜底：连续输入时定时器不断被重置，没有上限就永远不会落盘，崩溃时会丢掉整段。
- 不做「每次按键都写盘」：草稿文件是整份重写，每键一次既费盘也没必要。

### 5.3 强制落盘点

| 时机 | 位置 | 为什么必须同步落盘 |
| --- | --- | --- |
| 关发帖窗口 | `composeWin.OnClosed` / `closeComposeWindow` | 用户以为「关掉就是存好了」 |
| 关独立写评论窗口 | `replyWin.OnClosed` / `closeReplyWindow` | 同上；草稿仍在 `detailState` 里 |
| 关详情窗口 / 关标签 | `disposeDetailState` 之前 | 状态马上就 `dead`，之后没人能替它落盘 |
| 切详情标签 | `detailTabBar` 切换分支 | 隐藏标签不再重建，指纹不会再被算到 |
| 关主窗口 / 收进托盘 | `onMainWindowClose` 与 `onMainWindowClosed` | 窗口没了就没有帧尾了 |
| 退出账号 | `onAuthChanged` 里 `a.compose = nil` 之前 | 状态马上被丢弃 |
| 退出应用 | `accountBook.markQuitting` 里遍历 `acc.app` | 定时器可能来不及跑 |
| 进入草稿箱 | `showDrafts` | 列表读的是内存 store，先把 live 内容并进去 |
| 打开某条草稿之前 | `restoreDraft` 开头 | 当前正在写的那份不能因为「换了一条」而丢 |
| 删除 / 清空之后 | `deleteDraft` / `clearAllDrafts` | 删完立刻落盘，别等下一次 tick |

### 5.4 什么时候删条目

| 时机 | 位置 | 动作 |
| --- | --- | --- |
| 帖子发布成功 | 发布回调里 `st.resetComposeForm()` 之后 | 删这份发帖草稿，`draftID` 置空 |
| 动态发布成功 | `resetMomentForm` 之后 | 同上 |
| 评论 / 回复发送成功 | `submitReply` 的成功分支（`d.draft = ""` 处） | 删这篇帖子的回复草稿 |
| 用户点「清空」 | `composeFooter` 的清空按钮 | 删这份发帖草稿 |
| 草稿变成空 | `flushDrafts` | 正文全空且没有开着的组件 → 删条目，而不是存一条空草稿 |
| 用户在草稿箱删单条 | 行的删除按钮 | 删条目 + 清 live 状态 |

### 5.5 线程与失败

- 定时器回调只经 `a.publish` 回 UI 线程，和网络回包同一套纪律；`flushDrafts` 只在 UI 线程读写运行态。
- 无头测试（`a.win == nil`）**不挂定时器**：`draftTick` 只置 `draftPending`，由测试显式调 `a.flushDrafts()`，避免定时器与测试线程抢状态。
- 落盘失败（磁盘满、目录只读）只记日志 + 一条 toast「草稿没能保存到本机」，内存里的内容继续可用；下一次 tick 会再试。

## 6. 草稿箱界面

### 6.1 侧边栏入口

导航列表在浏览页之后、分割线之前加一行：

```text
[首页] [社区讨论] [动态] …
草稿箱 (3)          <- lucide.NotebookPen
------------------------------
评分榜
```

- `display.ListItem{Key: "drafts", Label: a.draftsNavLabel(), Icon: lucide.NotebookPen()}`。
- `draftsNavLabel()` 返回 `"草稿箱"` 或 `"草稿箱 (3)"`，写法与 `followsTabLabel` 的未读角标一致：折叠态图标轨也读同一份 `Label`，tooltip 里自然带上数字。
- 数字 = 草稿箱里的行数（发帖 / 动态各算一行，回复每篇算一行），来自内存 store，不需要额外请求。
- 选中时 `navSel = "drafts"`，与 `t<n>` 互斥。

### 6.2 页面结构

`pageName = "drafts"` 时只换右侧内容区，标题栏、侧边栏、分割线都不动：

```text
+--------------------------------------------------------------+
| 草稿箱                                    共 4 条  [清空全部] |
| 只显示「alice」的草稿，保存在这台电脑上                        |
+--------------------------------------------------------------+
| 发帖与动态                                                    |
| [✎] 本地草稿应该怎么存                         帖子 · 3 分钟前 |
|     想聊聊落盘节奏……                             [打开] [删除] |
|--------------------------------------------------------------|
| 回复                                                          |
| [💬] 第 3 条我试了，确实……                      回复 12 楼 · 2 小时前 |
|     2Libra 客户端更新日志                        [打开] [复制] [删除] |
+--------------------------------------------------------------+
```

- 页头：标题 + 条数 + 当前账号标识（`a.accountBadge`）+ 右侧「清空全部」按钮（`action.Button`，`Danger`，0 条时 `Disabled`）。
- 分组标题用 `display.Text`，11px 弱化色，与侧边栏分组同一套节奏；「发帖与动态」为空时不画这一组，两组都空走空态。
- 列表用 `ui.List(c, &a.draftsList, len(rows), ...)`，行表面复用 `feedRowSurface(c)`（与信息流同一种卡片、悬停与圆角），纵向 10、横向 12 的内边距。
- 行内容：
  - 左：`lucide.FilePen()`（发帖）/ `lucide.NotebookPen()`（动态）/ `lucide.MessageSquareText()`（回复），16×16，次要色。
  - 中：标题行——发帖取 `Title`，空则取正文首行；回复取 `PostTitle`（帖子已删也有快照），再空就取正文首行。
  - 摘要：正文压掉换行与 Markdown 标记后截 2 行，`MaxLines(2)`。
  - 元信息（等宽小字）：类型 + `relativeTime(UpdatedAt)` + 字数；回复再加「回复 12 楼 / 回复 老王」。
  - 右：主操作「打开」用文字按钮，其余是 28×28 图标按钮，`Hovered()` 才显示（`display.ListItem.Actions` 同一套行为），删除按钮用 `Danger` 色。
- 行点击 = 打开（与按钮同义），删除按钮必须吃掉这次点击，不能连带打开。

### 6.3 空态与清空

- 空态：`display.Empty{Icon: lucide.NotebookPen(), Title: "还没有草稿", Description: "在发帖窗口或评论框里写下的内容会自动存到这里，关掉窗口、甚至异常退出也不会丢。"}`
- 「清空全部」→ `feedback.Modal`（Title「清空所有草稿？」，Width 420，OkText「清空草稿」，CancelText「取消」）：
  - 正文写清范围与后果：「将删除当前账号的 4 条草稿（发帖 1、动态 0、回复 3），删除后无法恢复。其它账号的草稿不受影响。」
  - 确认后：删文件 + 清 live 状态 + toast「已清空 4 条草稿」。
- 弹窗状态放 `app.draftsClearOpen`，由 `main.go` 的 `dialogs` 画（草稿页走的是同一条 `view` 路径，弹窗照常叠在最上层）。

### 6.4 窄窗口与折叠态

- 侧边栏折叠成图标轨时，「草稿箱」是同一份 `sidebarNavItems` 里的一项，`sidebarRail` 自动画成 ActionIcon + tooltip，不需要另写分支。
- 内容区窄于 640 DIP 时：摘要从 2 行收到 1 行，元信息只留「类型 · 时间」，行右侧的「打开」文字按钮换成图标按钮。
- 草稿页不画右下角的 FAB 列（返回顶部 / 刷新 / 发帖都只对信息流有意义）。

### 6.5 与主窗口其它部分的关系

| 位置 | 改动 |
| --- | --- |
| `view` | 右侧内容区二选一：`drafts` 画草稿页，其余照旧 `body` + `listFabs` |
| `view` 的 `ensureLoaded` | 草稿页跳过：那里没有列表要拉，别白白发请求、也别占限流额度 |
| `selectTab` | 开头补 `a.pageName = "list"`：侧边栏在草稿页是可见的，点任意浏览页必须能回来 |
| `handleSidebarAction` | `case "drafts": a.showDrafts()` |
| `showDrafts` | `flushDrafts()` → `pageName = "drafts"` → `navSel = "drafts"` → `Invalidate` |
| 标题栏搜索 / 账号菜单 / 通知 | 不动：它们在标题栏，草稿页照样可用 |

## 7. 恢复流程

### 7.1 发帖 / 动态草稿

```text
点行「打开」
  → flushDrafts()                       // 先把正在写的那份存好
  → a.compose 为空就 newComposeState()
  → st.applyDraft(entry)                // 全量覆盖，见 4.3
  → st.mode = entry.Mode
  → a.showComposeWindow()               // 已有的窗口就带前台
  → 光标落在标题（标题空）或正文
```

- 已登录直接打开；未登录走现有的 `requestLoginWithScopesFor`，授权回来后窗口照常打开，内容不丢。
- 发帖 FAB（`showCompose`）的行为：`a.compose` 里已经有内容就接着写（现状）；为空时取「最近编辑的一条 `mode=post` 草稿」继续；一条都没有才开空白表单。这样「关窗再点发帖」还是原来那份字。
- 草稿箱页头的「写新帖」（`action.Text` 按钮）走 `newComposeDraft()`：先 flush 当前表单，再换一个新 id 打开空表单，已有草稿原样留在箱子里。

### 7.2 评论 / 回复草稿

```text
点行「打开」
  → flushDrafts()
  → 这篇帖子已经开着详情窗口？是 → 直接 applyReplyDraft 到那份 detailState，切到该标签并前置窗口
  → 否 → a.openDetail(Item{ShortID, Title, URL, CommentID: replyTo.ID})
         → openDetailWindow 建窗口并 loadDetail（带 CommentID 会滚到那层楼）
         → a.detail.shortID 对得上 → applyReplyDraft
```

- `applyReplyDraft` 只做四件事：`draft = Text`、`replyTo = ReplyTo`、`composerIdle = false`、`focusComposer = true`。
- 评论框本来就会因为「有草稿 / 有回复目标」自动展开（`replyInput` 的 `expanded` 判定），不需要额外的展开标记。
- `focusComposer` 由 `replyInput` 消费一次：`area.Focus(c)`（独立窗口路径同理），保证点完「打开」就能接着打字。
- 不恢复：`replyAsID`（临时借用别的账号）、表情 / 图片面板的开关、预览态、`winPreview`。

### 7.3 恢复后的一致性

- 恢复不发起任何网络请求以外的副作用：`openDetail` 照旧拉正文与第一页评论，草稿只改本机状态。
- 恢复不会自动发送：`posting` / `momentBusy` 都是 false。
- 恢复后立刻 `draftTick` 一次，把 `draftSavedFP` 对齐，避免刚打开就写一次盘。

### 7.4 异常情况

| 情况 | 表现 |
| --- | --- |
| 帖子已被删除 / 无权限 | 详情页显示原有错误与「重试」；草稿仍在箱子里，行上「复制」可以先把正文取出来 |
| 帖子已关闭评论 | 草稿照常恢复；发送时由现有逻辑 toast「该帖子已关闭评论」 |
| 被回复的评论已删除 | 「正在回复 12 楼」的快照仍在；发送时服务端拒绝，toast 说明原因，草稿不丢 |
| 草稿里的图片是未完成的上传 | 保存时已剥离占位符，不会出现「还有 N 张图片未处理完」的死锁 |
| 同一篇帖子的回复草稿在独立窗口里 | 打开后进的是页面里的评论框；需要大窗口写再点「独立窗口写评论」，文字跟着过去 |

## 8. 代码改动清单

新增：

- `draft.go`：`draftEntry` / `draftTarget` / `composeDraftModules` 及各自块结构、`draftStore`（load / save / put / delete / clear / rows / count）、`draftScope`、`stripUploadPlaceholders`、两个 `draftFingerprint`、`a.draftTick` / `a.flushDrafts` / `a.clearAllDrafts` / `a.deleteDraft` / `a.restoreDraft`、`liveDetailStates`。
- `draft_ui.go`：`a.draftsBody`（页头 / 分组 / 行 / 空态）、`a.draftRow`、`a.draftsClearDialog`、`a.showDrafts`、`a.newComposeDraft`。
- `draft_test.go`、`draft_ui_test.go`。

修改：

- `main.go`：`app` 增加 `drafts *draftStore`、`draftsList ui.ListState`、`draftsClearOpen bool`、`draftPending bool`、`draftDirtySince time.Time`、`draftTimer *time.Timer`；`view` 的内容区分支与 `ensureLoaded` 跳过；`sidebarNavItems` 加「草稿箱」；`handleSidebarAction` 加分支；`selectTab` 补 `pageName`；`dialogs` 挂清空确认；`onAuthChanged` 退出分支里 `a.compose = nil` 之前 flush。
- `compose.go`：`composeState` 加 `draftID` / `draftFP` / `draftSavedFP`；`applyDraft` / `draftEntry`；`resetComposeForm` 之后清 `draftID`。
- `compose_ui.go`：`composeWindowView` 帧尾 `a.draftTick()`；`OnClosed` flush；发布成功回调里 `deleteDraft`；「清空」按钮里 `deleteDraft`；`showComposeWindow` 按「最近一条草稿」续写。
- `detail_ui.go`：`detailState` 加 `draftFP` / `draftSavedFP` / `focusComposer`；`replyTextArea` 消费 `focusComposer`；`submitReply` 成功分支删草稿；`detailWindowView` 帧尾 tick；`detailTabBar` 切换前 flush。
- `detail_window.go`：`disposeDetailState` 开头 flush 该篇。
- `reply_window.go`：`replyWindowView` 帧尾 tick；`OnClosed` flush。
- `app_window.go`：`onMainWindowClose` / `onMainWindowClosed` / `finalizeAccountWindow` flush 并停定时器。
- `accounts.go`：`markQuitting` 里对每个 `acc.app` 调 `flushDrafts()`；`logoutAccount` 不删草稿文件（加一行注释说明）。
- `main_test.go` / `sidebar_test.go` / `compose_test.go` / `detail_test.go`：补断言，见第 10 节。

不改：`offline_store.go`（不复用 `localKV`）、所有网络出口（草稿只写本机）、`proxy.go`。

## 9. 实施阶段

### 阶段 1：存储层

- `draftEntry` 与各组件 DTO、`draftStore` 的读写 / 上限 / 损坏兜底 / 作用域。
- `stripUploadPlaceholders`、`draftScope`。
- 验收：读写往返一致；两个账号互不可见；坏文件不崩；上限按最旧丢。

### 阶段 2：发帖草稿

- `composeState` 的 `draftEntry` / `applyDraft` / 指纹；`draftID` 生命周期。
- 帧尾 tick + 定时器 + 强制落盘点。
- 发布 / 清空后删条目；FAB 续写最近一条。
- 验收：写完关窗、杀进程、重开都还在；发布后箱子里少一条。

### 阶段 3：评论 / 回复草稿

- `detailState` 指纹与 `focusComposer`；发送成功后删除。
- 恢复：已开窗口复用、未开窗口 `openDetail` 带 `CommentID`。
- 验收：写「回复 12 楼」到一半强杀，重开点恢复，文字与回复目标都在，光标在输入框里。

### 阶段 4：草稿箱页面

- 侧边栏入口与角标、`pageName = "drafts"`、页头 / 分组 / 行 / 空态。
- 单条删除、复制、清空确认。
- 验收：不新开窗口；侧边栏仍可用；点浏览页能回去；清空后不再复活。

### 阶段 5：收尾与回归

- 关窗 / 切标签 / 收托盘 / 退出账号 / 退出应用各走一遍落盘。
- `CGO_ENABLED=0 go test ./...`、`go tool mygo vet .`、`gofmt -l .`。
- 深色 / 浅色、窄窗口、侧边栏折叠、多账号窗口并排各看一遍。

## 10. 测试计划

### 单元测试（`draft_test.go`）

- `TestDraftStoreRoundTrip`：写读一致，字段一个不少（含组件块与 `reply_to`）。
- `TestDraftStoreScopedByAccount`：同目录两个 scope 各写一条，互不可见。
- `TestDraftStoreCorruptFileFallsBack`：坏 JSON → 空草稿 + `.bad` 副本，不 panic。
- `TestDraftStorePrunesOldestReply`：第 201 条挤掉最旧的，`updated_at` 顺序正确。
- `TestDraftStripUploadPlaceholders`：`![x](upload:3)` 被剥离，正常图片链接保留。
- `TestComposeDraftEntrySkipsClosedModules`：组件开关关着时不写对应块。
- `TestComposeDraftApplyResetsKeyAndReceipt`：恢复后幂等键与回执为空。
- `TestComposeDraftFingerprintCoversModules`：改投票选项 / 匿名开关 / 节点都会改变指纹。
- `TestReplyDraftFingerprintCoversTarget`：换「回复谁」也会改变指纹。
- `TestClearAllDraftsClearsLiveState`：清空后 live 表单与评论框也空了，再 tick 一次不会写回。
- `TestDeleteDraftClearsLiveCompose`：删单条后同一份内容不会复活。

### 持久化与恢复（同一份 `draft_test.go`）

- `TestComposeDraftSurvivesRestart`：写 → flush → 用同目录新建 app → `rows()` 里有它，`applyDraft` 后字段一致。
- `TestReplyDraftSurvivesRestart`：同上，`reply_to` 的 `floor` / `author` 都在。
- `TestFlushSkipsUnchanged`：指纹没变时不写盘（用一个记录写入次数的测试钩子）。
- `TestEmptyDraftDeletesEntry`：正文清空后 flush → 条目消失。

### UI 测试（`draft_ui_test.go`，`ui.NewTester`）

- `TestSidebarShowsDraftsWithCount`：导航项文案「草稿箱 (2)」，折叠态 rail 也有同一项。
- `TestDraftsPageKeepsSidebarAndOpensNoWindow`：点入口后 `pageName == "drafts"`、`a.win` 未变、`len(a.detailWindows) == 0`，侧边栏项仍画得出来。
- `TestSelectTabLeavesDraftsPage`：草稿页点「首页」→ `pageName == "list"`，`navSel == "t0"`。
- `TestDraftsRowOpensCompose`：点发帖草稿行 → 发帖窗口状态被填满，标题 / 正文 / 组件都在。
- `TestDraftsRowOpensReplyDraft`：点回复草稿行 → 详情窗口打开、`draft` 与 `replyTo` 就位、`focusComposer` 被消费。
- `TestDraftsRowReusesOpenDetailWindow`：同一篇已开着 → 不新建窗口，只切标签。
- `TestDraftsClearNeedsConfirm`：点「清空全部」只弹确认；取消后条目还在。
- `TestDraftsClearAllRemovesEverything`：确认后 `rows()` 为空、文件删除、live 状态清空。
- `TestDraftsDeleteSingleRow`：删一行只影响那一行。
- `TestDraftsCopyRow`：复制把正文放进剪贴板（用现有剪贴板测试替身）。
- `TestDraftsEmptyState`：空箱子画 `display.Empty`，不画空列表。

### 回归

- 现有 `compose_test.go` 的发布 / 清空 / 恢复表单断言全部通过（`resetComposeForm` 语义不变）。
- 现有 `detail_test.go` 的评论发送断言通过（发送成功仍清 `draft` 与 `replyTo`）。
- `sidebar_test.go` 的导航项顺序断言按新列表更新（草稿箱在分割线之前）。
- 网络用例零新增：本方案没有新端点，`proxy_test.go` 不需要改。

## 11. 验收标准

1. 侧边栏有「草稿箱」，带条数；点进去是主窗口内的页面，任务栏与窗口数不变。
2. 草稿箱只显示当前账号的草稿；换一个账号窗口看，看到的是另一个账号的草稿（或空）。
3. 发帖 / 动态写到一半关窗、结束进程、断电重启，重开点「打开」内容原样回来（含节点、匿名、投票等组件）。
4. 在评论框写一段「回复 12 楼」的话，强杀进程；重开后从草稿箱进去，正文、「回复 12 楼」的提示、光标位置都对。
5. 发布帖子 / 发出评论后，对应草稿从箱子里消失，不会重复出现。
6. 「清空全部」先确认再删；删完箱子里为空、文件为空，重启也不复活。
7. 空箱子有明确空态；窄窗口与折叠侧边栏不溢出、不出现被裁的行。
8. `go test ./...`、`go tool mygo vet .`、`gofmt -l .` 全绿。

## 12. 风险与对策

| 风险 | 对策 |
| --- | --- |
| 逐键写盘拖慢输入 | 800ms 防抖 + 5s 上限，只在指纹变化时写 |
| 每帧算指纹的开销 | 只对开着的编辑面算；FNV 一遍 20000 字是几十微秒 |
| 定时器与 UI 线程抢状态 | 回调只经 `publish`；无头测试不挂定时器，走显式 flush |
| 清空 / 删除后又被写回 | 删条目时同步清 live 状态并把 `draftSavedFP` 对齐 |
| 恢复覆盖用户正在写的内容 | 打开任何草稿前先 flush；同一份草稿只有一处输入框 |
| 关详情窗口时状态已 `dead`，落盘丢了 | `disposeDetailState` 开头先 flush |
| 未上传图片留下死占位符 | 保存时剥离 `upload:` 占位符 |
| 帖子被删 / 评论被删 | 保留快照与正文，行上提供「复制」；发送失败照旧 toast |
| 草稿文件被手工改坏 | 另存 `.bad` 后从空开始，记日志，不阻塞启动 |
| 磁盘写失败 | 日志 + toast，内存内容继续可用，下次 tick 重试 |
| 多份发帖草稿让 FAB 行为变复杂 | 明确规则：有内容接着写 → 否则最近一条 → 否则空白；「写新帖」强制新开 |
| 退出账号后草稿成了孤儿文件 | 账号 id 由昵称哈希得出，重新登录同昵称即取回；不做自动清理 |

## 13. 本次不做

- 不做草稿重命名、置顶、分组、搜索；草稿箱按时间倒序 + 两类分组。
- 不做云端同步、跨设备草稿、多窗口实时共享同一份草稿。
- 不做草稿版本历史与「撤销删除」；清空与删除都不可恢复（确认弹窗里写明）。
- 不做未登录窗口 → 首个账号的草稿迁移（`guest` 作用域独立保留）。
- 不保存未上传完成的图片本体，也不恢复上传队列。
- 不恢复「换个账号回复」的临时身份，不恢复表情 / 图片面板的展开状态。
- 不改 `offline_store.go` 的 `localKV`，也不为草稿引入 SQLite。
