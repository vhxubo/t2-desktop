package main

// 界面字体：非 macOS 用随应用打包的鸿蒙字体，macOS 保留系统自带的苹方
// （PingFang SC）。字体版权与授权条款见 fonts/LICENSE，随包副本见
// resources/third_party/HarmonyOS_Sans_LICENSE.txt。
const appFontFamily = "HarmonyOS Sans SC"

// 官方发布的简体中文静态字体，只打包常规(400)与加粗(700)两个字重，各约
// 8MB。界面只用这两档；500/600 由系统就近匹配到其中之一，不再额外携带
// Medium 文件。未修改地使用官方字体，符合华为的字体授权条款。
//
// 字体只在需要它的平台嵌入：font_embed.go 负责 Windows/Linux 的嵌入与
// 注册，font_noembed.go 让 macOS 不携带这两份数据。
