# 节点筛选与级联选择设计

- 状态：已确认，待实施
- 日期：2026-11-21
- 范围：首页节点筛选、全局节点 Tag、侧边栏节点快捷入口、节点配置弹窗
- 约束：界面优先使用 lobe-mygo；所有出站请求仍走 `proxyManager` 共享 transport

## 1. 目标

1. 首页支持按节点筛选，默认展示全部。
2. 节点选择使用 Lobe 风格的两列级联选择，不再手写一套行样式。
3. 节点 Tag 全局可点，点击后把筛选应用到首页并把首页放到顶部。
4. 侧边栏支持配置节点快捷入口，点击直接进入「首页 + 该节点筛选」。
5. 节点配置弹窗统一管理快捷入口与筛选偏好。
6. 节点选择器支持搜索：按节点名、父节点名与 slug 匹配，发帖与筛选共用。

## 2. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 首页筛选是否跨重启 | 不记忆，启动回「全部」；没有「恢复上次筛选」配置 |
| 2 | 点侧边栏「首页」是否清除筛选 | 不清除，只刷新当前筛选；清除走筛选 chip 的 X |
| 3 | 快捷入口数量 | 最多 8 个 |
| 4 | 排序 | 默认 `sort=value`；排序归每个节点快捷入口自己配置，没有全局排序开关 |
| 5 | 节点选择器搜索 | 级联浮层顶部加搜索框；输入后切换为扁平结果列表；发帖窗口只返回可发布的子节点 |

补充解释：

- 「节点对应的主页分类页面」= 首页 + 该节点筛选，不新开独立分类页。
- 「4 也支持配置筛选」落地为：排序写进每个快捷入口的节点引用，随快捷入口一起落盘；
  从帖子 Tag 等接口引用进入的筛选没有排序偏好，按价值优先。
- 首页筛选不再按账号落盘，也没有启动恢复逻辑。

## 3. 现状与代码位置

| 现有实现 | 位置 | 本次改动 |
| --- | --- | --- |
| 首页 tab 定义 | `api.go` 的 `tabSpecs[0]`，query 固定 `parent_slug=All` | 增加 `FilterByNode`，query 由筛选状态生成 |
| 列表加载 | `main.go` 的 `loadFor` / `loadSpec` / `ensureLoaded` / `refreshTab` | 统一走 `specFor(idx)`，拿到带筛选的 spec |
| 节点表与缓存 | `nodes.go` 的 `loadNodeTree` / `nodeSnapshot` / `storeNodeCache` | 抽出共享 `nodeCatalog`，供发帖、首页、弹窗共用 |
| 两列级联 | `cascader.go`，手写按钮行 | 重写为 Lobe `Popover` + 两列 `display.List` |
| 节点 Tag | `node_tag.go`，只读 `display.Tag` | 增加可点击变体，覆盖全部调用点 |
| 侧边栏 | `main.go` 的 `sidebarNavList` / `sidebarRail` | 增加节点快捷分组与折叠态菜单 |
| 弹窗 | `pandora_dialog.go` 的 `feedback.Modal` 模式 | 新增节点配置弹窗，挂在主窗口 `dialogs` |
| 帖子行节点信息 | `api.go` 的 `itemFrom` 只取节点名 | `Item` 增加 `nodeRef`，供 Tag 点击使用 |
| 评论行节点信息 | `comments.go` 只有 `NodeSlug` | 补 `NodeID` / 父节点 slug，缺的按 slug 查节点表 |

## 4. 交互设计

### 4.1 首页页头筛选

页头保持现有「标题 + 条目数」，标题下增加一行筛选条：

```
首页                                            共 128 条
[ # 全部节点  v ]   [ x ]        <- 有筛选时才出现 x
```

- 默认显示「全部节点」，与当前 `parent_slug=All` 一致。
- 筛选 chip 用 `action.Button`，`Variant: Filled`，`Size: Small`，左侧 `lucide.Hash`，右侧 `lucide.ChevronDown`。
- 有筛选时右侧出现 `action.StyleActionIcon` 的 X，tooltip「清除节点筛选」。
- 点击 chip 打开级联浮层；选择后立即应用、关闭浮层、重载第 1 页并回顶部。
- 浮层宽度 420，最高 320，两列各自滚动：

