# 发帖「工作内容」扩展设计（求职 / 招人啦）

- 状态：一期已实施，待手动验收
- 日期：2026-11-24
- 范围：发帖窗口侧栏「工作内容」组件、标题前缀、`contact` 字段、草稿、校验、匿名兼容
- 约束：不新增端点；字段名照开放 API 原文；标题规则对齐网页端

## 1. 目标

2Libra 网页端的发帖页有「工作内容」扩展，客户端目前没有，只能手写标题前缀与联系方式。
要补的是：

1. 发帖侧栏出现「工作内容」，弹窗填写求职 / 招人、全职 / 兼职、职位、薪资、远程、联系方式。
2. 标题自动维护 `【求职/全职/职位/薪资/远程】` 前缀，与投票 / 竞猜 / 兑换码等系统标签共存，
   且工作前缀始终在最前面。
3. 联系方式按文档的发帖字段 `contact` 提交，不再让用户塞进正文。
4. 草稿能保存 / 恢复这组字段；匿名发帖不剥离工作内容（网页端允许匿名使用）。

## 2. 非目标

- 不做编辑已发帖子的工作内容：编辑窗口仍只改标题、正文、可见性；组件字段去网页端改。
- 不做推广节点配额判断（等级 ≤4 禁止等），服务端会校验，客户端不重复实现。
- 不做 AI 关键词推荐（「找工作 / 招人」命中后提示添加扩展）。现有 `compose_suggest.go`
  是 @ 与表情短码，不混在一起。
- 不新增 `job` / `work` 之类的请求体字段；发帖字段表里没有这些字段。
- 不把联系方式追加到正文（见 4.3 与第 8 节）。

## 3. 现状与代码位置

| 现有实现 | 位置 | 说明 |
| --- | --- | --- |
| 发帖字段表 | 开放 API `api-write.md` 发帖一节 | 有 `contact`（string，选填，≤500）；没有工作内容相关字段 |
| 当前发帖字段 | `compose.go:260-263` | 注释明确 `title_prefix` / `tip` / `contact` / `status` 不再展示、不进请求体 |
| 标题标签系统 | `compose_title_tag.go:19-104` | 只认 5 个系统标签：兑换码、投票、竞猜、💰、🔒；非系统 `【…】` 当用户前缀原样保留 |
| 标题前缀提取 | `compose_title_tag.go:106-119` | 请求体 `title_prefix` 取标题开头连续的 `【】` / `[]` 块，≤100 字 |
| 点评前缀 | `compose_title_tag.go:121-147` | `《对象》点评：N 分｜`，与系统标签共存 |
| 组件表 | `compose_ui.go:734-756` | 7 个组件：投票、评分、竞猜、兑换码 / 红包、金币池、可见性、隐藏内容 |
| 组件弹窗 | `compose_ui.go:898-945` | 点「完成」后 `composeApplyModuleTitle` 同步标题 |
| 匿名联动 | `compose_ui.go:784-810` | 匿名时移除金币池、竞猜、隐藏内容；工作内容不在其中 |
| 请求体组装 | `compose.go:671-748` | `payload()` 逐块拼字段 |
| 草稿 DTO | `draft.go:68-132`、`draft.go:576-631` | `composeDraftModules` 每个组件一个指针；只在开关打开时写 |
| 草稿恢复 | `draft.go:637-680` | `applyComposeDraft` 全量覆盖表单 |
| 节点 | `data/2libra-dict.json:686-745` | `job-hunting`（求职）、`recruiting`（招人啦），父节点 `career` |
| 网页端规则 | `2libra-发帖规则.md:152-178` | 前缀拼法、剥离旧前缀、联系方式 Base64 追加正文 |
| Base64 工具 | `base64.go:52-54` | 已有 `encodeBase64Text`，二期若需要可直接用 |

## 4. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 组件位置 | 侧栏「附加组件」第一位，排在投票之前（工作内容常与节点绑定） |
| 2 | 字段 | 节点类型、工作类型、职位、薪资、远程、联系方式，共 6 项 |
| 3 | 前缀拼法 | `【` + 已填片段用 `/` 连接 + `】`；节点类型 `job-hunting` → 求职，其余非空 → 招人啦；工作类型 `full-time` → 全职，其余非空 → 兼职 |
| 4 | 前缀顺序 | 工作前缀 → 点评前缀 → 系统标签块 → 正文 |
| 5 | 联系方式 | 只发 `contact` 字段；一期不往正文追加 Base64（见第 8 节） |
| 6 | 节点联动 | 不自动改 `node_id`；当前节点是求职 / 招人啦时，弹窗打开时预选对应节点类型 |
| 7 | 匿名 | 允许；匿名开关不剥离工作前缀 |
| 8 | 校验 | 6 项全空点「完成」提示「未填写内容」且不启用；最终标题仍受 500 字上限约束 |
| 9 | 草稿 | 开关与 6 个字段一起进草稿；恢复时重建前缀 |
| 10 | 清理 | 移除组件时剥掉标题里的工作前缀，其余字段保留在内存，方便再次添加 |

