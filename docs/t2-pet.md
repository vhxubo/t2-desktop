# t2-pet 开发计划

桌面宠物：一个透明、置顶的小东西，本体是一杆黄铜天平——平时轻轻摇摆，
未读消息变成右盘的砝码，来消息时先一沉再阻尼摆回；收到通知时以「它自己
说话」的气泡讲一句，右键能要一份热榜/最新内容的速览，并记住你关注的人和
话题。

本文是后续开发的依据：**已核对过的事实、绕不过去的约束、以及每一期的验收标准**。
动 `pet*.go` 之前先读完第 1、2 节；排期与验收看第 4 节。

状态：P0 已落地（2026-11-14），P1 起待开发。

---

## 1. 形态决策：不做独立应用

曾经考虑过单独的 `t2-pet` 仓库/安装包。结论是**不做**，理由是四条硬约束，
任何一条都足以否掉：

| # | 约束 | 事实 | 后果 |
| --- | --- | --- | --- |
| 1 | 刷新令牌会轮换 | `auth.go` 刷新处注释：`Every refresh rotates refresh_token` | 两个进程各存一份 `token.json`，谁后刷新谁把对方的 refresh_token 作废 → 用户被踢下线 |
| 2 | 通知靠 SSE 长连接 | `stream.go`；文档限「每用户最多 10 条并发、建连每 (应用,用户) 每分钟 6 次」 | 两条流 = 双倍建连与双倍配额 |
| 3 | 条款禁止多开 | `terms.md` 第五节把「多开应用」列为规避配额 | 用第二个 client_id 分摊请求量属于违规 |
| 4 | 代码共享成本 | 114 个 `.go`、约 4.8 万行、全在 `package main` | 要共享 auth/api/stream 得先抽 `internal/`，比宠物本身大 |

**所以：宠物是 t2-desktop 的第二个窗口，一个安装包，托盘里开关。**

真到了必须拆的那天（宠物要独立分发 / 主程序完全不启动也要宠物 / 两边迭代节奏
打架），做法是先把 `auth`、`api`、`stream`、`cache` 抽成 `internal/` 包，再出两个
`main` 包——**仍然是一个安装包**，不是两个软件。

---

## 2. mygo 约束（已核对 v0.8.3 源码）

这些不是偏好，是框架的硬边界。改动前请重新确认版本与行为。

### 2.1 透明窗口

- `Frameless + Transparent + Content(原生 UI)` 三者同时给，Windows 才走
  DirectComposition 逐像素 alpha（`docs/architecture.md:732`）。
  **DirectComposition 不可用时会退回不透明位图**——这是唯一必须在真机上验的点。
- 必须 `c.Root().Background(ui.Transparent)`，否则整窗被主题底色填满。
- `DisableShadow: true`：窗口是一整块透明，系统阴影只会画出一个方框。
- **Windows 11 的 DWM 会给窗口画一圈 1 DIP 的描边，外加贴着圆角的阴影**（
  `DWMWA_BORDER_COLOR` 深色主题下约 `#404040`；Panel 窗口被 mygo 的 `roundFlyout`
  设成小圆角，阴影就绕着那圈圆角长）。不透明窗口看不出来，透明窗口上就是空心方框
  加一圈灰雾。mygo 没有关掉它们的出口（`frameBorder` 只处理 Windows 10 那条白线），
  所以建窗后调 `removeWindowChrome`（`window_chrome_windows.go`）：圆角设成
  `DWMWCP_DONOTROUND`、边框色设成 `DWMWA_COLOR_NONE`。
  **两样要一起关**：只关描边，阴影还在（实测白底上窗口外仍是 255 → 225 的渐变）；
  圆角一关，窗口外立刻回到纯白。

### 2.2 指针

- **`PassThrough` / `HitTest` 只把指针交给同一窗口内的下层元素，不交给桌面。**
  透明区域的鼠标事件仍然被这个窗口吃掉 → 窗口必须小。当前 120×128。
- **`DragWindow` 与 `OnClick` 在同一元素上互斥**：`input.go:481` 要求该元素除
  `flagDragWindow` 外没有任何 `interactive` 标记（clickable / focusable /
  editable / selectable / draggable / trackPointer 都算）。想两者都要，只能拆成
  两个元素（内层点击、外层拖动），代价是内层那块区域拖不动。
- `ContextMenu` 不设 `interactive` 标记，因此**可以与 `DragWindow` 共存**。

