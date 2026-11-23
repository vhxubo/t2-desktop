# 2Libra 网页端发帖规则（爬取整理）

> 爬取目标：`https://2libra.com` 网页端「发布帖子」页 `/post/create`
> 爬取时间：2026-11-20
> 站点版本：5.6.2（前端构建版本号 `o="5.6.2"`）
> 数据来源：Next.js 服务端渲染 HTML + 前端 JS chunk（页面及规则逻辑均编译在 JS 中，公开可访问，无需登录）
> 核心 chunk：`/_next/static/chunks/app/post/create/page-35ed675cef13fcd4.js`（发帖页）
> 常量 chunk：`/_next/static/chunks/3824-6809401b6b59987c.js`（module 46353 / 53682）

---

## 0. 结论速览：标题里的【】是怎么按类型变的

2Libra 没有一份写在「帮助文档」里的成文发帖规则。真正的「发帖规则」是**编译在发帖页 JS 里的标题前缀逻辑**：当用户在发帖页添加某种「扩展功能」时，前端会自动在标题最前面插入一个 `【关键词】` 标签；不同扩展对应不同关键词，多个标签用 `·` 连接在**同一个** `【】` 里。关键词集合固定为 5 个，其它内容不会进入这个标签。

| 扩展功能 | 标题里插入的关键词 | 去掉的写法 |
| --- | --- | --- |
| 兑换码 | `【兑换码】` | `es(title, "兑换码", false)` |
| 投票 | `【投票】` | 移除投票 |
| 竞猜 | `【竞猜】` | 移除竞猜 |
| 金币池 | `【💰】` | 移除金币池 |
| 隐藏内容 | `【🔒】` | 移除隐藏内容 |

另有两类**独立的【】前缀**（走不同代码路径，不进上面这套标签系统）：

- **求职 / 招人啦（工作内容）**：`【求职/全职/职位/薪资/远程】标题`
- **点评**：`《对象名》点评：N 分｜正文`（用的是 `《》` + `点评：`，不是标准 `【】` 标签）

以及**AI 关键词推荐**：前端从标题+正文里匹配关键词，自动建议用户加对应扩展（工作内容/兑换码/投票/竞猜/点评）。

---

## 1. 核心：【】标签系统（类型关键词自动增删）

### 1.1 标签字典与合法关键词

源码（module 63987 附近）：

```js
Q = {
  redeem:     "【兑换码】",
  vote:       "【投票】",
  bet:        "【竞猜】",
  rewardPool: "【💰】",
  restrict:   "【🔒】"
}
ee = ["兑换码", "投票", "竞猜", "💰", "🔒"]   // 合法关键词全集（有序）
```

要点：

- 合法关键词**只有这 5 个**，顺序固定为 `兑换码 · 投票 · 竞猜 · 💰 · 🔒`。
- 标签写在标题**最开头**，形如 `【兑换码·投票·💰】我的标题`。
- 多个标签之间用间隔号 `·`（U+00B7，源码里是 `\xb7`）连接，**共用一个 `【】`**。
- 不在 `ee` 里的词（例如用户自己手打的 `【吐槽】`）不会被纳入这套系统——既不会被识别，也不会被自动移除。

### 1.2 标签增删算法（完整还原）

源码里的两个函数 `es`（改标签）和 `et`（剥离前缀）：

```js
// et：从标题开头反复剥离某个前缀（可能有多个重复的标签）
function et(title, prefix) {
  let t = title;
  while (t.startsWith(prefix)) t = t.slice(prefix.length);
  return t;
}

// es：给标题加上 / 移除一个类型标签
// title = 原标题；tag = 要操作的关键词；add = true 加，false 删
function es(title, tag, add) {
  let rest = title;
  const m = title.match(/^【([^】]+)】/);       // 只匹配开头的第一个【】
  const tags = new Set();

  if (m) {
    // 把【】里的内容按 · 拆开，只保留合法关键词
    for (const part of m[1].split("·").map(s => s.trim()).filter(Boolean)) {
      if (ee.includes(part)) tags.add(part);
    }
    rest = title.slice(m[0].length);            // 去掉整个开头【】块
  }

  // 把 5 种标签前缀从正文开头彻底剥离（防止残留重复）
  rest = et(rest, Q.redeem);
  rest = et(rest, Q.vote);
  rest = et(rest, Q.bet);
  rest = et(rest, Q.rewardPool);
  rest = et(rest, Q.restrict);

  add ? tags.add(tag) : tags.delete(tag);       // 加 / 删目标标签

  const kept = ee.filter(k => tags.has(k));     // 按固定顺序输出
  return kept.length === 0 ? rest : `【${kept.join("·")}】${rest}`;
}
```

