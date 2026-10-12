//go:build !linux

package quota

import (
	"errors"
	"fmt"

	"github.com/millipede/millipede/sdk/contract"
)

// ErrPlatformUnsupported 表示当前平台无法设置进程级 rlimit。
//
// 已知平台情况（诚实标注，遵循约束 G1）：
//   - linux：支持，见 rlimit.go。子进程自行 setrlimit。
//   - darwin（macOS）：Go 的 syscall.SysProcAttr 没有 Rlimit 字段，
//     而 macOS 的 setrlimit(2) 必须由子进程在 fork 后、exec 前调用。
//     框架通过环境变量下发限制的机制在 linux/darwin 上是同一套代码，
//     但 macOS 上 Setrlimit 对部分资源（如 RLIMIT_AS）的语义与 linux 不同，
//     且 RLIMIT_NPROC 在 darwin 上不可用。
//
// 这是**平台限制**，不是实现缺失。为避免"以为生效了其实没生效"，
// 本平台上只要配置了进程级限制就返回明确错误，
// 由宿主决定是降级（去掉进程级限制、保留 QPS/预算）还是拒绝启动插件。
//
// 影响面：darwin 上进程级限制不生效，QPS 限流与调用次数预算
// （纯宿主侧逻辑）仍然完全生效。生产部署以 Linux 为准。
var ErrPlatformUnsupported = errors.New("quota: process rlimit not supported on this platform")

// BuildLimits 在不支持的平台返回 nil，由调用方按 ErrPlatformUnsupported 处理。
func BuildLimits(Limits) []contract.RLimit { return nil }

// PlatformSupported 报告当前平台是否支持进程级 rlimit。
func PlatformSupported() bool { return false }

// EncodeEnv 在不支持的平台上返回空串。
func EncodeEnv(l Limits) string { return "" }

// ParseEnv 解析环境变量；在不支持的平台上解析失败即视为错误。
func ParseEnv(s string) ([]contract.RLimit, error) {
	if s == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: cannot parse %s on this platform", ErrPlatformUnsupported, contract.RLimitEnvVar)
}

// SelfApply 在不支持的平台上直接报错，绝不静默返回成功。
func SelfApply(limits []contract.RLimit) (int, error) {
	if len(limits) == 0 {
		return 0, nil
	}
	return 0, fmt.Errorf("%w: %d limit(s) requested", ErrPlatformUnsupported, len(limits))
}