### 2.3 窗口与绘制

- `Panel: true` → Windows 上 `WS_EX_TOOLWINDOW | WS_EX_TOPMOST`，不进任务栏与
  Alt+Tab，浮在其他应用之上。
- 气泡用 `NewFlyout(FlyoutOptions{Parent: 宠物窗口, Focusable: false, FitContent: true})`：
  跟着宠物移动、宠物收起时一起关、**不抢键盘**。`Focusable: false` 是通知类浮层的
  正确选择，别改成 true。
- `WindowOptions` 没有 `Focusable: false` 出口（`window.go:691` 硬编码 true），
  点宠物会让应用获得焦点。交互设计要接受这一点。
- 逐帧动画用 `Painter.AnimationFrameRate(fps)`（只重绘、不重建视图），不要用
  `Context.AnimationFrame()`（那是全速刷新）。

### 2.4 数据与密钥

- `mygo.Secrets`（Windows 凭据管理器 / macOS Keychain / Secret Service）存 AI key。
- 用户令牌目前明文写在 `token.json`（`auth.go:92`，目录 0700、文件 0600）。
  **条款第九节把「明文存储令牌」列为可处置情形**，这是既有待修项，见 §6.4。

---

## 3. 现状（P0，已完成）

### 3.1 文件

| 文件 | 职责 |
| --- | --- |
| `pet.go` | 偏好落盘（`pet.json`）、窗口开关、气泡生命周期、通知播报入口 |
| `pet_ui.go` | 黄铜天平本体与气泡绘制（120×128 基准坐标 + 缩放）；`petMotion` 从时间和状态算每帧姿态 |
| `pet_test.go` | 9 条用例 |
| `main.go` | app 字段、`startPet()`、`publish` 里的 `redrawPetWindow()` |
| `tray.go` | 勾选项 `tray-pet`「桌面宠物」 |
| `notify.go` | `pollNotifications` 里调 `petAnnounce`，**排在 `nativeNotify` 判断之外** |
| `window_mode.go` | `hideMainWindowToTray` 不收宠物（注释说明） |

### 3.2 行为

- 托盘菜单「桌面宠物」勾选 → 宠物出现；再点 → 收起；选择写进 `pet.json`。
- 本体是黄铜古典天平：梯形底座、立柱、支点三角与青色宝石、可绕支点旋转的
  横梁、两根一组的 V 字吊绳、两只始终水平的弧碗托盘。颜色不跟主题走，靠
  深色外沿保证浅色桌面上也有轮廓。
- 动态有两路，叠加在 `petMotion(now, st, unread)` 这一个纯函数里，绘制过程
  不写状态：
  - 时间：6 秒一轮、不超过 1.2° 的微摆，4 秒一轮的上下呼吸，宝石光晕缓慢
    涨落。12 fps 重绘（见 §6.2）。
  - 状态：右盘砝码 = 一颗配重 + 未读数（钳到最多 4 颗），未读越多稳态倾角
    越大（每颗 1.3°）；`st.at`（说一句话的时刻）起叠一段
    `8.5·e^(-1.15t)·cos(6.2t)` 的阻尼摆动，最新的砝码 0.36 秒落进盘里，
    说话时支点亮到满。6 秒后自动衰减干净，不需要收尾回调。
- 收到通知 → 气泡说「@谁 做了什么 + 帖子标题」，最多 3 条时只说最后一条，
  其余折成「另有 N 条」；12 秒后自己收起。
- 点气泡 → 打开对应帖子（走 `openNotificationTarget`）。
- 拖动 → 移动；右键 → 打开主窗口 / 说句话 / 收起。
- 主窗口「收进托盘」时宠物留在桌面；退出应用时一起结束。
- 宠物有独立开关，和「原生通知」互不影响。

### 3.3 已知限制（有意为之，别当成 bug 修）

- 左键点宠物**没有**动作（拖动与点击互斥，选了拖动）。
- 宠物窗口的空白区（约 20 DIP 边距）会吃掉鼠标点击。
- 点击宠物会让应用获得焦点。

---

## 4. 路线图

每期结束都要：`CGO_ENABLED=0 go test ./...`、`go tool mygo vet .`、`gofmt -l .`
无输出；提交信息用英文（见 `AGENTS.md`）。

### P0.5 真机验证（先做，10 分钟）