## 5. 设计

### 5.1 数据结构

`composeState` 增加：

```go
jobOpen     bool
jobNode     string // "job-hunting" / "recruiting"；空表示不拼节点段
jobType     string // "full-time" / "part-time"；空表示不拼类型段
jobPosition string // ≤50
jobSalary   string // ≤50
jobRemote   bool
jobContact  string // ≤500，提交到 contact
```

`composeDraftModules` 增加：

```go
type composeDraftJob struct {
    Node     string `json:"node,omitempty"`
    Type     string `json:"type,omitempty"`
    Position string `json:"position,omitempty"`
    Salary   string `json:"salary,omitempty"`
    Remote   bool   `json:"remote,omitempty"`
    Contact  string `json:"contact,omitempty"`
}
```

`Job *composeDraftJob \`json:"job,omitempty"\`` 与其它组件并列。

### 5.2 前缀生成与剥离

新增函数，放在 `compose_title_tag.go`：

```go
var composeJobPrefixRE = regexp.MustCompile(`^【(?:求职|招人啦)(?:/[^】]*)?】`)

func composeJobPrefix(st *composeState) string
func composeStripJobPrefix(title string) string   // 循环剥，兼容叠加的旧前缀
func composeApplyJobTitle(st *composeState) string
```

拼法：

```text
parts := []string{}
switch st.jobNode {
case "job-hunting":
    parts = append(parts, "求职")
case "":
default:
    parts = append(parts, "招人啦")
}
switch st.jobType {
case "full-time":
    parts = append(parts, "全职")
case "":
default:
    parts = append(parts, "兼职")
}
if st.jobPosition != "" {
    parts = append(parts, st.jobPosition)
}
if st.jobSalary != "" {
    parts = append(parts, st.jobSalary)
}
if st.jobRemote {
    parts = append(parts, "远程")
}
prefix = "【" + strings.Join(parts, "/") + "】"
```

与网页端的一处刻意差异：网页端正则是 `^【(?:求职|招人啦)[^】]*】`，会把 `【求职者】`
这种用户自己写的前缀也删掉。这里要求 `求职` / `招人啦` 之后紧跟 `/` 或 `】`，
避免误伤（与 `compose_title_tag.go` 顶部对网页端 `es()` 的保守处理同一理由）。

标题重建顺序（关键）：

```go
func composeApplyJobTitle(st *composeState) string {
    review, rest := composeTakeReviewPrefixes(st.title) // 先拿走《》点评前缀
    rest = composeStripJobPrefix(rest)                  // 再剥旧的工作前缀
    if !st.jobOpen {
        return review + rest
    }
    return composeJobPrefix(st) + review + rest         // 工作前缀始终最前
}
```

调用时机：

- 组件弹窗点「完成」后；
- 移除组件后；
- 任何调用 `composeApplyModuleTitle` 之后（投票、竞猜、兑换码、金币池、隐藏内容增删）——
  因为 `composeSetTitleTag` 会把非系统的 `【工作…】` 当 custom 前缀挪到系统标签后面；
- 草稿恢复后、匿名开关切换后。

实现上把 `composeApplyModuleTitle` 末尾统一改成：

```go
st.title = composeApplyJobTitle(st)
```

这样所有标题变更路径都收敛到一个出口。

### 5.3 联系方式

- 一期只把 `jobContact` 写进请求体 `contact`，不追加正文。
- 依据：开放 API 发帖字段表明确有 `contact`（≤500），当前 `compose.go:260-263` 只是主动不用它。
- 发布后需要实测：用 `GET /v1/topics/:short_id` 看详情里联系方式是否可见；网页端详情是否展示。
  若展示，本设计完成；若不展示，走第 8 节的二期方案。
- 联系方式长度按字符数校验，不是字节数；超 500 提示「联系方式最多 500 字」。