```
+------------------------------------------------------+
| [ 搜索节点…                                  ]       |
| 全部节点                                  [当前]      |
| ------------------------------                       |
| 生活            >    |  整个「生活」                  |
| 职场            >    |  生活 · 个人生活       458     |
| AI              >    |  生活 · 萌宠           132     |
| ...                  |  ...                          |
| ------------------------------                       |
|                                   管理快捷入口       |
+------------------------------------------------------+
```

- 左列：`全部节点` 置顶；父分类行显示 `FolderTree` 图标与右箭头，点击只展开右列，不直接应用。
- 右列：第一行「整个「X」」应用父分类筛选（`parent_slug`）；子节点行应用 `node_id`；选中项显示 `Check`。
- 没有子节点的父分类是叶子，点左列直接应用父分类筛选。
- 底部只有文字按钮「管理快捷入口」，打开配置弹窗；排序不在这里切换，归节点快捷入口自己配置。
- 浮层内的文案只保留必要标签，不写说明段落。

#### 4.1.1 搜索

- 浮层顶部固定一个 `field.SearchBar`（`Size: Small`，`Variant: Filled`），占位文案「搜索节点」，绑定 `tabState.filterSearch`。
- 查询非空时，两列级联整体换成单列扁平结果列表；查询清空后回到级联。
- 匹配字段：子节点名、父节点名、子节点 slug、父节点 slug、拼接后的「父 · 子」。大小写不敏感，中文按子串匹配。
- 结果行仍用 `display.List`：
  - 父分类：`FolderTree` 图标 + 分类名，右侧小字「分类」。
  - 子节点：`Hash` 图标 +「父 · 子」，右侧帖子数。
  - 首页里父分类可点，点它按 `parent_slug` 筛；子节点按 `node_id` 筛。
- 发帖窗口（`SelectableParents: false`）里搜索结果只出子节点；父分类只作为分组标题出现，避免选出无法发帖的父分类。
- 结果上限 50 条，超出只显示前 50 条并提示「结果较多，继续输入以缩小范围」。
- 没有命中时显示一行弱化文字「没有匹配的节点」，不画空列表。
- 键盘：输入框里按 Enter 选中第一条结果；Esc 先清空查询，再按一次关闭浮层；Tab 在结果行之间移动。
- 输入法组合期间不处理 Enter，交给输入框自己确认候选词。
- 选中后清空查询并关闭浮层；二级添加面板也是选中即关（见 4.4）。

搜索态线框：

```
+------------------------------------------------------+
| [ 生活                                        x ]    |
| ------------------------------                       |
| 分类  生活                                 分类      |
| #     生活 · 个人生活                      458       |
| #     生活 · 萌宠                          132       |
| #     职场 · 生活观察                       27       |
| ------------------------------                       |
|                                   管理快捷入口       |
+------------------------------------------------------+
```

### 4.2 节点 Tag 全局可点

- `nodeTag` 保留只读版本；新增 `nodeTagAction`：
  - 仍是 `display.Tag`，外面包一层无外观 `ui.ButtonBase`。
  - `Cursor(CursorPointer)`，tooltip「筛选到「节点名」」，无障碍标签「筛选节点：节点名」。
  - hover 时给 Tag 外层加轻微背景或下划线，明确可点。
- 覆盖调用点：
  - 首页 / 列表行：`main.go` 的 `rowWith`，节点名来自 `SubtitlePrefix`。
  - 富行：`rows.go` 的 `richRow`，节点名来自 `rowExtra.NodeLabel`。
  - 详情页：`detail_ui.go` 的 `topicSubtitle`，使用 `topicDetail` 的 `NodeID/NodeSlug/NodeParentSlug/NodeName`。
  - 用户资料卡：`user_card_ui.go` 的帖子行。
- 点击行为统一走 `a.showNodeFeed(ref)`：
  1. 解析 `nodeRef`，必要时用节点表补齐 ID。
  2. `pageName = "list"`，切到首页 tab，更新 `navSel`。
  3. 设置筛选、清空旧列表、`st.list.ScrollTo(0, ui.Start)`。
  4. 立即拉第 1 页。
  5. 从详情窗口触发时 `raiseMainWindow()`，并让主窗口重绘。
