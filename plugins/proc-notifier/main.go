// Command proc-notifier 是一个 proc 后端示例插件：
// 独立可执行文件，通过 net/rpc 暴露 Invoke/Ping，演示子进程插件形态。
//go:generate go run ./cmd/gen
//plugin:export name=proc-notifier api=1.0 backend=proc extension=notifier
// 约束：日志只走 stderr，绝不往 stdout 写业务日志（握手走 stdout）。
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/millipede/millipede/kernel/loader/proc"
	"github.com/millipede/millipede/sdk/contract"
)

// handler 实现 proc.PluginHandler。
type handler struct{}

// Invoke 处理宿主调用。method 决定行为，args 为 JSON 编码的 contract.Message。
func (h *handler) Invoke(method string, args []byte) ([]byte, error) {
	switch method {
	case "Send":
		var msg contract.Message
		if len(args) > 0 {
			if err := json.Unmarshal(args, &msg); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
		}
		// 业务日志走 stderr，绝不能走 stdout（约束：握手防污染）。
		fmt.Fprintf(os.Stderr, "[proc-notifier] send topic=%s id=%s\n", msg.Topic, msg.ID)
		out, _ := json.Marshal(contract.SendResult{Accepted: true, Detail: "proc ok"})
		return out, nil
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

	// 启动 RPC 服务端并打印握手行（阻塞）。
	if err := proc.ServePlugin(&handler{}); err != nil {
		fmt.Fprintf(os.Stderr, "proc-notifier fatal: %v\n", err)
		os.Exit(1)
	}
}