### 5.4 弹窗界面

`composeModuleSpecs` 增加：

```go
{name: "工作内容", hint: "求职或招人，在标题前加工作信息。",
    flag: func(st *composeState) *bool { return &st.jobOpen },
    body: func(a *app, c *ui.Context, st *composeState) { a.composeJob(c, st) }},
```

`composeJob` 画在 `feedback.Modal` 里（与投票 / 金币池同一套）：

1. 说明一行：「适合发在求职、招人啦节点；标题会自动加【…】前缀。」
2. 节点类型：下拉，选项「不填 / 求职 / 招人啦」。
3. 工作类型：下拉，选项「不填 / 全职 / 兼职」。
4. 职位：输入框，占位「例：后端工程师」，≤50。
5. 薪资：输入框，占位「例：25k-35k」，≤50。
6. 远程：复选框「支持远程」。
7. 联系方式：输入框，占位「微信 / 邮箱 / 链接」，≤500。
8. 预览：一行等宽小字，实时显示 `【求职/全职/后端/25k/远程】`；未填时显示「填写后生成前缀」。
9. 校验：节点类型、工作类型、职位、薪资、远程、联系方式全空时，点「完成」toast
   「未填写内容」，不置 `jobOpen`。

弹窗打开时：若 `jobNode` 为空且当前节点 `slug` 是 `job-hunting` / `recruiting`，
预选对应项；用户仍可改。

### 5.5 请求体

`payload()` 增加：

```go
if st.jobOpen {
    if st.jobContact != "" {
        p["contact"] = st.jobContact
    }
}
```

- 不新增其它字段。
- `title_prefix` 由现有 `composeTitlePrefix(title)` 自动提取，工作前缀会被包含进去；
  验收里要断言 `title_prefix` 等于工作前缀。
- 标题最终长度校验沿用 `composeTitleMax`；前缀本身占长度，超限时提示用户缩短职位 / 薪资。

### 5.6 草稿

写：

```go
if st.jobOpen {
    has = true
    mods.Job = &composeDraftJob{
        Node: st.jobNode, Type: st.jobType, Position: st.jobPosition,
        Salary: st.jobSalary, Remote: st.jobRemote, Contact: st.jobContact,
    }
}
```

读：`applyComposeDraft` 里对应回填 6 个字段、`jobOpen = true`，然后调用
`st.title = composeApplyJobTitle(st)`，保证旧草稿标题里缺前缀时能补上、重复前缀能收成一个。

### 5.7 其他路径

- 匿名开关：不动 `jobOpen` 与工作前缀（网页端允许匿名使用工作内容）。
- 「全部移除」：`jobOpen = false`，`composeApplyModuleTitle` 会调用 `composeApplyJobTitle` 剥前缀。
- 发帖成功后的 `resetComposeForm`：6 个字段清空，`jobOpen = false`。
- 模式切换（帖子 / 动态）：工作内容只属于帖子模式；切到动态时组件区照现有逻辑隐藏。
- 编辑窗口：不展示工作内容；标题里已带的前缀由用户自行编辑，编辑 payload 不含 `contact`，
  不会清掉已发帖子的联系方式。

## 6. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `compose.go` | `composeState` 加 6 字段 + `jobOpen`；`payload()` 发 `contact`；`resetComposeForm` 清理 |
| `compose_title_tag.go` | `composeJobPrefixRE`、`composeJobPrefix`、`composeStripJobPrefix`、`composeApplyJobTitle` |
| `compose_ui.go` | 组件表加一项；`composeJob` 弹窗；`composeApplyModuleTitle` 末尾重挂工作前缀；匿名与「全部移除」路径确认 |
| `draft.go` | `composeDraftJob` 结构；写草稿与 `applyComposeDraft` 回填 |
| `compose_test.go` / `compose_title_tag_test.go` / `draft_test.go` | 单元与 UI 测试 |
| `2libra-发帖规则.md` | 不改；它是网页端规则的来源 |

## 7. 验收条件

### 7.1 单元测试

- [x] `TestComposeJobPrefix`：6 组组合的前缀与网页端一致：
      只职位、职位 + 薪资、全填、只远程、只节点类型、全空。
- [x] `TestComposeStripJobPrefix`：连续多个旧前缀全部剥掉；`【求职者】` 不被误删；
      `《X》点评：8 分｜【求职/全职】标题` 剥完保留点评前缀。