- 列表行内的 Tag 必须吃掉这次点击，不能连带打开帖子。实现上用最内层 `ButtonBase` 承接，测试断言帖子详情没有被打开。

### 4.3 侧边栏快捷入口

展开态，在导航列表与底部入口之间：

```
[导航列表...]
------------------------------
节点                    [设置]
  # 生活 · 个人生活
  # AI · 中转站
  # 职场
  + 添加节点快捷入口
```

- 分组标题 11px 弱化色，右侧 `Settings2` 图标按钮，tooltip「配置节点快捷入口」。
- 快捷行用 `display.List`，`Compact: true`：
  - 父分类：`FolderTree` 图标。
  - 子节点：`Hash` 图标。
  - `Active` 命中当前筛选时高亮。
- 空态显示一行 `Plus` +「添加节点快捷入口」，点击打开配置弹窗。
- 点击快捷行 = `showNodeFeed(ref)`，不新开窗口。
- 最多显示 8 行；超过 8 个的旧数据只显示前 8 个，保存时截断。

折叠态：

```
[导航图标...]
--------
  #
```

- 一个 `Hash` ActionIcon，tooltip「节点快捷入口」。
- 点击打开 `overlay.DropdownMenu`，列出快捷入口；末项分隔线后是「配置节点…」。
- 当前筛选命中任一快捷入口时，ActionIcon 保持激活态。

### 4.4 节点配置弹窗

入口：

- 侧边栏「节点」分组右侧的设置图标。
- 首页筛选浮层底部的「管理快捷入口」。
- 侧边栏空态的「添加节点快捷入口」。

结构（`feedback.Modal`，Title「节点快捷入口」，Width 560）：弹窗里只放列表与
入口，不直接画搜索框 / 开关这类表单；表单一律放到二级面板。

```
+--------------------------------------------------+
| 节点快捷入口                                  [x] |
| 侧边栏显示这些节点，最多 8 个。行尾可单独切换排序。|
|                                                  |
| [::] # 生活                          [价值]  [x]  |
| [::] # AI · 中转站                   [最新]  [x]  |
|                                                  |
| [ +  添加节点                              > ]   |
|                                                  |
|                            [取消]  [保存]        |
+--------------------------------------------------+
```

- 「添加节点」打开二级 `feedback.Drawer`（右侧滑入，Title「添加节点」，
  Width 460）：搜索框 + 两列级联都在 drawer 里，不与弹窗内容挤在一起。
  列表最大高度按窗口高度算（`nodeCascadeProps.ListHeight`），不再用 Popover
  的 260 上限，避免抽屉下半截空着、节点被截成几行。

- 快捷入口列表用 `layout.SortableList`：
  - 行内：`GripVertical` 拖拽手柄、节点图标、节点名、该节点的排序小按钮、`X` 移除。
  - 键盘可用 Space 拿起、方向键移动、Esc 放回。
  - 排序小按钮在「价值 / 最新」之间切换，只改这一行，写进该节点的 `sort` 字段。
- 添加节点复用 `nodeCascadePicker` 这一套级联选择，但不显示「全部节点」：
  - 搜索框与两列级联都在 Drawer 内；搜索结果只出子节点，父分类只作分组标题。
  - 选中即加入草稿、清空搜索并关闭 Drawer，回到配置弹窗；重复则 toast「这个节点已经在快捷入口里了」。
  - 达到 8 个后 toast「最多添加 8 个快捷入口」，不再加入。
  - 关闭按钮 / Esc / 点遮罩收起 Drawer，回到配置弹窗。
- 保存才写盘并刷新侧边栏；取消丢弃草稿，二级面板一并关闭。
- 空态用 `display.Empty`：标题「还没有快捷入口」，描述「点「添加节点」从节点目录里挑选。」
- 节点表加载失败时，Drawer 内显示原因与「重试」，不影响已有快捷入口的保存。

### 4.5 交互汇总

