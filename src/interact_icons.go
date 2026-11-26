package main

import "github.com/egoist/mygo/ui"

// 互动区右侧的两个图标入口：表态（笑脸）与打赏（金币）。单独放一个文件，
// 不和 icons.go 里按功能分区的那组图标混在一起。
var (
	// 表态：笑脸，点开贴着它的气泡选表情。
	reactionIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M8.5 14a4.5 4.5 0 0 0 7 0"/><circle cx="9" cy="9.5" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="9.5" r="1" fill="currentColor" stroke="none"/></svg>`))
	// 打赏：金币，点开贴着它的气泡填金额。
	rewardIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7.5v9"/><path d="M9 10.5h6M9 13.5h6"/></svg>`))
	// 回复：向左的回旋箭头，放在评论底行最右端。
	replyIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 5L4 10l5 5"/><path d="M4 10h9a6 6 0 0 1 6 6v3"/></svg>`))
)
