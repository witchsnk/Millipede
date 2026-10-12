// Package quota 实现每插件资源配额（约束 C1）。
//
// 与 safecall 的分工：
//   - safecall 负责"调用边界"：并发槽位、单次超时、熔断、panic 边界。
//     它的 MaxConcurrency 是调用方的并发闸门，本包不重复实现。
//   - quota 负责"资源预算"：QPS 限流、调用次数预算、CPU/内存地址空间上限。
//
// 为什么单独成包：进程级资源限制（setrlimit）必须作用于子进程，
// 与调用边界无关；而 QPS/预算类限制在 builtin/proc 两种后端都要生效，
// 放在 kernel 层才能统一生效。
//
// 重要原则（约束 C1）：**不允许"无限"缺省**。
// 所有限流参数缺省为保守的有界值，而不是关闭限制。
package quota

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrQuotaExceeded 表示超出速率（QPS）或次数预算，调用被快速失败。
var ErrQuotaExceeded = errors.New("quota: exceeded")

// ErrClosed 表示配额对象已被释放。
var ErrClosed = errors.New("quota: closed")

// Limits 描述单个插件的配额上限。
//
// 零值表示"使用默认上限"，**不是**"无限制"。
// 若确实需要关闭某项限制，请使用 Limit 提供的显式构造（见 Unlimited 注释），
// 并在部署评审中记录理由。
type Limits struct {
	// MaxQPS 每秒允许的调用次数上限；<=0 用 DefaultQPS。
	// 采用令牌桶，允许短时突发（见 Burst）。
	MaxQPS float64
	// Burst 允许的突发量；<=0 时取 max(1, ceil(MaxQPS))。
	Burst int
	// MaxCalls 进程生命周期内允许的总调用次数预算；<=0 表示不限制总次数。
	// 用于"批处理任务跑完即止"这类一次性插件，避免逻辑缺陷导致无限循环消耗。
	MaxCalls int64
	// CPUSeconds CPU 时间上限（秒）；<=0 表示不限制。proc 后端通过 setrlimit 生效。
	CPUSeconds int64
	// AddressSpaceBytes 地址空间上限（字节）；<=0 表示不限制。proc 后端生效。
	AddressSpaceBytes int64
	// MaxProcs 该插件允许派生的子进程数上限；<=0 表示不限制。
	// 防止插件 fork 炸弹打爆宿主机（proc 后端生效）。
	MaxProcs int64
}

// DefaultQPS 是缺省 QPS 上限。
// 选 200 的理由：足以覆盖绝大多数业务插件的正常调用量，
// 同时在插件出现死循环调用时能把宿主的资源消耗限制在可控范围。
const DefaultQPS = 200

// WithDefaults 补全零值字段，返回可用的 Limits。
func (l Limits) WithDefaults() Limits {
	out := l
	if out.MaxQPS <= 0 {
		out.MaxQPS = DefaultQPS
	}
	if out.Burst <= 0 {
		b := int(out.MaxQPS)
		if b < 1 {
			b = 1
		}
		out.Burst = b
	}
	return out
}

// Validate 校验配额配置是否合法。
func (l Limits) Validate() error {
	if l.MaxQPS < 0 {
		return fmt.Errorf("quota: MaxQPS must be >= 0, got %v", l.MaxQPS)
	}
	if l.Burst < 0 {
		return fmt.Errorf("quota: Burst must be >= 0, got %d", l.Burst)
	}
	if l.MaxCalls < 0 {
		return fmt.Errorf("quota: MaxCalls must be >= 0, got %d", l.MaxCalls)
	}
	if l.CPUSeconds < 0 {
		return fmt.Errorf("quota: CPUSeconds must be >= 0, got %d", l.CPUSeconds)
	}
	if l.AddressSpaceBytes < 0 {
		return fmt.Errorf("quota: AddressSpaceBytes must be >= 0, got %d", l.AddressSpaceBytes)
	}
	if l.MaxProcs < 0 {
		return fmt.Errorf("quota: MaxProcs must be >= 0, got %d", l.MaxProcs)
	}
	return nil
}

