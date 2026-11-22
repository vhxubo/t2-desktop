//go:build darwin

package main

// macOS 的系统字体对中文就是苹方，按用户要求不覆盖。字体数据不嵌入，
// 两份数据都保持 nil，二进制因此省下约 16MB。
const (
	harmonySansSCRegularFile = "fonts/HarmonyOS_Sans_SC_Regular.ttf"
	harmonySansSCBoldFile    = "fonts/HarmonyOS_Sans_SC_Bold.ttf"
)

var (
	harmonySansSCRegularData []byte
	harmonySansSCBoldData    []byte
)

func usesHarmonySans() bool { return false }