- [ ] `go run .` 看透明是否真的是逐像素（桌面从宠物周围透出来，不是一个灰框）
- [ ] 置顶是否压得住其它窗口；是否进任务栏/Alt+Tab
- [ ] 拖动时气泡是否跟着走；上方空间不够时气泡是否翻到下面
- [ ] 多显示器、不同 DPI 缩放下位置是否正确（`StateKey` 记住的坐标）
- [ ] 主窗口收进托盘 → 宠物仍在；退出应用 → 一起消失

> 若透明失效（DirectComposition 不可用），退路是给窗口加 `Vibrancy`（Windows 11
> 22H2+），或退化为「不透明圆角卡片 + 宠物」的形态。这是产品决策，别默默改。

### P1 快捷动作与内容速览（无 AI）

目标：右键就能要一份「热榜 / 最新发布 / 最新评论」的速览，宠物用气泡说出来。

- [ ] 右键菜单加三项：`总结热榜`、`最新发布`、`最新评论`
- [ ] 新增 `pet_brief.go`：拉第一页 → 取前 N 条（建议 5）→ 拼成一句话
      （`1. 标题 · 作者` 逐行，最多 5 行），气泡用 `MaxLines` 截断
- [ ] 点击速览条目 → 打开对应帖子（复用 `openDetail` + `showMainWindow`）
- [ ] 缓存：同一类速览 10 分钟内不重复请求（`localKV`，键 `pet:brief:<kind>`）
- [ ] 加载中：宠物「思考」表情 + 气泡「我去看看…」；失败 → toast，气泡不留下错误文案

**数据与配额**（照抄 `tabSpecs`，不要新造参数）：

| 速览 | 端点 | 备注 |
| --- | --- | --- |
| 热榜 | `GET /v1/topics/hot` | 可选 `period=24h`（默认）/ `7d` |
| 最新发布 | `GET /v1/topics/latest` | `limit` ≤ 50 |
| 最新评论 | `GET /v1/comments/latest` | `limit` ≤ 50，列表字段是 `items` |

配额：登录读 600 次/小时/(应用,用户)，匿名读 60 次/分/路由。**不要做定时轮询**，
只在用户点菜单时请求；通知走已有的 SSE。

**验收**：三条菜单项各拉一次，气泡内容与站点首页一致；10 分钟内重复点不发请求；
断网时给 toast 而不是空白气泡。

### P2 记忆系统（无 AI 也能用）

目标：宠物知道你看什么、在意谁。没有 AI key 时靠规则打分，有 key 时再交给模型
（P3）。

#### 2.1 事件日志

`cache/pet/events-<scope>.jsonl`，按 `accountScope()`（`offline_cache.go`）隔离，
未登录记在 `anonymous`。每行一条：

```json
{"at":"2026-11-14T17:30:00+08:00","kind":"open","short_id":"abc","node":"work","tags":["Go"],"author":"jimmy"}
{"at":"...","kind":"read","short_id":"abc","dwell_ms":42000}
{"at":"...","kind":"fav","short_id":"abc"}
{"at":"...","kind":"skip","short_id":"abc","from":"pet"}
```

`kind` 取值：`open` / `read` / `fav` / `bookmark` / `react` / `comment` / `search` /
`skip`（宠物推荐了但没点）。

- [ ] 写入点：`openDetail`（open）、详情页离开（read + dwell）、`interact.go` 的
      收藏/书签/表态、`compose.go` 的评论、`search.go` 的搜索
- [ ] 上限：单文件 5000 行，超过后按天滚动删除最旧的
- [ ] 退出登录/撤销授权 → 整个目录删掉（条款要求）

#### 2.2 兴趣画像

`cache/pet/interest-<scope>.json`，从事件日志物化出来，不是唯一真相：

```json
{"updated":"...","half_life_days":14,
 "nodes":{"work":0.82,"life":0.31},
 "tags":{"Go":1.4,"桌面开发":0.9},
 "authors":{"jimmy":1.1}}
```

- 权重 = Σ 事件权重 × 0.5^(age/半衰期)；`fav`/`comment` 权重高，`open` 低，
  `skip` 为负
- 每次写入事件后增量更新，不重算全量
- [ ] 右键菜单加「我在看什么」：把 top 3 节点 + top 3 标签念一句

#### 2.3 特别关注的人

`cache/pet/watched-<scope>.json`：

```json
{"users":[{"name":"jimmy","note":"Go 桌面开发","notify":true}]}
```

