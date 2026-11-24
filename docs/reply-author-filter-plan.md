# 评论区「只看楼主 / 只看某人」设计

- 状态：已实施
- 日期：2026-11-24
- 范围：详情页评论区的按作者过滤、工具条与头像右键入口、分页续拉、定位与视图切换
- 约束：纯客户端过滤，不新增网络出口；不落盘；不改服务端数据

## 1. 目标

长帖评论过百时，用户只想看发帖人自己的补充，或只想看某个人的发言。现在只能手动翻。

要补的是：

1. 评论工具条提供「只看楼主」开关，一眼看到楼主的所有回复。
2. 评论头像右键菜单提供「只看 ta 的回复」，用于任意用户。
3. 过滤后仍能继续加载后面的页，不会出现「只显示了已加载部分」的错觉。
4. 从通知 / 列表定位到某条评论时，过滤自动让位，不能把目标行藏起来。

## 2. 非目标

- 不做服务端过滤参数（`GET /v1/topics/:short_id/replies` 没有作者筛选参数）。
- 不按账号持久化过滤对象：这是「这篇帖子的临时视角」，不是账号习惯。
- 不把过滤结果用于「只看某人的所有帖子」；那是用户资料卡已有的能力。
- 不改评论树的数据结构、不改 `replyList` 本体，只在显示层过滤。

## 3. 现状与代码位置

| 现有实现 | 位置 | 说明 |
| --- | --- | --- |
| 评论状态 | `detail_ui.go:50-63` | `replyList`、`display`、`replyPage/Total/TotalPages`、`replyNoMore`、`replyLoading` |
| 显示列表重建 | `reply_view.go:159-187`（`rebuildDisplay`） | 现在只处理折叠，`display` 是所有滚动定位与行数的唯一依据 |
| 楼主判断 | `detail_ui.go:2142-2150`（`isTopicAuthor`） | 有作者 id 按 id，缺 id 退回昵称；匿名帖不参与 |
| 评论行 | `detail_ui.go:2289-2310`（`replyRow`） | 行缩进与树形导轨读 `r.Depth` |
| 评论工具条 | `detail_ui.go:1939-1995`（`replyComposerToolBar`） | 现有三枚：回顶、视图切换、刷新 |
| 头像右键菜单 | `avatar_context_menu.go:18-27` | 现有键：`at`、`follow`、`block` |
| 分页续拉 | `detail_ui.go:719-830`（`loadRepliesFor`）、`detail_ui.go:2136-2140`（`replyCanLoadMore`） | 无限滚动，`AtEnd` 触发 |
| 打开详情 | `detail_ui.go:534-537` | `flat = it.CommentID != ""`，定位到具体评论时用扁平视图 |

## 4. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 过滤状态 | `detailState.authorFilter`：空串表示不过滤；另有 `authorFilterID`、`authorFilterIsOP`、`authorFilterName` 用于判断与文案 |
| 2 | 过滤口径 | 与 `isTopicAuthor` 一致：id 优先，缺 id 按昵称；匿名帖的「匿名」不匹配 |
| 3 | 显示形态 | 过滤态强制平铺：保留行 `Depth=0`，保留原楼层号，按原顺序排列；退出过滤恢复原树形 |
| 4 | 置顶评论 | 同样按作者过滤，不特殊保留 |
| 5 | 入口一 | 工具条新增「只看楼主」图标按钮；过滤态下变成激活样式，再点取消 |
| 6 | 入口二 | 评论头像右键菜单新增「只看 ta 的回复」，位置在分割线之后、关注之前 |
| 7 | 持久化 | 不落盘。关闭详情、切换帖子、从通知定位时清空；刷新评论与切换视图保留 |
| 8 | 续拉上限 | 过滤态下自动续拉最多 10 页；超过后显示「继续加载」按钮，避免为找楼主回复把全帖拉完 |
| 9 | 计数 | 工具条显示「只看楼主 · N 条」；N 是当前已加载里命中的条数，不是全帖命中数 |
| 10 | 空态 | 已加载范围内 0 条时显示「楼主还没有回复」/「ta 还没有回复」，并给「查看全部」 |

## 5. 设计

### 5.1 状态字段

在 `detailState` 增加：

```go
authorFilter      string // 空串表示不过滤；否则是作者昵称
authorFilterID    string // 主编；有 id 时用于比对
authorFilterIsOP  bool   // true 表示入口是「只看楼主」
authorFilterName  string // 展示用昵称（楼主时取当前 topic.Author）
filterAutoPages   int    // 过滤态下已经为过滤自动续拉的页数
```

