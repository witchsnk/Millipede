// Package protocol 定义 proc 后端的握手与 RPC 协议。
//
// 关键坑（见 docs/04 风险清单）：
//   - 插件绝对不能往 stdout 写业务日志（握手走 stdout 传端口）。
//     这里握手用 stdout 传一行 "MILLIPEDE|<cookie>|<port>"，
//     之后 stdout 不再使用；业务 RPC 走 TCP，日志走 stderr 或独立文件。
//   - 就绪竞态：端口还没监听就拨号会 connection refused，需要重试+超时。
//   - 退出码分层：非 0=启动失败；RPC 错误=插件内部异常；超时=协议/网络。
package protocol

import (
	"fmt"
	"strings"
)

// MagicPrefix 握手行前缀，用于在 stdout 中识别握手信息并抵御日志污染。
const MagicPrefix = "MILLIPEDE|"

// Handshake 握手信息。
type Handshake struct {
	Cookie string // 随机 magic cookie，防止端口被误连
	Port   int    // 插件 RPC 服务监听端口
}

// Encode 编码握手行。
func (h Handshake) Encode() string {
	return fmt.Sprintf("%s%s|%d", MagicPrefix, h.Cookie, h.Port)
}

// Decode 解析握手行；非握手行返回 ok=false。
func Decode(line string) (Handshake, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, MagicPrefix) {
		return Handshake{}, false
	}
	rest := strings.TrimPrefix(line, MagicPrefix)
	parts := strings.SplitN(rest, "|", 2)
	if len(parts) != 2 {
		return Handshake{}, false
	}
	var port int
	if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil {
		return Handshake{}, false
	}
	return Handshake{Cookie: parts[0], Port: port}, true
}

// ---- RPC 请求/响应（跨边界只传可序列化值，约束 A5） ----

// InvokeRequest RPC 调用请求。
type InvokeRequest struct {
	Cookie string // 每次调用都校验，防止误连
	Method string
	Args   []byte // 已编码（gob/json）的参数
}

// InvokeResponse RPC 调用响应。
type InvokeResponse struct {
	OK    bool
	Data  []byte
	Error string
}

// PingRequest 心跳请求。
type PingRequest struct {
	Cookie string
}

// PingResponse 心跳响应。
type PingResponse struct {
	Alive bool
}