| 入口 | 动作 | 结果 |
| --- | --- | --- |
| 任意节点 Tag | 点击 | 主窗口首页 + 该节点筛选 + 回顶部 |
| 侧边栏快捷行 | 点击 | 首页 + 该节点筛选 |
| 侧边栏「首页」 | 点击 | 刷新当前筛选，不清除 |
| 首页筛选 chip | 点击 | 打开级联浮层 |
| 级联「全部节点」 | 点击 | `parent_slug=All`，关闭浮层 |
| 级联父分类 | 点击左列 | 展开右列；叶子则直接筛选 |
| 级联「整个 X」 | 点击 | `parent_slug=<slug>` |
| 级联子节点 | 点击 | `node_id=<uuid>` |
| 级联搜索框 | 输入 | 两列换成扁平结果列表 |
| 搜索结果 | 点击 | 父分类按 `parent_slug`、子节点按 `node_id` 筛选，关闭浮层 |
| 搜索框 Enter | 按下 | 选中第一条结果 |
| 配置弹窗搜索选中 | 点击 | 加入草稿，清空搜索，关闭 Drawer |
| 节点行排序按钮 | 点击 | 只切这个节点的价值 / 最新，随快捷入口保存 |
| 筛选 chip X | 点击 | 回到全部 |
| 配置弹窗保存 | 点击 | 写入快捷入口（含各自的排序） |

## 5. 数据模型

### 5.1 节点引用与筛选

```go
// nodeRef 是一个可以筛到首页的节点引用。ID 是 node_id（uuid），
// Slug 是节点自身 slug，ParentSlug 是父节点 slug。
type nodeRef struct {
    ID         string `json:"id,omitempty"`
    Slug       string `json:"slug,omitempty"`
    ParentSlug string `json:"parent_slug,omitempty"`
    Name       string `json:"name,omitempty"`
    ParentName string `json:"parent_name,omitempty"`
    Sort       string `json:"sort,omitempty"` // 该节点自己的排序：value | latest
}

type nodeFilterKind uint8

const (
    nodeFilterAll nodeFilterKind = iota
    nodeFilterParent
    nodeFilterNode
)

type nodeFilter struct {
    Kind nodeFilterKind
    Ref  nodeRef
}
```

- `nodeFilterAll`：不带节点条件，发 `parent_slug=All`。
- `nodeFilterParent`：按父分类，发 `parent_slug=<Ref.Slug>`。
- `nodeFilterNode`：按具体节点，发 `node_id=<Ref.ID>`。

标签显示：

- 全部：「全部节点」。
- 父分类：父分类名；名字缺失时退回 slug。
- 子节点：「父 · 子」；父名缺失时只显示子名。

`tabState` 新增四个只在 UI 线程读写的字段：

```go
type tabState struct {
    // ...现有字段
    filter       nodeFilter // 首页当前筛选
    filterOpen   bool       // 筛选浮层是否打开
    filterSearch string     // 搜索框；关闭浮层时清空
    filterRoot   string     // 左列展开的父 slug
}
```

### 5.2 首页 query

保持参数顺序与现有默认请求逐字节一致：

```go
func homeQuery(f nodeFilter) string {
    q := "page=1&limit=50&"
    switch f.Kind {
    case nodeFilterParent:
        q += "parent_slug=" + url.QueryEscape(f.Ref.Slug)
    case nodeFilterNode:
        q += "node_id=" + url.QueryEscape(f.Ref.ID)
    default:
        q += "parent_slug=All"
    }
    return q + "&sort=" + url.QueryEscape(validNodeSort(f.Ref.Sort))
}
```

三种结果：

```text
page=1&limit=50&parent_slug=All&sort=value
page=1&limit=50&parent_slug=life&sort=value
page=1&limit=50&node_id=uuid-work&sort=value
page=1&limit=50&node_id=uuid-work&sort=latest   // 该快捷入口自己配成「最新」
```

第 2 页继续用现有 `endpoint` 只替换 `page`，筛选条件留在 `spec.Query` 里，不会丢。

### 5.3 tabSpec 标记

```go
type tabSpec struct {
    // ...现有字段
    FilterByNode bool // 只有首页为 true；社区讨论同样走 /v1/topics，但不吃首页筛选
}
```

`specFor(idx)` 是唯一入口：

