// Package loader 定义统一的插件加载后端抽象（见 docs/01 架构设计）。
//
// 四种后端（builtin/proc/wasm/interp）都实现同一 Loader 接口，
// 业务代码只面向 sdk，永远不知道插件是编译进来的还是子进程跑的。
package loader

import (
	"context"
	"errors"
	"time"

	"github.com/witchsnk/millipede/sdk/contract"
)

// BackendKind 后端类型。
type BackendKind string

const (
	BackendBuiltin BackendKind = "builtin"
	BackendProc    BackendKind = "proc"
	BackendWASM    BackendKind = "wasm"
	BackendInterp  BackendKind = "interp"
)

// State 插件实例状态。
type State string

const (
	StateLoading  State = "Loading"
	StateReady    State = "Ready"
	StateDegraded State = "Degraded"
	StateFailed   State = "Failed"
	StateStopped  State = "Stopped"
)

// ErrNotSupported 表示该后端不支持某操作（如 builtin 不支持 Unload）。
var ErrNotSupported = errors.New("loader: operation not supported by backend")

// Descriptor 描述一个待加载的插件。
type Descriptor struct {
	ID         string      // 唯一标识
	Kind       string      // 扩展点类型，如 "notifier"
	Backend    BackendKind // builtin/proc/wasm/interp
	URI        string      // 路径/镜像/命令
	APIVersion contract.APIVersion
	Config     map[string]any
	// Checksum 是 sha256 十六进制摘要，加载前强制比对（约束 D1）。
	Checksum string
	// Signature 是 HMAC-SHA256 签名（十六进制）；用于区分"篡改"与"伪造"。
	Signature string
	// Version 与 Checksum/Signature 一起构成清单键（pluginID@version）。
	Version string
}

// Instance 是一个已加载的插件实例。
type Instance interface {
	ID() string
	State() State
	// Invoke 调用插件方法。args/返回必须可序列化（约束 A5）。
	Invoke(ctx context.Context, method string, args any) (any, error)
	// Close 关闭实例。幂等 + 超时。
	Close(ctx context.Context) error
}

// ReloadEvent 热更新事件。
type ReloadEvent struct {
	PluginID string
	Reason   string
}

// Loader 后端统一接口。
type Loader interface {
	Kind() BackendKind
	Load(ctx context.Context, d Descriptor) (Instance, error)
	// Unload 卸载实例；builtin 返回 ErrNotSupported。
	Unload(ctx context.Context, id string) error
	// Watch 监听热更新事件。
	Watch(ctx context.Context) (<-chan ReloadEvent, error)
}

// ErrNotLoaded 表示目标插件当前未加载（用于可观测查询，语义为"查无此项"）。
var ErrNotLoaded = errors.New("loader: plugin not loaded")

// Info 是 Loader.Inspect 返回的可观测详情（约束 E2）。
//
// 放在 loader 包而不是 observability 包，是为了避免可观测层
// 反向依赖 Loader 实现、形成依赖倒置。
type Info struct {
	// State 插件当前状态。
	State State
	// Version 插件版本（若描述符提供）。
	Version string
	// LoadedAt 加载时刻；零值表示未知。
	LoadedAt time.Time
	// Restarts 累计重启次数；无重启语义的后端恒为 0。
	Restarts int64
	// ExitCode 最近一代子进程退出码；非进程后端恒为 0。
	ExitCode int64
	// LastError 最近一次错误；nil 表示无。
	LastError error
	// WatchTracked 该插件是否被产物监听跟踪。
	WatchTracked bool
	// Quota 配额用量统计（约束 C1）。
	Quota QuotaStats
}

// QuotaStats 是配额使用情况的只读投影（约束 C1/E2）。
//
// 放在 loader 包而非 quota 包，是为了让 loader.Info 不依赖 quota，
// 避免 loader（抽象层）反向依赖 kernel/quota（实现层）。
type QuotaStats struct {
	// Allowed 通过限流与预算检查的调用数。
	Allowed uint64 `json:"allowed"`
	// RejectedQPS 因 QPS 超限被拒的调用数。
	RejectedQPS uint64 `json:"rejected_qps"`
	// RejectedBudget 因预算耗尽被拒的调用数。
	RejectedBudget uint64 `json:"rejected_budget"`
}

// Inspector 由 Loader 实现，用于按 ID 读取插件的可观测详情（约束 E2）。
//
// 约定：**可选接口**。通过类型断言探测，未实现时按"无详情"处理，
// 从而在**不修改** loader.Loader 的前提下扩展能力——满足约束 A2（只增不改）。
type Inspector interface {
	// Inspect 返回某插件的可观测详情。
	Inspect(ctx context.Context, id string) (Info, error)
}