规则要点：

1. 只识别**标题最开头**的一个 `【】`；标题中间的 `【】` 不参与。
2. 拆标签时**只认白名单** `ee`，非法的词被丢弃。
3. 剥离时用 `startsWith` 循环剥，能清掉连续重复的同类标签。
4. 输出时标签按 `ee` 的固定顺序重排，而不是按用户添加顺序——即不管先加投票还是先加兑换码，结果恒为 `【兑换码·投票】…`。
5. 删到没有标签时，标题不再带 `【】`。

### 1.3 提交给后端的 `title_prefix`

发帖提交时，前端还会单独提取标题前缀字段：

```js
title_prefix: (title => {
  const m = title.match(/^((?:\[[^\[\]]+\]|【[^【】]+】)+)/);
  return m ? m[1] : "";
})(title)
```

即：把标题开头**连续**的 `[方括号]` 或 `【中文括号】` 块整体提取出来作为 `title_prefix`。支持方括号 `[]` 和中文括号 `【】` 两种，且允许多个连续块（如 `【兑换码】[转载]标题`）。

### 1.4 各触发点（调用 `es` 的全部位置）

| 操作 | 调用 | 说明 |
| --- | --- | --- |
| 填好兑换码并确认 | `es(title, "兑换码", true)` | 加 `【兑换码】` |
| 移除兑换码 | `es(title, "兑换码", false)` | 删 `【兑换码】` |
| 填好投票并确认 | `es(title, "投票", true)` | 加 `【投票】` |
| 移除投票 | `es(title, "投票", false)` | 删 `【投票】` |
| 填好竞猜并确认 | `es(title, "竞猜", true)` | 加 `【竞猜】` |
| 移除竞猜 | `es(title, "竞猜", false)` | 删 `【竞猜】` |
| 填好金币池并确认 | `es(title, "💰", true)` | 加 `【💰】` |
| 移除金币池 | `es(title, "💰", false)` | 删 `【💰】` |
| 设置隐藏内容并确认 | `es(title, "🔒", true)` | 加 `【🔒】` |
| 移除隐藏内容 | `es(title, "🔒", false)` | 删 `【🔒】` |

提示文案（原文）：

- 投票：「PK 类型只能有两个选项，其他类型可以有多个选项。开启投票后在帖子标题增加【投票】标签。」
- 隐藏内容：「确定后会在帖子标题前增加【🔒】标签（若标题已有该标签会先去掉再置于最前）。」

### 1.5 匿名发帖的联动规则

开启匿名时，前端**自动移除**三个不兼容的扩展及其标签：

```js
title = es(title, "💰", false);
title = es(title, "竞猜", false);
title = es(title, "🔒", false);
toast.info("匿名发帖不能使用金币池、竞猜和隐藏内容，已自动移除相关设置")
```

---

## 2. 另一类【】前缀：求职 / 招人啦（工作内容）

这条走**独立代码路径**，不进第 1 节的标签系统。

源码：

```js
const el = /^【(?:求职|招人啦)[^】]*】/;   // 用于先清除旧前缀

// 表单确认时：
let s = "";
if (e.node)     s += `${ e.node === "job-hunting" ? "求职" : "招人啦" }/`;
if (e.jobType)  s += `${ e.jobType === "full-time" ? "全职" : "兼职" }/`;
if (e.position) s += `${ e.position }/`;
if (e.salary)   s += `${ e.salary }/`;
if (e.isRemote) s += `远程/`;
if (!s) return toast.warning("未填写内容");
s = s.slice(0, -1);                        // 去掉结尾的 /
title = `【${ s }】` + stripOldJobPrefix(title);   // 去掉旧的前缀后再加新的
```