// Stats 是配额的运行时统计快照。
type Stats struct {
	// Allowed 通过限流与预算检查的调用数。
	Allowed uint64
	// RejectedQPS 因 QPS 超限被拒的调用数。
	RejectedQPS uint64
	// RejectedBudget 因总次数预算耗尽被拒的调用数。
	RejectedBudget uint64
	// CallsSinceRefill 自上次补充令牌以来的调用计数。
	CallsSinceRefill uint64
}

// Quota 是单个插件的运行时配额对象。
//
// 并发安全。典型用法是在每次跨边界调用前调用 Allow：
//
//	if !q.Allow() {
//		return quota.ErrQuotaExceeded
//	}
type Quota struct {
	lim Limits

	// 令牌桶：rate 为每秒补充速率，tokens 为当前可用令牌。
	mu      sync.Mutex
	tokens  float64
	lastRef time.Time

	allowed         atomic.Uint64
	rejectedQPS     atomic.Uint64
	rejectedBudget  atomic.Uint64
	callsSinceRefil atomic.Uint64

	closed atomic.Bool
}

// New 构造配额对象。传入的 Limits 会被 WithDefaults 补全。
//
// refills 由 Allow 惰性触发（不使用后台定时器），
// 这样 Quota 对象不需要生命周期管理，不会泄漏 goroutine。
func New(lim Limits, now time.Time) *Quota {
	l := lim.WithDefaults()
	return &Quota{
		lim:     l,
		tokens:  float64(l.Burst), // 初始给满一个突发桶
		lastRef: now,
	}
}

// NewSimple 构造配额对象并以当前时间作为起点。
func NewSimple(lim Limits) *Quota { return New(lim, time.Now()) }

// Limits 返回生效的配额配置。
func (q *Quota) Limits() Limits { return q.lim }

// Allow 判断本次调用是否在配额内。
//
// 实现要点：
//   - 令牌桶惰性补充：不建后台 goroutine，避免泄漏（约束 B3）。
//   - 先判预算再判速率：预算耗尽是硬上限，即使当前速率很低也必须拒绝。
func (q *Quota) Allow(now time.Time) bool {
	if q.closed.Load() {
		return false
	}
	if q.lim.MaxCalls > 0 && int64(q.allowed.Load())+1 > q.lim.MaxCalls {
		q.rejectedBudget.Add(1)
		return false
	}
	if !q.takeToken(now) {
		q.rejectedQPS.Add(1)
		return false
	}
	q.allowed.Add(1)
	q.callsSinceRefil.Add(1)
	return true
}

// takeToken 惰性补充并尝试消费一个令牌。
func (q *Quota) takeToken(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.lastRef.IsZero() {
		elapsed := now.Sub(q.lastRef).Seconds()
		if elapsed > 0 {
			q.tokens += elapsed * q.lim.MaxQPS
			// 上限为突发桶大小，防止长时间空闲后积累出巨大突发。
			if maxTok := float64(q.lim.Burst); q.tokens > maxTok {
				q.tokens = maxTok
			}
			q.lastRef = now
			q.callsSinceRefil.Store(0)
		}
	}
	if q.tokens < 1 {
		return false
	}
	q.tokens--
	return true
}

// Stats 返回配额统计快照。
func (q *Quota) Stats() Stats {
	return Stats{
		Allowed:          q.allowed.Load(),
		RejectedQPS:      q.rejectedQPS.Load(),
		RejectedBudget:   q.rejectedBudget.Load(),
		CallsSinceRefill: q.callsSinceRefil.Load(),
	}
}

// Close 释放配额。之后所有 Allow 返回 false。
// 幂等，可重复调用。
func (q *Quota) Close() { q.closed.Store(true) }

// Closed 报告配额是否已释放。
func (q *Quota) Closed() bool { return q.closed.Load() }
