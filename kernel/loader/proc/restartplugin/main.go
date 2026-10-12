// Command restartplugin 是 proc 后端的崩溃/重启测试辅助插件。
// 行为由环境变量 RESTART_SCENARIO 控制：
//   - "echo"（默认）：正常服务
//   - "crash"        ：握手成功后立即以退出码 7 退出
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/witchsnk/millipede/kernel/loader/proc"
)

type handler struct {
	started atomic.Int64
}

func (h *handler) Invoke(method string, args []byte) ([]byte, error) {
	switch method {
	case "Echo":
		return []byte("pong:" + string(args)), nil
	case "Scenario":
		return []byte(os.Getenv("RESTART_SCENARIO")), nil
	case "PID":
		return []byte(strconv.Itoa(os.Getpid())), nil
	case "StartCount":
		return []byte(strconv.FormatInt(h.started.Load(), 10)), nil
	case "Crash":
		// 主动崩溃，验证宿主能自动拉起新一代进程。
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(7)
		}()
		return []byte("crashing"), nil
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

	scenario := os.Getenv("RESTART_SCENARIO")
	if scenario == "" {
		scenario = "echo"
	}
	h := &handler{}
	h.started.Add(1)

	if scenario == "crash" {
		// 握手完成后立即崩溃，让宿主观察到退出码 7。
		go proc.ServePlugin(h)
		time.Sleep(200 * time.Millisecond)
		os.Exit(7)
	}

	if err := proc.ServePlugin(h); err != nil {
		fmt.Fprintf(os.Stderr, "restartplugin fatal: %v\n", err)
		os.Exit(1)
	}
}
