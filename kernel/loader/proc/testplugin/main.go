// Command testplugin 是 proc 后端的测试辅助插件：实现 Echo 方法。
package main

import (
	"fmt"
	"os"

	"github.com/millipede/millipede/kernel/loader/proc"
)

type handler struct{}

func (h *handler) Invoke(method string, args []byte) ([]byte, error) {
	switch method {
	case "Echo":
		return []byte("pong:" + string(args)), nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func main() {

	// 约束 C1：最先套用宿主下发的进程级资源限制，早于任何业务代码。
	if err := proc.ApplyRlimits(); err != nil {
		fmt.Fprintln(os.Stderr, "apply rlimits:", err)
		os.Exit(1)
	}

	if err := proc.ServePlugin(&handler{}); err != nil {
		fmt.Fprintf(os.Stderr, "testplugin fatal: %v\n", err)
		os.Exit(1)
	}
}