规则：

- 前缀格式：`【求职/全职/职位/薪资/远程】`（字段用 `/` 分隔，只拼用户填了的字段）。
- 节点取值：`job-hunting` → `求职`，其它 → `招人啦`；工作类型：`full-time` → `全职`，其它 → `兼职`。
- 每次确认先用 `el` 循环剥掉旧的 `【求职…】` / `【招人啦…】` 前缀，再加新的，保证不重复。
- 联系方式会被 `btoa()` Base64 编码后追加到正文（`联系方式：\n<base64>`）——这是防爬/防滥用处理，不是标题规则。

---

## 3. 点评前缀（注意：不是【】标签）

点评扩展生成的是 `《》` + `点评：` 格式：

```js
const re = /^《[^》]+》点评：\d+ 分｜/;   // 先循环剥掉旧点评前缀
title = `《${targetName}》点评：${score} 分｜${restOfTitle}`;
```

示例：`《沙丘2》点评：8 分｜值得一看`

---

## 4. AI 关键词推荐（标题/正文 → 建议加哪种扩展）

前端从 `标题 + 正文` 里匹配关键词（不区分大小写），命中即建议对应扩展：

```js
const text = `${title} ${content}`.toLowerCase();
const hits = new Set();
for (const [category, words] of Object.entries(KEYWORD_MAP)) {
  if (words.some(w => text.includes(w))) hits.add(category);
}
// 提示："推荐" + [...hits].join("、") + "扩展"
```

关键词字典（原文）：

| 类别 | 关键词 |
| --- | --- |
| 💼 工作内容 | 找工作、招人、求职、招聘、全职、兼职、实习、社招、校招、内推、猎头、跳槽、面试、offer、简历、岗位、薪资、待遇、月薪、时薪、远程办公、居家办公、入职 |
| 🎁 兑换码 | 兑换码、邀请码、优惠码、激活码、注册码、折扣码、优惠券、代金券、礼品卡 |
| 🗳️ 投票 | 投票、你们觉得、对比、哪个好、选哪个、大家觉得、谁更、PK、二选一、三选一、投票决定、怎么选 |
| 🎰 竞猜 | 竞猜、猜猜、猜一猜、下注、赔率、猜结果、我猜、你们猜、谁能赢、谁会赢 |
| 💬 点评 | 电影、推荐、分享、打分、评分、评价、评测、测评、观后感、读后感、值得、好不好、实测、开箱、安利、吐槽、体验 |

只用于**推荐提示**，不会自动改标题，也不影响后端判定。

---

## 5. 扩展功能菜单（全部 9 项，原文）

| 扩展 | 说明 | 适用节点（原文） | 匿名可用 |
| --- | --- | --- | --- |
| 💼 工作内容 | — | 适合"求职"、"招人啦"等节点 | 是 |
| 🎁 兑换码 | 添加兑换码/红包 | 适合"作品分享"、"分享发现"等节点 | 是 |
| 🗳️ 投票 | 发起投票 | 适合一些需要投票的场景 | 是 |
| 🎰 竞猜 | 发起事件竞猜，用户投入金币参与 | — | 否 |
| 💰 金币池 | 用户评论有概率获得金币奖励 | — | 否 |
| 💬 点评 | 对任何对象进行点评 | — | 是 |
| 👤 匿名发布 | 以匿名身份发布帖子，保护隐私 | — | — |
| 👁️ 可见性 | 设置帖子的可见范围，可按登录状态、等级或属性限制正文可见 | — | 是 |
| 🔒 隐藏内容 | 设置仅满足条件后可见的隐藏内容 | — | 否 |

注：`hidden content(🔒)` 带有 `isNew` 标记，.hiddenUntil = `2026-03-29`（相对当时为「新功能」角标）。

