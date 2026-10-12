// +build ignore

// Package notifier 是一个 builtin 示例插件，演示契约实现与后台任务规范。
// 该插件通过 init() + 注册表编译进主程序（约束 B3/B4）。
//go:generate go run ./cmd/gen
//plugin:export name=example-notifier api=1.0 backend=builtin extension=notifier
package notifier

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/millipede/millipede/kernel/lifecycle"
	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/loader/builtin"
	"github.com/millipede/millipede/sdk/contract"
	"github.com/millipede/millipede/sdk/host"
)

// Notifier 实现 contract.Notifier 的示例插件。
type Notifier struct {
	lifecycle.Base
	env     host.Env
	sv      *host.Supervisor
	sent    atomic.Int64
	stopped atomic.Bool
}

// New 构造插件实例（builtin.Loader 的工厂）。
func New(env host.Env) (lifecycle.Lifecycle, error) {
	return &Notifier{env: env}, nil
}

// Name 实现 contract.Extension。
func (n *Notifier) Name() string { return "example-notifier" }

// APIVersion 实现 contract.Extension。
func (n *Notifier) APIVersion() contract.APIVersion {
	return contract.APIVersion{Major: 1, Minor: 0}
}

// Init 装配依赖、校验配置（约束 B1：不得在此启动后台任务）。
func (n *Notifier) Init(ctx context.Context) error {
	endpoint := n.env.Config.GetString("endpoint", "")
	if endpoint == "" {
		return fmt.Errorf("notifier: missing required config endpoint")
	}
	host.Log(ctx).Info(ctx, "notifier init", "endpoint", endpoint)
	return nil
}

// Start 启动后台任务，任务挂在传入 ctx 上（约束 B3）。
func (n *Notifier) Start(ctx context.Context) error {
	n.sv = host.NewSupervisor(ctx)
	// 示例后台任务：定期打点。真实插件可在此起 worker。
	n.sv.Go(func(cctx context.Context) error {
		<-cctx.Done()
		return nil
	})
	n.Base.SetStop(func(sctx context.Context) error {
		if n.stopped.Swap(true) {
			return nil
		}
		return n.sv.Wait()
	})
	host.Log(ctx).Info(ctx, "notifier started")
	return nil
}

// Send 实现 contract.Notifier（约束 A3：带 ctx 返回 error）。
func (n *Notifier) Send(ctx context.Context, msg contract.Message) (contract.SendResult, error) {
	select {
	case <-ctx.Done():
		return contract.SendResult{}, ctx.Err()
	default:
	}
	n.sent.Add(1)
	host.Log(ctx).Info(ctx, "notifier send", "topic", msg.Topic, "id", msg.ID)
	return contract.SendResult{Accepted: true, Detail: "ok"}, nil
}

// Sent 返回已发送计数（演示状态查询）。
func (n *Notifier) Sent() int64 { return n.sent.Load() }

// Register 在启动期把插件注册进 builtin 后端。
func Register(l *builtin.Loader) error {
	return l.Register(builtin.Plugin{
		Desc: loader.Descriptor{
			ID:         "example-notifier",
			Kind:       "notifier",
			Backend:    loader.BackendBuiltin,
			APIVersion: contract.APIVersion{Major: 1, Minor: 0},
		},
		New: New,
		Invoker: func(p lifecycle.Lifecycle, method string, args any) (any, error) {
			n, ok := p.(*Notifier)
			if !ok {
				return nil, fmt.Errorf("notifier: wrong lifecycle type %T", p)
			}
			switch method {
			case "Send":
				msg, ok := args.(contract.Message)
				if !ok {
					return nil, fmt.Errorf("notifier: Send args must be contract.Message, got %T", args)
				}
				return n.Send(context.Background(), msg)
			default:
				return nil, fmt.Errorf("notifier: unknown method %q", method)
			}
		},
	})
}
