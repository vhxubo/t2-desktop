package main

// replySurface 标记评论输入相关的浮层画在哪扇窗口里。详情页与独立写评论
// 窗口共用同一份 detailState，但同一时刻只该由点开它的那扇窗口渲染浮层。
type replySurface string

const (
	replySurfaceDetail replySurface = "detail"
	replySurfaceWindow replySurface = "reply"
)
