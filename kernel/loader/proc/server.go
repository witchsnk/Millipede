// Package proc 插件侧（子进程）RPC 服务端与宿主侧（父进程）客户端。
//
// 本文件是插件侧服务端：插件 main 调用 ServePlugin，
// 监听随机端口、向 stdout 打印握手行，然后以 net/rpc 暴露 Invoke/Ping。
package proc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/rpc"
	"os"

	"github.com/witchsnk/millipede/kernel/loader/proc/protocol"
	"github.com/witchsnk/millipede/kernel/quota"
	"github.com/witchsnk/millipede/sdk/contract"
)

// PluginHandler 插件侧业务处理器：由具体插件实现。
type PluginHandler interface {
	// Invoke 处理一次方法调用。args/reply 都是已编码字节，由插件自行解码。
	Invoke(method string, args []byte) ([]byte, error)
}

// RPCService 是 net/rpc 暴露的服务（必须导出）。
type RPCService struct {
	cookie  string
	handler PluginHandler
}

// Invoke RPC 方法（约束 A5：跨边界只传可序列化值）。
func (s *RPCService) Invoke(req protocol.InvokeRequest, resp *protocol.InvokeResponse) error {
	if req.Cookie != s.cookie {
		resp.OK = false
		resp.Error = "bad cookie"
		return nil
	}
	data, err := s.handler.Invoke(req.Method, req.Args)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		return nil
	}
	resp.OK = true
	resp.Data = data
	return nil
}

// Ping 心跳。
func (s *RPCService) Ping(req protocol.PingRequest, resp *protocol.PingResponse) error {
	resp.Alive = req.Cookie == s.cookie
	return nil
}

// ApplyRlimits 套用宿主下发的进程级资源限制（约束 C1）。
//
// 插件 main 的**第一件事**必须调用它，越早越好。
//
// 为什么由插件自己套用而不是宿主设置：Go 的 os/exec 没有为子进程设置
// rlimit 的钩子（syscall.SysProcAttr 在 linux/darwin 上都没有该字段），
// 唯一可行的方式是子进程在执行任何插件代码前自行调用 setrlimit(2)。
//
// 调用方式：
//
//	func main() {
//		if err := proc.ApplyRlimits(); err != nil {
//			fmt.Fprintln(os.Stderr, "rlimit:", err)
//			os.Exit(1)
//		}
//		proc.ServePlugin(&myHandler{})
//	}
//
// 返回错误时**应当退出**：静默忽略会让人以为限制生效了，
// 那正是约束 C1 要防的情况。
func ApplyRlimits() error {
	limits, err := quota.ParseEnv(os.Getenv(contract.RLimitEnvVar))
	if err != nil {
		return err
	}
	if len(limits) == 0 {
		return nil
	}
	_, err = quota.SelfApply(limits)
	return err
}

// ServePlugin 启动插件 RPC 服务端并打印握手行。
// cookie 为空则自动生成。该函数阻塞直至监听失败。
func ServePlugin(handler PluginHandler) error {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return fmt.Errorf("proc: gen cookie: %w", err)
	}
	cookieStr := hex.EncodeToString(cookie)

	svc := &RPCService{cookie: cookieStr, handler: handler}
	server := rpc.NewServer()
	if err := server.Register(svc); err != nil {
		return fmt.Errorf("proc: register rpc: %w", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("proc: listen: %w", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port

	// 握手：向 stdout 打印一行，之后不再使用 stdout（防止污染）。
	fmt.Println(protocol.Handshake{Cookie: cookieStr, Port: port}.Encode())
	os.Stdout.Sync()

	for {
		conn, err := lis.Accept()
		if err != nil {
			return fmt.Errorf("proc: accept: %w", err)
		}
		go server.ServeConn(conn)
	}
}
