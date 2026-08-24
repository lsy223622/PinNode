//go:build !cgo

// 该文件只用于没有 CGO/Android NDK 时的 Go 静态编译检查；正式 Android
// 构建仍使用 log.go 的 Android logcat 实现。
package libtailscale

import "log"

func initLogging(_ AppContext) {
	log.SetFlags(log.Flags() &^ log.LstdFlags)
}