`newDetailState` / `openDetail` / `openNotificationTarget` 初始化时清零。

### 5.2 过滤算法

在 `rebuildDisplay` 开头插入一层：

```go
func (d *detailState) filterReplies(list []replyItem) []replyItem
```

规则：

- 不过滤时原样返回，保持现在「collapsed 为空就复用切片头」的行为，不动索引。
- 过滤时逐条判断，命中则复制一份并令 `Depth = 0`（只改副本，不改 `replyList`）。
- 命中判断：

```go
func (d *detailState) matchesAuthorFilter(r replyItem) bool {
    if d.authorFilterID != "" {
        if r.AuthorID != "" {
            return r.AuthorID == d.authorFilterID
        }
        // 对方没给 id 时退回昵称，和 isTopicAuthor 同口径
    }
    return d.authorFilter != "" && r.Author == d.authorFilter
}
```

- 匿名帖：`topic.Author == "匿名"` 时不设 `authorFilter`，入口直接禁用（没有「楼主」可看）。
- 过滤后再跑原有折叠算法，`hiddenCount` 基于过滤后的列表计算。
- `display` 仍是唯一的显示依据，滚动定位、行数、`stayID`/`stayIdx` 都继续读它。

### 5.3 工具条入口

`replyComposerToolBar` 的 `items` 增加一枚，位置在「视图切换」之前：

- 图标：`lucide.Users()` 或 `lucide.Filter()`（落地时按 lobe 图标集里已有的选）。
- `Label`：过滤态「取消只看楼主」；平时「只看楼主」。
- `Disabled`：`d.replyLoading || d.replyList == nil || isAnonymousTopic(d.topic)`。
- 点击：
  - 未过滤 → `setAuthorFilter(d, opAuthor)`；
  - 已过滤 → `clearAuthorFilter(d)`。

过滤态视觉：沿用 `action.ActionIconGroup` 的激活能力；若该组件不支持 active，
就在按钮左侧画一条 3 DIP 的主色竖条（与发帖侧栏「已添加」同一语言），不新增 Tooltip。

> AGENTS 约定不新增 Tooltip；按钮必须有可读的无障碍名，但不额外加悬浮说明。

工具条右侧或评论输入框上方显示计数：「只看楼主 · 12 条」。计数来自
`len(d.display)`（过滤后），不引入新字段。

### 5.4 头像右键入口

`avatar_context_menu.go`：

- 新增常量 `detailAvatarFilterKey = "filter"`，文案「只看 ta 的回复」。
- 菜单顺序：`@名字` → 分割线 → `只看 ta 的回复` → `关注` → `屏蔽`。
- 点击后调用同一个 `setAuthorFilter(d, name, id)`，然后收起菜单。
- 自己是评论作者时该项禁用（「只看自己」没有意义），用 `a.isMine(name)` 判断。

### 5.5 续拉

过滤态下沿用现有无限滚动：

- `ui.List` 的 `AtEnd()` 会按 `display` 的条数触发；过滤后条目少，可能立刻到底，
  从而继续拉下一页，这正是不用额外写「找下一楼」逻辑的原因。
- 增加保护：`filterAutoPages >= 10` 时不再自动续拉，在列表底部画「继续加载（还有 N 页）」。
  点击后 `filterAutoPages = 0` 并恢复自动续拉。
- `replyCanLoadMore` 不改；`replyNoMore` 仍然兜底。
- 续拉失败仍走现有底部错误 + 「重试」，不改变重试语义。

### 5.6 定位与切换

- `openNotificationTarget`：设置 `focusID` 时同时 `clearAuthorFilter`，否则目标可能被过滤。
- `openDetail`：新帖一律不过滤。
- 视图切换（楼层 / 扁平）：保留 `authorFilter`，清空列表后重新拉第一页，过滤继续生效；
  `d.flat` 仍按账号落盘（`reply_view.go:95-104`），过滤不影响视图偏好。
- 刷新评论：保留过滤，重新拉第一页。
- 插入新评论：`insertReply` 后 `rebuildDisplay` 会重新过滤；不是过滤对象的新评论不显示，
  但 `replyTotal` 由服务端返回，不手动 +1，避免计数漂移。

### 5.7 空态与提示

过滤后 `len(display) == 0` 且已加载：

