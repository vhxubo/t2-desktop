package main

import (
	"bytes"
	"io"

	"github.com/egoist/mygo/ui"
)

// 动图识别：预览窗口要按帧播放 gif 与 apng，正文里仍只画首帧。
//
// mygo 自带这两个格式的解码器（ui.NewGIFDecoder / ui.NewAPNGDecoder），在 Go
// 里逐帧解，任何平台都能放。这里只做两件事：认出格式，并且确认它真的不止
// 一帧——只有一帧的 gif 与普通图片没有区别，交给 ui.Image 画更省内存。
//
// 动画 WebP 目前没有纯 Go 的解码器（x/image/webp 只解首帧），按静态图显示；
// 正文与预览里都至少能看到第一帧。

var (
	gifMagic = []byte("GIF8")
	pngMagic = []byte("\x89PNG\r\n\x1a\n")
	// apngMagic 是动画 PNG 的 acTL 块标记：它必须出现在第一帧数据之前。
	apngMagic = []byte("acTL")
)

// animatedImageMaxBytes 是允许当动图播放的文件上限。再大的文件解码出来是
// 几百 MB 的像素，播放不如直接看首帧。
const animatedImageMaxBytes = 32 << 20

// animatedDecoder 给一份图片字节返回逐帧解码器：gif 与 apng 且不止一帧时
// ok 为 true，其余情况（静态图、动画 WebP、超大文件）为 false。
func animatedDecoder(data []byte) (ui.VideoDecoder, bool) {
	if len(data) == 0 || len(data) > animatedImageMaxBytes {
		return nil, false
	}
	var (
		dec ui.VideoDecoder
		err error
	)
	switch {
	case bytes.HasPrefix(data, gifMagic):
		dec, err = ui.NewGIFDecoder(data)
	case bytes.HasPrefix(data, pngMagic):
		head := data[:min(len(data), 64<<10)]
		if !bytes.Contains(head, apngMagic) {
			return nil, false
		}
		dec, err = ui.NewAPNGDecoder(data)
	default:
		return nil, false
	}
	if err != nil || dec == nil {
		return nil, false
	}
	// 试着解两帧：第二帧就是结尾说明只有一帧，按静态图处理。
	if _, err := dec.Next(); err != nil {
		return nil, false
	}
	if _, err := dec.Next(); err == io.EOF {
		return nil, false
	}
	if err := dec.Rewind(); err != nil {
		return nil, false
	}
	return dec, true
}
