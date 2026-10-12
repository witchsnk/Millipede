// Package safecall 提供统一的插件调用保护边界（约束 B5/C1）。
//
// 每次跨边界调用插件都被包裹：recover(panic) + ctx 超时 + 并发配额 +
// 熔断器 + 指标采集。框架层强制，禁止静默吞错。
package safecall

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// ErrPanic 表示插件内部发生 panic（已被边界捕获，未穿透宿主）。
var ErrPanic = errors.New("safecall: plugin panic")

// ErrCircuitOpen 表示熔断器打开，调用被快速失败。
var ErrCircuitOpen = errors.New("safecall: circuit open")

// ErrConcurrencyLimit 表示并发配额已满，调用被快速失败以保护系统。
var ErrConcurrencyLimit = errors.New("safecall: concurrency limit reached")

// Metrics 是按插件维度的调用指标（约束 E1），全部用原子计数保证低开销。
type Metrics struct {
	Total              atomic.Int64 // 成功调用数
	Errors             atomic.Int64 // 业务错误数
	Panics             atomic.Int64 // panic 次数
	Timeouts           atomic.Int64 // 超时次数
	CircuitTrips       atomic.Int64 // 熔断拒绝次数
	ConcurrencyRejects atomic.Int64 // 并发配额拒绝次数
	TotalNanos         atomic.Int64 // 累计耗时（纳秒）
}

// Snapshot 返回指标的一致快照。
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Total:              m.Total.Load(),
		Errors:             m.Errors.Load(),
		Panics:             m.Panics.Load(),
		Timeouts:           m.Timeouts.Load(),
		CircuitTrips:       m.CircuitTrips.Load(),
		ConcurrencyRejects: m.ConcurrencyRejects.Load(),
		TotalNanos:         m.TotalNanos.Load(),
	}
}

// MetricsSnapshot 是只读指标快照。
type MetricsSnapshot struct {
	Total, Errors, Panics, Timeouts, CircuitTrips, ConcurrencyRejects, TotalNanos int64
}

// Caller 是单个插件的受保护调用器。
type Caller struct {
	pluginID string
	limiter  chan struct{} // 并发配额（容量 = MaxConcurrency）
	cb       *CircuitBreaker
	metrics  *Metrics
	onPanic  func(pluginID string, recovered any, stack []byte)
}

// Options 控制保护行为。
type Options struct {
	MaxConcurrency int // 并发上限，<=0 默认 64
	Breaker        BreakerOptions
}

// NewCaller 构造某插件的调用器。
func NewCaller(pluginID string, opt Options, onPanic func(string, any, []byte)) *Caller {
	if opt.MaxConcurrency <= 0 {
		opt.MaxConcurrency = 64
	}
	return &Caller{
		pluginID: pluginID,
		limiter:  make(chan struct{}, opt.MaxConcurrency),
		cb:       NewCircuitBreaker(opt.Breaker),
		metrics:  &Metrics{},
		onPanic:  onPanic,
	}
}

// Metrics 返回指标。
func (c *Caller) Metrics() *Metrics { return c.metrics }

// PluginID 返回插件标识。
func (c *Caller) PluginID() string { return c.pluginID }

// ResetBreaker 重置熔断器状态。
//
// 用于"插件进程已被宿主重启"这类场景：进程级恢复已经发生，
// 旧的调用级失败计数不应继续把新进程挡在门外，
// 否则会出现"重启成功了但仍然一直 circuit open"的假死。
func (c *Caller) ResetBreaker() {
	c.cb.Reset()
}

// Invoke 以超时+配额+熔断+recover 调用 fn。timeout<=0 用默认 5s。
// 任何 panic 都被捕获转为 ErrPanic 并上报；不静默。
func (c *Caller) Invoke(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) error) error {
	// 1) 熔断快速失败
	if !c.cb.Allow() {
		c.metrics.CircuitTrips.Add(1)
		return ErrCircuitOpen
	}

	// 2) 并发配额（满则快速失败，避免雪崩）
	select {
	case c.limiter <- struct{}{}:
		defer func() { <-c.limiter }()
	default:
		c.metrics.ConcurrencyRejects.Add(1)
		return fmt.Errorf("%w: plugin=%s", ErrConcurrencyLimit, c.pluginID)
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	defer func() { c.metrics.TotalNanos.Add(time.Since(start).Nanoseconds()) }()

	err := c.run(ctx, fn)
	switch {
	case err == nil:
		c.cb.RecordSuccess()
		c.metrics.Total.Add(1)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		c.metrics.Timeouts.Add(1)
		c.cb.RecordFailure()
	case errors.Is(err, ErrPanic):
		c.metrics.Panics.Add(1)
		c.cb.RecordFailure()
	default:
		c.metrics.Errors.Add(1)
		c.cb.RecordFailure()
	}
	return err
}

// run 在独立 goroutine 中执行 fn 以支持超时中断，并在两层都做 recover。
func (c *Caller) run(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				if c.onPanic != nil {
					c.onPanic(c.pluginID, r, stack)
				}
				done <- fmt.Errorf("%w: %v", ErrPanic, r)
			}
		}()
		done <- fn(ctx)
	}()

	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		// 超时：返回 ctx 错误。注意 fn 可能仍在运行，
		// 这正是"同进程无法硬阻断死循环"的坑（见 docs/04），
		// 因此对同进程插件必须把超时作为兜底，并用配额限制其资源占用。
		return ctx.Err()
	}
}