---

## 6. 发帖校验规则与限制

| 项 | 规则 | 源码常量 / 提示 |
| --- | --- | --- |
| 标题 | 不能为空 | `"请输入标题"` |
| 标题长度 | 最多 500 字符 | `maxLength=500`（匿名发帖弹窗） |
| 正文长度 | 最多 20000 字符 | `"正文长度不能超过 20000 个字符"` / `eW = len > 2e4` |
| 正文 | 可留空（留空则只发标题） | `"留空则只发标题。"` |
| 正文格式 | 支持 Markdown 超集 | 提供实时预览 / 全屏编辑，链接指向 markdownguide.org |
| 节点 | 必须选择（手动或 AI 自动） | `"请选择节点"`；手动下无效节点提示「节点不存在，请重新选择或选择由 AI 自动选择。」 |
| 节点-特殊 | `社区-讨论` 手动映射到固定 uuid `30000000-0000-0000-0000-000000000002` | — |
| AI 自动节点 | 未手动选时用 `AI_PRE_NODE_ID = d0629de1-0979-4846-81c6-ff69da66f7ab` | 提示「节点将通过帖子标题和正文由 AI 自动选择。」 |
| 投票选项 | 最多 20 个，每项最长 255 字；PK 型必须恰好 2 项 | `MAX_VOTE_OPTIONS_LENGTH=20`, `MAX_VOTE_OPTION_LENGTH=255` |
| 竞猜选项 | 2–6 项，不得重复 | `"竞猜必须有2-6个选项"` / `"竞猜选项不能相同"` |
| 每日发帖上限 | 达到上限后「预览」按钮禁用 | `"达到今日发帖上限"` |

---

## 7. 推广（promotion）规则（原文）

弹窗标题：**推广须知**

> 除了对内容符合社区的发布规则外，推广内容还受如下限制：
>
> 1. 在「推广」节点发帖受近 **7** 天配额限制，超出配额的内容将自动移入黑洞节点；
>
>    | 条件 | 每周（近 7 天）上限 |
>    | --- | --- |
>    | 等级 ≤ 4 | 0（禁止） |
>    | 等级 = 5 | 1 篇 |
>    | 等级 ≥ 6 | 3 篇 |
>    | 2Libra Pro | 合理下不限 |
>
> 2. 本站优先通过 AI 检定内容是否为推广（无论是否为手动选择的节点），AI 识别可能不够准确请理解。存在异议时可随时在评论区 @守护者 修正。
> 3. 带兑换码、邀请码的推广内容，需要选择**兑换码**扩展，否则该推广内容会被守护者移动至黑洞。
> 4. 请不要让自己的账号沦为推广账号，否则管理员会对账号进行降权甚至关闭发布权限，合理的推广才能够良性循环。

对应常量：

```
PROMOTION_NODE_SLUG = "promotion"
PROMOTION_LIMIT_WINDOW_DAYS = 7
PROMOTION_LIMIT_FORBIDDEN_MAX_LEVEL = 4
PROMOTION_LIMIT_MID_LEVEL = 5
PROMOTION_LIMIT_LOW_LEVEL = 1       // 等级=5：每周 1 篇
PROMOTION_LIMIT_HIGH_LEVEL = 3      // 等级≥6：每周 3 篇
PROMOTION_NOTICE_DURATION_MINUTES = 10
BLACK_HOLE_NODE_ID = "30000000-0000-0000-0000-000000000001"
```

新手欢迎弹窗另有：「⚠️ 推广节点发布限制：等级 1~4 暂不可在「推广」节点发帖，等级 5 每周限 1 篇，升级或赞助 Pro 会员可扩展限制。」

---

## 8. 匿名发布规则（原文）

弹窗标题：**匿名发布** → 发布注意：

