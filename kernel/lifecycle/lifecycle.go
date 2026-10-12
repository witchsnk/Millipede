// Package lifecycle 实现插件生命周期状态机与失败回滚（约束 B1/B2）。
//
// 顺序：Init -> Start；任一失败则对已成功的实例逆序 Stop（回滚）。
// Stop 必须幂等且带超时，否则关闭/热更时卡死（见 docs/04 风险清单）。
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Phase 生命周期阶段。
type Phase int32

const (
	PhaseNew Phase = iota
	PhaseInitialized
	PhaseStarted
	PhaseStopped
	PhaseFailed
)

// String 阶段名。
func (p Phase) String() string {
	switch p {
	case PhaseNew:
		return "New"
	case PhaseInitialized:
		return "Initialized"
	case PhaseStarted:
		return "Started"
	case PhaseStopped:
		return "Stopped"
	case PhaseFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// Lifecycle 是插件必须实现的显式生命周期接口。
// 对比 Java 的 @PostConstruct/DisposableBean，显式接口更可控、可测。
type Lifecycle interface {
	// Init 装配依赖、校验配置。不得启动后台任务。
	Init(ctx context.Context) error
	// Start 启动后台任务，任务必须挂在 ctx 上。
	Start(ctx context.Context) error
	// Stop 停止并回收资源。必须幂等 + 在 ctx 超时前返回。
	Stop(ctx context.Context) error
}

// Manager 按依赖顺序拉起一组插件，失败时逆序回滚。
type Manager struct {
	mu      sync.Mutex
	started []managed // 已成功 Start 的，按启动顺序，回滚时逆序
}

type managed struct {
	name string
	lc   Lifecycle
}

// NewManager 构造生命周期管理器。
func NewManager() *Manager { return &Manager{} }

// Options 控制超时。
type Options struct {
	InitTimeout  time.Duration
	StartTimeout time.Duration
	StopTimeout  time.Duration
}

// DefaultOptions 给出安全的缺省超时（约束 B1：不允许"无限"）。
func DefaultOptions() Options {
	return Options{
		InitTimeout:  10 * time.Second,
		StartTimeout: 10 * time.Second,
		StopTimeout:  15 * time.Second,
	}
}

// Boot 按给定顺序对一组 (name, Lifecycle) 执行 Init->Start。
// 任一环节失败：先对当前失败的调用止损，再逆序 Stop 所有已 Start 成功的实例。
// 返回的第一个错误是"根因"，回滚错误会包裹其后。
func (m *Manager) Boot(ctx context.Context, opt Options, items ...BootItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, it := range items {
		// Init
		ictx, cancel := context.WithTimeout(ctx, opt.InitTimeout)
		err := it.LC.Init(ictx)
		cancel()
		if err != nil {
			root := fmt.Errorf("lifecycle: init %q: %w", it.Name, err)
			return m.rollback(ctx, opt, root)
		}

		// Start
		sctx, cancel := context.WithTimeout(ctx, opt.StartTimeout)
		err = it.LC.Start(sctx)
		cancel()
		if err != nil {
			root := fmt.Errorf("lifecycle: start %q: %w", it.Name, err)
			return m.rollback(ctx, opt, root)
		}

		m.started = append(m.started, managed{name: it.Name, lc: it.LC})
	}
	return nil
}

// BootItem 一个待启动的插件。
type BootItem struct {
	Name string
	LC   Lifecycle
}

// rollback 逆序 Stop 已启动实例，并返回根因+回滚错误的聚合。
func (m *Manager) rollback(ctx context.Context, opt Options, root error) error {
	var errs []error
	errs = append(errs, root)
	for i := len(m.started) - 1; i >= 0; i-- {
		it := m.started[i]
		sctx, cancel := context.WithTimeout(ctx, opt.StopTimeout)
		if err := it.lc.Stop(sctx); err != nil {
			errs = append(errs, fmt.Errorf("lifecycle: rollback stop %q: %w", it.name, err))
		}
		cancel()
	}
	m.started = nil
	return errors.Join(errs...)
}

// Shutdown 逆序 Stop 全部已启动实例（进程退出/热更排空）。
func (m *Manager) Shutdown(ctx context.Context, opt Options) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for i := len(m.started) - 1; i >= 0; i-- {
		it := m.started[i]
		sctx, cancel := context.WithTimeout(ctx, opt.StopTimeout)
		if err := it.lc.Stop(sctx); err != nil {
			errs = append(errs, fmt.Errorf("lifecycle: shutdown stop %q: %w", it.name, err))
		}
		cancel()
	}
	m.started = nil
	return errors.Join(errs...)
}

// Base 提供可嵌入的默认实现，帮助插件作者满足"Stop 幂等"约束。
// 用法：type P struct { lifecycle.Base ... }
type Base struct {
	stopped sync.Once
	stopFn  func(ctx context.Context) error
}

// SetStop 注册真正的停止逻辑（只会被执行一次）。
func (b *Base) SetStop(fn func(ctx context.Context) error) { b.stopFn = fn }

// Stop 幂等地执行停止逻辑。
func (b *Base) Stop(ctx context.Context) error {
	var err error
	b.stopped.Do(func() {
		if b.stopFn != nil {
			err = b.stopFn(ctx)
		}
	})
	return err
}