- [ ] 入口：用户资料窗口（`user_card.go`）加「特别关注」
- [ ] 命中判断：通知的 actor 在这个名单里 → 气泡优先展示，标题加「★」，
      不受「最多 3 条」折叠（`petAnnounce` 里先挑关注的）
- [ ] 无通知时的兜底：`GET /v1/users/{name}/topics` 定时检查（**15 分钟一次起**，
      并计入配额；默认关闭，由用户显式开启）
- [ ] 这条链路**不过 AI**：直接展示，快且不花钱

**验收**：收藏 3 篇 Go 帖子后，「我在看什么」说出 Go；把某人加进特别关注，
他回复时气泡带 ★ 且不被其它消息挤掉；换账号后画像与名单都是空的。

### P3 AI 层（可选，没 key 就跳过）

目标：让速览「说人话」，让推荐更贴。**没有 key 时 P1/P2 的功能完整可用**，
这是硬要求——AI 是增强，不是前置依赖。

#### 3.1 Provider 抽象

`pet_ai.go`：

```go
type aiProvider struct {
    baseURL string // OpenAI 兼容：https://api.deepseek.com/v1 等
    model   string
    key     string // mygo.Secrets，键名 "pet-ai-key"
}
```

- 只依赖 `POST {baseURL}/chat/completions`，不绑定具体厂商
- 支持 Ollama（`http://127.0.0.1:11434/v1`，key 留空）——本地模型也算「有 AI」
- 设置窗口加一块「AI（可选）」：地址、模型、key、测试连通性
- key 存 `mygo.Secrets`，**不要**写进 `pet.json` 或日志

#### 3.2 三个用途（都只做这三件）

1. **速览归纳**：把 P1 的 5 条候选压成两三句人话
2. **候选重排**：按兴趣画像给热榜候选打分，取 top 5
3. **兴趣归纳**：把 P2 的画像翻成一句「你最近在看 Go 和桌面开发」

约束：

- 输入只送**公开内容**：标题、节点、标签、前 200 字摘要。**不送通知、不送私信**
- 输出强制 JSON（`{"picks":[{"id":"...","why":"..."}]}`），解析失败退回规则结果
- 超时 3 秒，失败不重试、不阻塞 UI，气泡退回规则文案
- 结果按 `short_id + 模型 + prompt 版本` 缓存到 `cache/pet/summary-<scope>.json`
- 只在**用户点菜单**时调用，不做后台定时总结（那是持续烧钱）

#### 3.3 合规红线（`terms.md`，逐条对照）

- ✅ 允许：对单篇内容做生成式摘要、翻译、排版优化并展示给用户
- ❌ 禁止：全站聚合后**对外发布**（「2Libra 热帖站」）、批量存档、站外镜像
- ❌ 禁止：把内容用于训练或微调模型——选 provider 时要确认其 API 数据不用于训练；
  介意的话用 Ollama 本地跑
- ✅ 展示内容时保留作者昵称与原文链接
- ⚠️ `read:me` 的数据（通知、收藏、关注）只能服务授权本人，不得聚合统计或外泄
- ⚠️ 撤销授权后要及时删除本地令牌与派生数据（画像、事件日志、摘要缓存都算）

### P4 打磨（按需）

- [ ] 勿扰时段：设置里选「23:00–08:00 不冒泡」，通知只进列表
- [ ] 贴边吸附：拖到屏幕边缘时吸附并记住
- [ ] 多套砝码造型：消息 / 金币 / 关注各一种，对应不同的称重语义
      （`petPanWeights` 现在只数未读）
- [ ] 开机自启（如果主程序支持）
- [ ] 设置窗口里加「桌面宠物」一块，与托盘菜单共用 `setPetEnabled`
- [ ] 宠物与系统通知的联动：点系统通知时宠物也做个反应

---

## 5. 测试策略

无头测试能覆盖的和不能覆盖的，写清楚，别写假的「已验证」。

| 能测（`ui.NewTester`） | 不能测 |
| --- | --- |
| 气泡文案、多消息折叠、主题跟随 | 窗口是否真的透明（DirectComposition） |
| 像素：画布有内容、四角 alpha=0、两盘与底座都在 | 是否真的置顶、是否进任务栏 |
| 姿态纯函数：时间微摆、冲击衰减、砝码钳制 | 真机上 12 fps 的实际观感与 CPU/GPU 占用 |
| 偏好落盘、托盘勾选状态 | 拖动时的原生行为、气泡跟随 |
| 自动收起（靠 `onPetBubbleHidden` 钩子 + 真实定时器） | 多显示器/DPI 下的位置 |
| 速览请求的路径与参数（`httptest`） | AI provider 的真实响应质量 |