- 匿名贴发布需额外消耗 **400** 金币（`COIN_COST.anonymousPost = -400`，UI 用 `Math.abs` 显示 400）；
- 评论不会消耗金币，但也因此无法获取其他用户的评论收益，另外收到的打赏会转交于你；
- 不能在自己的匿名贴中对其他用户进行打赏与表态；
- 匿名贴中的所有操作都不会显示你的用户名和头像，并通过代理用户 **@Bourne** 身份发布；
- **1 天**内只能发布一篇匿名贴（`ANONYMOUS_POST_LIMIT_DAYS = 1`）；
- ⚠️ 注意：请不要使用匿名身份发布低质量、灌水以及其他非讨论工作与生活相关事情的内容，否则将会限制后续的匿名发布功能。

对应常量：

```
ANONYMOUS_USER_NAME = "Bourne"
ANONYMOUS_USER_ID   = "40000000-0000-0000-0000-000000000004"
ANONYMOUS_POST_LIMIT_DAYS = 1
```

匿名发帖页（`3401-*.js`）另有一条 AI 判定说明：

> 提交后会先做一次 AI 内容判定，通常需要 10~30 秒；你可以随时关闭窗口，判定会在后台继续。未通过会返回原因，并可能进入冷却。
> 匿名贴的正文会被代理用户署名「代理发布用户」；你仍可与普通用户一样对该贴回复、表态。

---

## 9. 金币池规则（原文）

> 金币规则：初始金币 + 用户每次回复增加的金币（+25，目前由系统发放）。用户中奖后，会从金币池中扣除相应金币后继续计算，直到金币消耗完或结束。

对应常量：

```
REWARD_POOL_INITIAL_PROBABILITY = 0.08
REWARD_POOL_RATE = 0.01
REWARD_POOL_INITIAL_COINS = 100
REWARD_POOL_MAX_AMOUNT = 500
REWARD_POOL_INCREMENT_COINS = 25
REPLY_COMMENT_NOT_WIN_COIN = 6
REWARD_POOL_AMOUNT_LINE = 100
REWARD_POOL_AMOUNT_RATE = 0.3
REWARD_POOL_SUPPLEMENT_WINDOW_WEEKS = 2
REWARD_POOL_SUPPLEMENT_MAX_COUNT = 5
```

---

## 10. 社区通用规则（首页欢迎弹窗，原文）

> 这是一个还在成长中的社区，你可以在此交流工作与生活上的事情。
> 希望每一位新用户遵守社区的规则，共同维护一个良好的社区环境。
> 核心的规则是：
>
> - **友好第一：** 请尊重他人，避免人身攻击、恶意挑衅或阴阳怪气。
> - **理性讨论：** 表达不同意见时，请用逻辑和事实，而非情绪和标签。
> - **平等交流：** 不论新手还是老手，都应同等地进行交流。

---

## 11. 相关发帖成本（COIN_COST，节选）

```
anonymousPost : -400        匿名发帖
checkin       : +50         签到
register      : +2000       注册
inviteRegister: +3000       邀请注册
post(len)     : len>5000?-200 : len>2000?-120 : len>1000?-60 : len>100?-30 : -10
comment(len)  : 同上档位
postFormat(len): len>500?-280 : len>100?-150 : -80
editPost / editComment / editPostscript / addPostscript(len):
               len>5000?-150 : len>2000?-100 : len>1000?-50 : len>100?-25 : -10
postReaction.send : -20      commentReaction.send : -20
specialPostReaction.send : -45  specialCommentReaction.send : -35
suggestNode(len): len>400?-30 : len>300?-20 : len>200?-10 : -5
pinComment : -200
```

违规相关：

```
VIOLATION_CHECK_USER_LEVEL = 3       // 3 级可用违规检定
VIOLATION_CHECK_RECENT_LIMIT = 3
VIOLATION_CHECK_COIN_REWARD = 300     // 检定正确奖励
VIOLATION_EDIT_TIMEOUT_MINUTES = 10
VIOLATION_TIMEOUT_COIN_PENALTY = 200
```

---

## 12. 爬虫方法备注（可复现）