```go
func (a *app) specFor(idx int) tabSpec {
    spec := tabSpecs[idx]
    if spec.FilterByNode {
        st := a.tabs[idx]
        spec.Query = homeQuery(st.homeFilterNode)
    }
    if spec.Kind == kindNotification {
        st := a.tabs[idx]
        spec.Query = notificationsQuery(st.filter, st.keyword)
    }
    return spec
}
```

`loadFor`、`ensureLoaded`、`refreshTab`、`body` 里的 `tabSpecs[idx]` 都改成 `a.specFor(idx)`，避免出现「列表用了筛选、鉴权判断没用」的错位。

### 5.4 Item 节点字段

`Item` 增加：

```go
type Item struct {
    // ...现有字段
    Node nodeRef
}
```

解码规则（`itemFrom`）：

1. 优先读 `node`，其次读 `post.node` / `topic.node`。
2. `ID` 取 `id`，`Slug` 取 `slug`，`ParentSlug` 取 `parent_slug`。
3. 父节点名优先读 `node.parent.name` / `parent_node.name`。
4. 列表响应没有父名时留空，显示时按需从节点表补。

评论行（`commentItem`）：

- 已有 `NodeSlug`；补 `NodeID` 与 `ParentNodeSlug`（响应里有就取）。
- 没有 ID 时，点击 Tag 先按 slug 查节点表。

### 5.5 节点表

新增共享 `nodeCatalog`，放在 `nodes.go` 或新文件 `node_catalog.go`：

```go
type nodeCatalog struct {
    nodes    []composeNode
    at       time.Time
    loaded   bool
    loading  bool
    err      string
    failedAt time.Time
    seq      uint64
}
```

- `ensure()`：先读 `nodeSnapshot()`；新鲜期内直接用；失败冷却 1 分钟；同一时刻只允许一个请求。
- 成功后 `storeNodeCache()`，与发帖窗口共享同一份内存与磁盘缓存。
- 发帖窗口的 `ensureComposeNodes` 改为委托 `nodeCatalog.ensure()`，`composeState.nodes` 只作为渲染副本，避免两处并发请求。
- 首页级联、配置弹窗、Tag 解析都读同一份 `nodeCatalog`。

### 5.6 偏好与快捷入口持久化

存储键：

- `node.shortcuts:v1`：快捷入口（本机共享），每项自带排序。

结构：

```go
type nodeShortcutFile struct {
    Version int       `json:"version"`
    Items   []nodeRef `json:"items"` // nodeRef.Sort 为 value / latest
}
```

旧文件里的 `prefs` 与按账号存的 `node.lastfilter:v1:*` 不再读取；它们在磁盘上
留着无妨，启动一律回默认筛选。

- 快捷入口只存引用与排序，不存令牌、不存帖子内容。
- 读取失败或版本不认识时按空列表处理，并记日志，不阻塞界面。
- 保存时去重（按 `ID`，没有 ID 时按 `Slug`）并截断到 8 个。
- 启动时首页总是默认筛选，不读也不写上次筛选；排序随每个快捷入口的 `sort` 字段一起存。

## 6. 组件设计

### 6.1 `nodeCascader`

替换 `cascader.go` 的手写行，保留两列语义：

```go
type nodeCascadeProps struct {
    Value             *nodeFilter
    Open              *bool
    ActiveRoot        *string
    Search            *string // 搜索框绑定；空串显示两列级联
    AllowAll          bool    // 首页：显示「全部节点」
    SelectableParents bool    // 首页：父分类本身可筛；发帖窗口为 false
    KeepOpenOnPick    bool    // 保留：部分调用方选完不收起；配置弹窗已改用 Drawer
    ListHeight        float32 // 二级面板按窗口高度传，Popover 用默认 260
    OnManage          func()
}

func nodeCascader(c *ui.Context, nodes []composeNode, p nodeCascadeProps)
```

- 触发框：`action.Button`，`Variant: Filled`，显示当前筛选标签与 `ChevronDown`。
- 浮层：`lobeoverlay.Popover`，顶部 `field.SearchBar`，下面按 `*p.Search` 二选一：
  - 空查询：两列 `display.List`，左列「全部节点」+ 父分类，右列「整个 X」+ 子节点。
  - 非空查询：单列扁平结果列表，行内带「分类 / 节点」小字与帖子数。
