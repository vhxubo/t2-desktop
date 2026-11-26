package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 标题【】前缀规则，照抄 2libra 网页端发帖页的行为（见 2libra-发帖规则.md）：
// 开启某个扩展时前端在标题最前面插入一个【关键词】，关闭时去掉；多个关键词
// 用间隔号「·」连在同一个【】里，并按固定顺序重排。全集是 5 个，含隐藏内容
// 的 🔒；桌面端添加「隐藏内容」组件时会自动加上，移除时自动去掉。
//
// 与网页端一处刻意的不同：网页端 es() 会把开头第一个【…】整块抹掉再重建，
// 连【吐槽】这类用户自己写的非系统前缀也一起删掉。这里只收走块里「系统
// 关键词」，其余原样保留，避免一次点击就吞掉作者自己写的前缀。

const composeTitleTagSep = "·"

// composeTitleTagOrder 是标签在标题里出现的固定顺序，与网页端一致。
var composeTitleTagOrder = []string{"兑换码", "投票", "竞猜", "💰", "🔒"}

// composeTitleTagIndex 返回 tag 在固定顺序里的下标，不是系统标签返回 -1。
func composeTitleTagIndex(tag string) int {
	for i, t := range composeTitleTagOrder {
		if t == tag {
			return i
		}
	}
	return -1
}

// composeSplitTitleTags 把标题开头的连续【…】块拆成三部分：
// 系统标签集合、用户自己写的前缀块（原顺序保留）、剩下的正文。
// 只处理「一个块里的全部片段都是系统标签」的情况；块里只要有一个非系统
// 片段，整块就当作用户内容留下（例如【吐槽】）。
func composeSplitTitleTags(title string) (map[string]bool, []string, string) {
	tags := make(map[string]bool)
	var custom []string
	body := title
	for strings.HasPrefix(body, "【") {
		end := strings.Index(body, "】")
		if end < 0 {
			break
		}
		content := body[len("【"):end]
		body = body[end+len("】"):]
		var kept []string
		for _, part := range strings.Split(content, composeTitleTagSep) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if composeTitleTagIndex(part) >= 0 {
				tags[part] = true
			} else {
				kept = append(kept, part)
			}
		}
		if len(kept) > 0 {
			custom = append(custom, "【"+strings.Join(kept, composeTitleTagSep)+"】")
		}
	}
	return tags, custom, body
}

// composeTitleWithTags 按固定顺序重建系统标签块，后接用户前缀与正文。
func composeTitleWithTags(tags map[string]bool, custom []string, body string) string {
	var b strings.Builder
	known := make([]string, 0, len(composeTitleTagOrder))
	for _, t := range composeTitleTagOrder {
		if tags[t] {
			known = append(known, t)
		}
	}
	if len(known) > 0 {
		b.WriteString("【")
		b.WriteString(strings.Join(known, composeTitleTagSep))
		b.WriteString("】")
	}
	for _, c := range custom {
		b.WriteString(c)
	}
	b.WriteString(body)
	return b.String()
}

// composeSetTitleTag 给标题加上（on=true）或去掉（on=false）一个系统标签。
// 点评前缀（《X》点评：N 分｜）也算前缀，标签块紧跟在它后面时同样能改；
// 非系统片段原样保留，未知标签原样返回。
func composeSetTitleTag(title, tag string, on bool) string {
	if composeTitleTagIndex(tag) < 0 {
		return title
	}
	review, rest := composeTakeReviewPrefixes(title)
	tags, custom, body := composeSplitTitleTags(rest)
	if on {
		tags[tag] = true
	} else {
		delete(tags, tag)
	}
	return review + composeTitleWithTags(tags, custom, body)
}

// composeTitlePrefixRE 对应网页端提取 title_prefix 的正则：标题开头连续的一个或多个
// [方括号] / 【中文括号】块，两种括号都算，允许多个连在一起。
var composeTitlePrefixRE = regexp.MustCompile(`^((?:\[[^\[\]]+\]|【[^【】]+】)+)`)

// composeTitlePrefix 提取发帖请求体里的 title_prefix。文档约定该字段 ≤100 字，
// 超长的自定义前缀截到 100 字，避免整篇帖子被 400 拒掉。
func composeTitlePrefix(title string) string {
	prefix := composeTitlePrefixRE.FindString(title)
	if utf8.RuneCountInString(prefix) > 100 {
		runes := []rune(prefix)
		return string(runes[:100])
	}
	return prefix
}

// composeReviewPrefixRE 对应网页端「点评」扩展写的《对象》点评：N 分｜ 前缀。
var composeReviewPrefixRE = regexp.MustCompile(`^《[^》]+》点评：\d+ 分｜`)

// composeTakeReviewPrefixes 把标题开头叠加的点评前缀与其余部分分开。
func composeTakeReviewPrefixes(title string) (string, string) {
	var prefix strings.Builder
	for {
		loc := composeReviewPrefixRE.FindStringIndex(title)
		if loc == nil || loc[0] != 0 {
			return prefix.String(), title
		}
		prefix.WriteString(title[:loc[1]])
		title = title[loc[1]:]
	}
}

