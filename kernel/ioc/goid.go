package ioc

import (
	"bytes"
	"runtime"
	"strconv"
)

// goid 返回当前 goroutine 的 id。
//
// 说明：这是运行时诊断手段，仅用于"解析链"的 goroutine 定位，
// 即只在**需要构造单例**（低频，一次性）时调用；已构造好的热路径直接返回，
// 不会进入这里，因此不影响运行效率。
//
// 栈帧格式："goroutine 123 [running]:"
func goid() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	if n == 0 {
		return 0
	}
	s := buf[:n]
	s = bytes.TrimPrefix(s, []byte("goroutine "))
	i := bytes.IndexByte(s, ' ')
	if i <= 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(s[:i]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