新增 UI 时的规矩（沿用仓库惯例）：

- 结果用窗口内 toast（`a.toast`），页面里只留「正在做」的进度
- 不要在 app 状态里存 `ui.Element` / `*ui.Context`
- 后台结果经 `publish` / `Window.Update` 回 UI 线程
- 测试断言中文文案，改文案要同步改测试

---

## 6. 待修与风险

### 6.1 必须验的

- **透明窗口在真机上的表现**（P0.5）。无头渲染证明不了 DirectComposition 那条路。

### 6.2 观察中的

- `TestPandoraEntryNameTruncates` 是既有的时间炸弹（`ExpireAt` 硬编码 2026-11-13），
  与本功能无关，但会让 `go test ./...` 偶发失败，干扰判断。
- 宠物窗口 12 fps 重绘：低功耗机器上留意 CPU/GPU 占用，必要时降到 8 fps 或
  只在「说话 / 悬停」时动。

### 6.3 配额

- 登录读 600 次/小时/(应用,用户)；匿名读 60 次/分/路由。
- P1 的速览有缓存、P2 的关注检查默认关闭，都是为了不在这条线上踩雷。
- **永远不要**给宠物加「每隔 N 秒刷新」的定时器。

### 6.4 合规欠账（与本功能相关）

- [ ] 令牌从 `token.json` 明文迁到 `mygo.Secrets`（条款第九节可据此处置应用）。
      迁移要兼容旧文件：读到明文就搬进钥匙串再删文件。

---

## 7. 参考

- 接口文档：<https://r2.2libra.com/openapi/v1/index.md>（分册见 `AGENTS.md`）
- 条款：<https://r2.2libra.com/openapi/v1/terms.md>
- mygo 架构说明：`go list -m -f '{{.Dir}}' github.com/egoist/mygo` 下的
  `docs/architecture.md`（Windows 透明与面板窗口的实现）
- 仓库内可复用：`stream.go`（SSE）、`offline_cache.go`（`accountScope`）、
  `reading_history.go`（阅读历史，兴趣信号的现成来源）、`toasts.go`、`api.go`（`tabSpecs`）

---

## 8. 变更记录

- 2026-11-14 建立本文；P0 落地（`pet.go`、`pet_ui.go`、`pet_test.go`，托盘开关，
  通知气泡，主窗口收进托盘时宠物留守）。真机透明与置顶待验（P0.5）。
- 2026-11-14 去掉宠物窗口的描边与阴影：Windows 11 的 DWM 把窗口当圆角有框的窗口
  渲染，透明窗口上就成了空心方框加灰雾。新增 `window_chrome_windows.go`（`!windows`
  为空实现），建窗后调一次 `removeWindowChrome`：关圆角 + 关边框色，两样缺一不可。
- 2026-11-14 本体从橙色小动物改成黄铜古典天平，动画从「呼吸 + 眨眼」换成
  「横梁摇摆 + 未读称重 + 消息冲击」。窗口仍是 120×128，气泡与交互不变。
  绘制全用 `Painter.FillPath` / `StrokePath`：**`Painter.Line` 只画水平或垂直
  线**，斜的吊绳和指针用它会被折成一条直角边（实测是「从天上垂下来的竖
  条」），见 `pet_ui.go` 里的 `stroke` 辅助函数。
- 2026-11-14 把桌面宠物抽成**默认关闭的实验功能**，单独在分支
  `pet` 上做，不影响 main 的既有逻辑（main 已彻底移除宠物相关代码）。新增 `experiment.go`
  （`experimentStore`，落盘 `experiment.json`，当前只有 `pet` 一项，默认
  false）；`petExperimentOn()` 是宠物被放出来 / 开口说话的总闸，比 `petStore.on()`
  （用户的具体开关）高一级。设置窗口新增「实验功能」一块，勾选「桌面宠物
  （实验功能）」才打开它；实验没开时托盘菜单里不出现「桌面宠物」，启动也不
  放宠物。开启后按用户在 `pet.json` 里的原选择恢复，关闭时只收起、不抹掉选择。