1. `curl https://2libra.com` 与 `/post/create` 拿 SSR HTML（公开，无需登录）。
2. 从 HTML 提取 `webpack` runtime `webpack-b9cd6bd06986c071.js`，其中的 `r.u()` 映射给出全部 chunk 文件名；发帖页专属 chunk 是 `app/post/create/page-35ed675cef13fcd4.js`。
3. 所有 chunk 用 `curl`（带浏览器 UA + Referer）下载后，用 Python 正则提取 JS 字符串字面量，搜索 `【`、`规则`、`标题` 等关键字定位规则代码。
4. 常量在 `3824-*.js` 的 module 46353（站点/业务常量）、53682（金币常量）、98510（品牌/主题）。
5. 规则分散在多个 module：63987（匿名/金币池/提交）、58378（推广须知/预览）、及 create page 内的 37452 等。

> 说明：文档中所有中文文案均为网页前端代码里的原文；表格中的数值为前端打包时写死的常量。后端是否二次校验、以及服务端 Actual 规则（如 AI 判定细则）不在网页前端可见范围内。

---

## 13. D20 属性鉴定字典（隐藏内容 / 可见性共用）

> 2026-11-22 复核：站点前端已换到 `/_next/static/chunks/3824-c6bd6e08e690f1d0.js`（第 12 节里的 `3824-6809401b6b59987c.js` 是旧 hash），字典内容未变。同一份映射在发帖页 chunk、可见性弹窗 chunk（`9532-*.js`）与详情页 chunk（`layout-*.js`）里反复引用，是提交字段与界面文案的唯一来源。

常量定义原文（`3824-*.js`）：

```js
function(e){
  e.KARMA="karma"; e.EXPERIENCE="experience"; e.CHARM="charm";
  e.LUCK="luck"; e.DILIGENCE="diligence"; e.CRAFT="craft"
}(_||(t.PostRestrictedD20Attr=_={})),

t.POST_RESTRICTED_D20_ATTR_LABEL_MAP={
  [_.KARMA]:"财富", [_.EXPERIENCE]:"阅历", [_.CHARM]:"魅力",
  [_.LUCK]:"幸运", [_.DILIGENCE]:"勤勉", [_.CRAFT]:"技艺"
}
```

| 机器取值（枚举 `PostRestrictedD20Attr`） | 中文标签 | 提交字段 |
| --- | --- | --- |
| `karma` | 财富 | `restricted_d20_attr` / `access_guard_attr` |
| `experience` | 阅历 | 同上 |
| `charm` | 魅力 | 同上 |
| `luck` | 幸运 | 同上 |
| `diligence` | 勤勉 | 同上 |
| `craft` | 技艺 | 同上 |

相邻校验常量（同 chunk）：

| 常量 | 值 | 用途 |
| --- | --- | --- |
| `POST_RESTRICTED_DURATION_MIN_MONTHS` / `MAX_MONTHS` | 1 / 12 | 隐藏内容有效期 |
| `POST_RESTRICTED_CHECK_COST` | 100 | 每次属性检定花费金币 |
| `POST_RESTRICTED_CHECK_AUTHOR_SHARE_RATIO` | 0.5 | 检定费用归作者的比例 |
| 隐藏内容目标值 | 1～25 的整数 | 发帖页 `Number.isInteger(e) && e>=1 && e<=25` |
| 可见性属性下限 | 0～10 | `9532-*.js` 的属性下限输入框 |

应用内所有 D20 属性位置（发帖的隐藏内容与可见性、编辑帖子的可见性、受限内容门槛说明、草稿恢复）统一使用这一份字典，界面显示中文标签、请求只发机器取值。

注意站点另有一套 `ATTR_NAME_MAP`：`craft:"工艺"`、`experience:"经历"`、`karma:"财富"`、`luck:"幸运"`、`diligence:"勤勉"`、`charm:"魅力"`。它出现在等级/签到检定文案里（`3300-*.js`），**不是**发帖 D20 字段的取值表；发帖的隐藏内容与可见性都只用上面的 `POST_RESTRICTED_D20_ATTR_LABEL_MAP`，不要把「工艺 / 经历」混进发帖字典。