- `display.List` 自带 hover、active、键盘与无障碍行为，不再手写 `ButtonBase` 行。
- 两列与结果列表都放进 `ui.Scroll`，限高由 `ListHeight` 决定（Popover 默认 260，抽屉按窗口高度算），窗口缩小时不撑破窗口。
- `OnManage` 非空时底部画「管理快捷入口」；排序不放这里，归节点快捷入口自己配。
- 发帖窗口使用 `AllowAll: false`、`SelectableParents: false`，选中值仍是节点 uuid；首页使用 `AllowAll: true`、`SelectableParents: true`。
- 搜索匹配由 `nodeSearch(nodes, query)` 负责，返回有序命中；组件只负责画。

```go
type nodeHit struct {
    Ref      nodeRef
    IsParent bool
}

// nodeSearch 先匹配自身名字与 slug，再匹配父名；父分类命中排在前，
// 子节点保持节点表顺序。空查询返回 nil。
func nodeSearch(nodes []composeNode, query string) []nodeHit
```

### 6.2 `nodeTagAction`

```go
type nodeTagProps struct {
    Label  string
    Ref    nodeRef
    OnPick func(nodeRef)
}

func nodeTagAction(c *ui.Context, p nodeTagProps) ui.Element
```

- `Ref` 为空或 `OnPick` 为 nil 时退回只读 `nodeTag`。
- 外层 `ui.ButtonBase` 只负责点击与 tooltip，视觉仍是 `display.Tag`。
- 标签文本优先用传入的 `Label`，保持详情页「父 · 子」与列表页现有文案不变。

### 6.3 侧边栏分组

```go
func (a *app) sidebarNodeSection(c *ui.Context, nav layout.SideNavState)
func (a *app) sidebarNodeRail(c *ui.Context)
```

- 展开态：分隔线 + 标题行 + `display.List` + 空态按钮。
- 折叠态：分隔线 + 单个 ActionIcon + `DropdownMenu`。
- 都挂在 `sidebarNavList` 的同一个 `Column` 里；`DraggableSideNav` 的 body 本身已包在 `ui.Scroll` 中，不需要再加滚动容器。

### 6.4 节点配置弹窗

```go
type nodeEditor struct {
    open   bool
    draft  []nodeRef
    pick   nodeFilter
    pickOpen bool
    activeRoot string
    err    string
}

func (a *app) nodeEditorDialog(c *ui.Context)
```

- 挂在 `main.go` 的 `dialogs`，与潘多拉弹窗同层。
- 打开时把持久化列表复制到 `draft`；保存时统一校验、去重、截断、写盘。
- 节点表未加载时调用 `nodeCatalog.ensure()`，添加区显示加载中或失败原因。
- 从其它窗口打开时先 `raiseMainWindow()`，再置 `open`。

## 7. 代码改动清单

新增：

- `node_filter.go`：`nodeRef`、`nodeFilter`、`homeQuery`、`specFor`、`showNodeFeed`、引用解析。
- `node_catalog.go`：共享节点目录加载与单飞控制。
- `node_cascader.go`：Lobe 两列级联组件（替换 `cascader.go`）。
- `node_search.go`：节点搜索匹配、排序与结果归一。
- `node_shortcuts.go`：快捷入口存储、侧边栏分组、配置弹窗。
- `node_filter_test.go`、`node_cascader_test.go`、`node_search_test.go`、`node_shortcuts_test.go`、`node_tag_test.go`。

修改：

- `api.go`：`tabSpec.FilterByNode`；`Item.Node`；`itemFrom` 解码节点引用。
- `main.go`：`app` 增加 `nodeCat`、`nodeShortcuts`、`nodeEditor`；`specFor` 接入；页头筛选条；侧边栏分组；`dialogs` 挂弹窗。
- `node_tag.go`：可点击变体。
- `rows.go`、`detail_ui.go`、`user_card_ui.go`：Tag 传引用与回调。
- `comments.go`：评论节点引用补字段。
- `compose.go`、`compose_ui.go`：改用共享节点目录与新级联组件。
- `main_test.go`、`sidebar_test.go`、`rows_test.go`、`detail_test.go`、`user_ui_test.go`：按新结构补断言。

## 8. 实施阶段