- 文案：`只看楼主` → 「楼主还没有回复」；`只看 ta` → 「ta 还没有回复」。
- 一行副文案：「已加载 N 条评论，可能还没到 ta 的楼层。」当 `replyCanLoadMore(d)` 为真时显示。
- 按钮「查看全部」→ `clearAuthorFilter(d)`。

### 5.8 与内容屏蔽的关系

- 先过滤作者，再在 `replyRow` 里处理屏蔽折叠（现有顺序不变）。
- 被屏蔽的评论若命中过滤对象，仍按屏蔽逻辑画成提示行，用户可以手动展开。
- 不因为过滤跳过屏蔽检查。

## 6. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `detail_ui.go` | `detailState` 新字段；`filterReplies`/`matchesAuthorFilter`/`setAuthorFilter`/`clearAuthorFilter`；工具条加一枚；计数与空态 |
| `reply_view.go` | `rebuildDisplay` 先过滤再折叠；`saveReplyView` 不写过滤状态 |
| `avatar_context_menu.go` | 新增「只看 ta 的回复」菜单项与禁用条件 |
| `detail.go` | 不需要改接口；过滤只用已加载数据 |
| `detail_ui_test.go` / 新增 `reply_author_filter_test.go` | 单元与无头 UI 测试 |
| `icons.go` | 若 lobe 图标集没有合适图标，才在本文件加一枚线性图标 |

## 7. 验收条件

### 7.1 单元测试

- [x] `TestFilterRepliesKeepsOnlyAuthor`：3 条评论中楼主 2 条，过滤后只剩 2 条，顺序与楼层号不变。
- [x] `TestFilterRepliesFlattensDepth`：保留行的 `Depth` 全部为 0，`replyList` 本体 `Depth` 不变。
- [x] `TestFilterRepliesByIdBeatsName`：同名不同 id 的用户不被误判；id 缺失时按昵称命中。
- [x] `TestFilterRepliesAnonymousTopic`：匿名帖不产生过滤态，入口禁用。
- [x] `TestFilterRepliesPinned`：置顶评论只在作者命中时保留。
- [x] `TestClearAuthorFilterRestoresTree`：清空过滤后 `display` 与过滤前逐项一致。

### 7.2 UI 测试（`ui.NewTester`）

- [x] 有 3 条评论的详情页，工具条出现「只看楼主」。
- [x] 点击后只剩楼主 2 条；再点恢复 3 条，按钮文案回「只看楼主」。
- [x] 过滤态显示「只看楼主 · 2 条」。
- [x] 右键评论头像出现「只看 ta 的回复」；点击后只剩该作者的评论。
- [x] 过滤后 0 条时出现「楼主还没有回复」与「查看全部」，点「查看全部」恢复全量。
- [x] 从通知定位（`focusID`）打开时过滤被清空，目标行可见。
- [x] 切到扁平视图后过滤仍生效；切回楼层视图仍生效。
- [x] 刷新评论后过滤仍生效，计数按新数据更新。
- [x] 自己评论的右键菜单里「只看 ta 的回复」不可点。

### 7.3 分页验收

- [x] 假服务端第 1 页没有楼主回复、第 2 页有：过滤打开后自动拉第 2 页并显示命中行。
- [x] 连续 10 页都没有命中时停止自动续拉，底部出现「继续加载」；点击后继续拉。
- [x] 续拉失败时底部出现错误与「重试」，过滤态保持。

### 7.4 回归

- [ ] 现有折叠 / 展开、评论定位、已读判断、新评论插入、屏蔽评论展开测试不变。
- [ ] `CGO_ENABLED=0 go test ./...` 通过。
- [ ] `go tool mygo vet .` 无告警。
- [ ] `gofmt -l .` 无输出。

## 8. 风险与回退

| 风险 | 处理 |
| --- | --- |
| 过滤后 `Depth` 压平导致树形导轨错乱 | 过滤态强制 `Depth=0`，不画导轨；验收里单独测 |
| 过滤态下滚动位置跳变 | 过滤切换后 `ScrollTo(0, Start)`；从定位进来时先清过滤再定位 |
| 自动续拉把长帖全拉完 | 10 页上限 + 手动「继续加载」 |
| 楼主用昵称判断时同名误判 | 优先 id；匿名帖禁用入口；现有 `isTopicAuthor` 同口径 |
| 过滤状态被误持久化 | 不写 `replyViewMemory`，`saveReplyView` 只收集 readTo/collapsed |