// composeStripReviewPrefix 去掉标题上所有叠加的点评前缀（网页端是循环剥离）。
func composeStripReviewPrefix(title string) string {
	_, rest := composeTakeReviewPrefixes(title)
	return rest
}

// composeWithReviewPrefix 把标题改成《对象》点评：N 分｜原标题。
func composeWithReviewPrefix(title, target string, score int) string {
	title = composeStripReviewPrefix(title)
	return "《" + target + "》点评：" + strconv.Itoa(score) + " 分｜" + title
}

/* ------------------------------------------------------------------
   工作内容前缀（求职 / 招人啦）
---------------------------------------------------------------- */

// 节点类型与工作类型的机器取值：节点 slug 照 src/data/2libra-dict.json，工作类型
// 照网页端发帖页。下拉框存这些取值，前缀生成时再换成中文片段。
const (
	jobNodeHunting  = "job-hunting"
	jobNodeRecruit  = "recruiting"
	jobTypeFullTime = "full-time"
	jobTypePartTime = "part-time"
)

// composeJobPrefixRE 只认由「求职 / 招人啦」开头的工作前缀，且后面必须紧跟 /
// 或 】。与网页端 `^【(?:求职|招人啦)[^】]*】` 相比刻意收窄：作者自己写的
// 【求职者】不能被一次点击吞掉（与上面 es() 的保守处理同一理由）。
var composeJobPrefixRE = regexp.MustCompile(`^【(?:求职|招人啦)(?:/[^】]*)?】`)

// composeJobPrefix 把工作表单元拼成标题前缀：「【」+ 已填片段用 / 连接 +「】」。
// 片段顺序固定：节点类型、工作类型、职位、薪资、远程。一项都没填时返回空串，
// 弹窗预览据此显示「填写后生成前缀」。
func composeJobPrefix(st *composeState) string {
	if st == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	switch st.jobNode {
	case jobNodeHunting:
		parts = append(parts, "求职")
	case "":
	default:
		parts = append(parts, "招人啦")
	}
	switch st.jobType {
	case jobTypeFullTime:
		parts = append(parts, "全职")
	case "":
	default:
		parts = append(parts, "兼职")
	}
	if p := strings.TrimSpace(st.jobPosition); p != "" {
		parts = append(parts, p)
	}
	if p := strings.TrimSpace(st.jobSalary); p != "" {
		parts = append(parts, p)
	}
	if st.jobRemote {
		parts = append(parts, "远程")
	}
	if len(parts) == 0 {
		return ""
	}
	return "【" + strings.Join(parts, "/") + "】"
}

// leadingBracketBlock 拆出标题开头的一个【】或 [] 块，返回块与剩余部分。
func leadingBracketBlock(title string) (string, string) {
	if strings.HasPrefix(title, "【") {
		if end := strings.Index(title, "】"); end >= 0 {
			end += len("】")
			return title[:end], title[end:]
		}
	}
	if strings.HasPrefix(title, "[") {
		if end := strings.Index(title, "]"); end >= 0 {
			end += len("]")
			return title[:end], title[end:]
		}
	}
	return "", title
}

// stripLeadingJobBlocks 跳过开头连续的【】/[] 块，删掉其中匹配 isJob 的块。
// 之所以要跳过其它块：加投票等系统标签时 composeSetTitleTag 会把工作前缀当成
// 用户自定义块挪到系统标签后面，只查开头就剥不掉，再拼一次就会叠加两个前缀。
func stripLeadingJobBlocks(title string, isJob func(string) bool) string {
	var kept strings.Builder
	for {
		block, rest := leadingBracketBlock(title)
		if block == "" {
			break
		}
		title = rest
		if isJob(block) {
			continue
		}
		kept.WriteString(block)
	}
	return kept.String() + title
}

// composeStripJobPrefix 循环剥掉标题上所有工作前缀，点评前缀原样保留。
func composeStripJobPrefix(title string) string {
	review, rest := composeTakeReviewPrefixes(title)
	rest = stripLeadingJobBlocks(rest, func(block string) bool {
		return composeJobPrefixRE.MatchString(block)
	})
	return review + rest
}

// composeApplyJobTitle 是所有标题变更的统一出口：先拿走点评前缀，再剥掉旧的
// 工作前缀，开着工作内容时把新前缀放回最前面。
// 顺序：工作前缀 → 点评前缀 → 系统标签块 → 正文。
// 除了通用正则，还按当前表单精确剥一次：只填了职位 / 薪资 / 远程时前缀不以
// 「求职 / 招人啦」开头，正则认不出来，但当前生成的那块必须能撤掉。
func composeApplyJobTitle(st *composeState) string {
	if st == nil {
		return ""
	}
	review, rest := composeTakeReviewPrefixes(st.title)
	prefix := composeJobPrefix(st)
	rest = stripLeadingJobBlocks(rest, func(block string) bool {
		return composeJobPrefixRE.MatchString(block) || (prefix != "" && block == prefix)
	})
	if !st.jobOpen || prefix == "" {
		return review + rest
	}
	return prefix + review + rest
}