### 阶段 1：数据层

- 定义 `nodeRef`（含自己的 `sort`）/ `nodeFilter`。
- 实现 `homeQuery` 与 `specFor`，接入 `loadFor` / `ensureLoaded` / `refreshTab`。
- `Item` 与评论行补节点引用。
- 验收：默认首页请求与现在完全一致；父/子筛选请求参数正确；第 2 页不丢筛选。

### 阶段 2：共享节点目录与级联组件

- 抽出 `nodeCatalog`，发帖窗口改为委托加载。
- 用 `Popover` + 两列 `display.List` 重写级联，发帖窗口先切过去。
- 加 `field.SearchBar` 与 `nodeSearch`：空查询走两列，非空走扁平结果。
- 验收：发帖选节点行为不变；节点表只发一次请求；级联键盘与 hover 正常；搜索能按节点名、父名、slug 命中。

### 阶段 3：首页筛选

- 页头筛选 chip、级联浮层、清除按钮；排序不在这里。
- `showNodeFeed` 统一处理切页、清列表、回顶、加载。
- 验收：三种筛选都能落到正确请求；列表回顶；chip 文案正确。

### 阶段 4：全局 Tag

- `nodeTagAction` 与全部调用点。
- 详情窗口点击后主窗口前置。
- 验收：Tag 点击不误开帖子；详情、评论、资料卡、列表都能筛选。

### 阶段 5：侧边栏快捷入口

- 存储读写、展开/折叠两种形态、高亮、空态。
- 验收：点击生效；重启后仍在；超过 8 个截断。

### 阶段 6：配置弹窗

- 草稿增删、拖拽排序、去重、上限、每行自己的排序切换、保存/取消。
- 添加区的搜索：选中即清空查询并关闭 Drawer，回到配置弹窗。
- 验收：取消不写盘；保存后侧边栏立即更新；排序只改对应节点；搜索添加后直接回到弹窗。

### 阶段 7：回归与打磨

- `CGO_ENABLED=0 go test ./...`
- `go tool mygo vet .`
- `gofmt -l .` 无输出
- 深色/浅色、窄窗口、侧边栏折叠、详情窗口多开各走一遍。
- 新增网络出口检查：本方案没有新端点，仍走现有客户端。

## 9. 测试计划

### 单元测试

- `TestHomeQueryDefaultUnchanged`：默认 query 逐字节等于现有值。
- `TestHomeQueryParentAndNode`：父分类与子节点 query 正确，互斥参数不残留。
- `TestHomeQueryPageTwoKeepsFilter`：第 2 页仍带筛选。
- `TestNodeFilterLabel`：全部 / 父 / 子的显示文案。
- `TestResolveNodeRefBySlug`：只有 slug 时能从节点表补 ID。
- `TestResolveNodeRefAmbiguousName`：同名节点不瞎选，返回失败。
- `TestNodeSearchMatchesNameParentAndSlug`：节点名、父名、slug 都能命中，大小写不敏感。
- `TestNodeSearchParentFirst`：父分类命中排在子节点前，子节点保持原顺序。
- `TestNodeSearchComposeOnlyChildren`：发帖模式不返回可选的父分类。
- `TestNodeSearchLimit`：超过 50 条时截断并给提示。
- `TestNodeSearchEmptyAndWhitespace`：空串与纯空白回到级联。
- `TestShortcutStorePersistAndCap`：保存、去重、截断 8 个、重启读取。
- `TestLastFilterPerAccount`：不同账号的上次筛选互不影响。

### UI 测试（`ui.NewTester`）