- [x] `TestComposeApplyJobTitleOrder`：标题为 `【求职/全职/后端】【投票】原标题`，
      工作前缀在系统标签之前。
- [x] `TestComposeJobPayload`：`payload()` 含 `contact`，不含任何 `job*` 字段；
      `title_prefix` 为工作前缀。
- [x] `TestComposeJobContactLimit`：501 字返回「联系方式最多 500 字」。
- [x] `TestComposeJobAnonymousKeepsPrefix`：勾匿名后 `jobOpen` 与前缀仍在。
- [x] `TestComposeJobDraftRoundTrip`：写草稿 → 新表单恢复 → 6 字段与前缀一致。

### 7.2 UI 测试（`ui.NewTester`）

- [x] 侧栏出现「工作内容」，位置在「投票」之前。
- [x] 点击后弹窗出现「节点类型」「工作类型」「职位」「薪资」「远程」「联系方式」与预览行。
- [x] 选求职 / 全职，职位「后端」、薪资「25k」、勾远程 → 预览
      `【求职/全职/后端/25k/远程】`；点「完成」后标题带该前缀。
- [x] 已有投票组件时再改工作内容，工作前缀仍在投票标签之前。
- [x] 点行尾「移除工作内容」→ 标题前缀消失，其余字段保留（再次打开弹窗仍在）。
- [x] 6 项全空点「完成」→ toast「未填写内容」，行状态仍是「设置」不是「已添加」。
- [x] 当前节点选「求职」时打开弹窗，节点类型预选「求职」。

### 7.3 请求级验收（httptest）

- [x] `POST /v1/topics` 请求体断言：
      - `title` 含前缀；
      - `title_prefix` 等于前缀；
      - `contact` 等于填写值；
      - 没有 `job`、`work_content` 等未在文档里的字段。

### 7.4 手动验收

- [ ] 发一条「求职」节点帖子，网页端与客户端详情都能看到标题前缀。
- [ ] 在网页端打开同一条帖子，确认联系方式是否展示：
      - 展示 → 一期完成；
      - 不展示 → 记录到本节，按第 8 节二期方案补。
- [ ] 匿名发一条带工作内容的帖子，前缀保留，服务端不报错。
- [ ] 发帖成功后表单清空，下一次发帖默认不带工作内容。

### 7.5 回归与门禁

- [ ] 现有投票 / 评分 / 竞猜 / 兑换码 / 金币池 / 可见性 / 隐藏内容标题测试不变。
- [ ] 现有草稿测试不变；旧草稿没有 `job` 字段时正常恢复。
- [ ] `CGO_ENABLED=0 go test ./...` 通过。
- [ ] `go tool mygo vet .` 无告警。
- [ ] `gofmt -l .` 无输出。

## 8. 待实测与二期方案

网页端把联系方式 `btoa()` 后追加到正文（`2libra-发帖规则.md:178`），而开放 API 又有
`contact` 字段。两者关系需要实测确认：

- 若 `contact` 字段能在帖子页展示：维持一期方案，不污染正文。
- 若只有正文里的 Base64 块才展示：二期在 `payload()` 里追加

  ```text
  \n\n联系方式：\n<base64(contact)>
  ```

  追加前先用正则剥掉正文里已有的 `联系方式：` 块，避免每点一次「完成」就追加一遍；
  Base64 直接复用 `base64.go:52` 的 `encodeBase64Text`（UTF-8 结果与网页端一致）。
  二期还要在详情页把这种块渲染成可复制的联系方式，不显示裸 Base64。

二期不进入本次实现范围；一期验收里保留「手动确认联系方式是否展示」一项。

## 9. 风险与回退

| 风险 | 处理 |
| --- | --- |
| 工作前缀被系统标签挪到后面 | 所有标题变更统一走 `composeApplyJobTitle`；单元测试覆盖「加投票后仍最前」 |
| 前缀误删用户自己写的 `【求职者】` | 正则要求 `求职` / `招人啦` 后紧跟 `/` 或 `】`；测试覆盖 |
| 重复点击「完成」叠加前缀 | `composeStripJobPrefix` 循环剥离后再拼 |
| `contact` 服务端不展示 | 一期先发字段；实测不展示再走二期正文追加，不影响已发帖子 |
| 标题超 500 字 | 在 `payload()` 校验最终标题；提示缩短职位 / 薪资，不静默截断 |
| 旧草稿没有 `job` 字段 | `jobOpen=false`，标题原样；恢复逻辑只在 `m.Job != nil` 时重建前缀 |