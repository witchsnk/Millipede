package host

import (
	"context"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// Supervisor 帮助插件把后台任务挂在传入的 ctx 上（约束 B3）。
// 插件禁止裸起常驻 goroutine，必须通过 Supervisor 托管，
// 以保证 Stop 时能统一回收，避免热更后的 goroutine 泄漏。
type Supervisor struct {
	g       *errgroup.Group
	ctx     context.Context
	stopped atomic.Bool
}

// NewSupervisor 以插件 Start 收到的 ctx 构造监督器。
func NewSupervisor(ctx context.Context) *Supervisor {
	g, c := errgroup.WithContext(ctx)
	return &Supervisor{g: g, ctx: c}
}

// Go 启动一个受监督的任务。任务返回 error 会被记录下来并取消整个监督器。
func (s *Supervisor) Go(fn func(ctx context.Context) error) {
	s.g.Go(func() error { return fn(s.ctx) })
}

// Ctx 返回监督器的 context（任务应监听其 Done）。
func (s *Supervisor) Ctx() context.Context { return s.ctx }

// Wait 等待所有任务退出，返回首个非 nil 错误。幂等。
func (s *Supervisor) Wait() error {
	if s.stopped.Swap(true) {
		// 已经 Wait 过：直接返回 nil，保证 Stop 幂等（约束 B1）。
		return nil
	}
	return s.g.Wait()
}