- `TestNodeTagClickFiltersHomepage`：点 Tag 后请求带 `node_id`，首页回顶。
- `TestNodeTagInsideRowDoesNotOpenPost`：点行内 Tag 不会打开帖子详情。
- `TestFilterChipOpensCascade`：chip 打开浮层，选父/子/全部都生效。
- `TestFilterSearchSwitchesToResults`：输入后两列消失、结果列表出现，清空后恢复。
- `TestFilterSearchEnterSelectsFirst`：搜索框回车选中第一条，请求参数正确。
- `TestFilterSearchSelectParentAndChild`：结果里点父分类与子节点分别走 `parent_slug` / `node_id`。
- `TestSidebarShortcutClick`：快捷行点击后首页筛选正确，请求带上该节点自己的排序。
- `TestSidebarShortcutRailMenu`：折叠态菜单列出快捷入口。
- `TestNodeEditorAddRemoveReorder`：添加、移除、拖拽排序都写进草稿。
- `TestNodeEditorCancelDiscards`：取消后侧边栏不变。
- `TestNodeEditorLimitAndDuplicate`：第 9 个与重复项被拒绝并提示。
- `TestNodeEditorSearchAddsAndCloses`：搜索选中后加入草稿、清空查询并关闭 Drawer。
- `TestNodeEditorPerNodeSort`：节点行的小按钮只切自己，不出现全局排序开关。
- `TestShortcutStorePersistAndCap`：排序随快捷入口落盘，重启后还在。
- `TestStartupHomeFilterIsDefault`：启动不恢复上次筛选。
- `TestFilterPopoverHasNoGlobalSort`：首页筛选浮层没有排序开关，默认价值优先。

### 回归

- 现有 `main_test.go` 的首页请求断言保持通过。
- 发帖窗口节点选择、节点简介、AI 分区轮询不受影响。
- `proxy_test.go` 不需要新增用例（没有新出口），但确认所有请求仍走共享客户端。

## 10. 验收标准

1. 默认启动：首页请求 `page=1&limit=50&parent_slug=All&sort=value`，界面显示「全部节点」。
2. 任意节点 Tag：主窗口切到首页、筛选 chip 显示该节点、列表回到顶部、请求参数正确。
3. 侧边栏快捷入口：点击即筛选；当前项高亮；重启后仍在；最多 8 个。
4. 级联选择：全部 / 父分类 / 子节点三种都可用；父分类叶子可直接选；第 2 页不丢筛选。
5. 搜索：节点名、父名、slug 都能命中；父分类与子节点结果分别走 `parent_slug` / `node_id`；Enter 选第一条；发帖窗口只出子节点；无结果有提示；清空后回到级联。
6. 配置弹窗：增删、每行排序切换、去重、上限、取消、保存都符合预期；启动时首页回默认，不恢复上次筛选；搜索添加后直接回到弹窗。
7. 视觉：全部使用 Lobe 组件与 token；不出现手写行样式；窄窗口与折叠侧边栏不溢出。
8. 质量：`go test ./...`、`go tool mygo vet .`、`gofmt -l .` 全部通过。

## 11. 风险与对策

| 风险 | 对策 |
| --- | --- |
| 行内 Tag 点击连带打开帖子 | Tag 用最内层 `ButtonBase` 承接点击，补 UI 测试 |
| 列表响应缺节点 uuid | 按 slug 查 `nodeCatalog`；查不到就提示并触发加载，不瞎筛 |
| 同名节点无法区分 | 节点表已有 `disambiguateNodeLabels`；解析失败时提示去首页筛选里选 |
| 发帖窗口与首页并发拉节点表 | `nodeCatalog` 单飞 + 共享缓存；发帖窗口委托加载 |
| 浮层在窄窗口被裁 | 两列限高、各自滚动；Popover 走 overlay，不占页头布局 |
| 搜索命中父名时结果过多 | 父分类排最前，子节点按节点表顺序；截断 50 条并提示继续输入 |
| 中文输入法回车误选 | 组合期间不处理 Enter；只在输入框 `Submitted()` 且非组合态时选第一条 |
| 搜索结果随节点表刷新失效 | 每帧从当前 query 重算，不缓存命中列表；关闭浮层时清空 query |
| 筛选与分页错位 | 筛选只改 `spec.Query`，第 2 页由现有 `endpoint` 替换 `page` |
| 持久化文件损坏 | 读取失败按空处理并记日志；保存前做结构校验 |

## 12. 本次不做

- 不做独立节点主页 / 节点详情页，只做首页筛选。
- 不做服务端节点搜索，也不做拼音首字母；只在本机已缓存的节点表内匹配。
- 不做每个快捷入口独立的筛选规则；排序可以独立配置，跟随节点一起存。
- 不改首页默认排序语义；默认仍是价值优先。
- 不新增 API 端点，不新增网络出口。
