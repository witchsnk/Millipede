// Package builtin 实现编译期内置后端：插件以 init() + 注册表编译进主程序。
//
// 特点（见 docs/adr/ADR-001）：零运行时开销、可静态检查、可调试；
// 不可卸载（Unload 返回 ErrNotSupported）。这是默认主路径。
package builtin

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/millipede/millipede/kernel/lifecycle"
	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/quota"
	"github.com/millipede/millipede/kernel/safecall"
	"github.com/millipede/millipede/sdk/host"
)

// Plugin 是一个内置插件的注册单元。
type Plugin struct {
	Desc loader.Descriptor
	New  func(env host.Env) (lifecycle.Lifecycle, error)
	// Invoker 把 method+args 路由到插件的具体方法。
	// 由插件包提供，保证类型安全（避免反射，提升热路径性能）。
	Invoker func(p lifecycle.Lifecycle, method string, args any) (any, error)
}

// Loader 内置后端。
type Loader struct {
	mu        sync.RWMutex
	registry  map[string]Plugin    // id -> 注册单元（启动期写）
	instances map[string]*instance // id -> 已加载实例
	callerOpt safecall.Options
	// quotaLim 每插件资源配额（约束 C1）。builtin 与 proc 必须都接入，
	// 否则"每插件配额"只对一半后端成立，属于半成品。
	quotaLim quota.Limits
	// envFor 为插件构造运行环境（注入 Config/Logger），由宿主提供。
	envFor func(pluginID string) host.Env
}

// New 构造内置后端。envFor 可为 nil，则只注入 PluginID。
func New(callerOpt safecall.Options, envFor func(pluginID string) host.Env) *Loader {
	if envFor == nil {
		envFor = func(id string) host.Env { return host.Env{PluginID: id} }
	}
	return &Loader{
		registry:  make(map[string]Plugin),
		instances: make(map[string]*instance),
		callerOpt: callerOpt,
		envFor:    envFor,
	}
}

// Register 在启动期（init）注册插件。
func (l *Loader) Register(p Plugin) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.registry[p.Desc.ID]; ok {
		return fmt.Errorf("builtin: duplicate plugin id %q", p.Desc.ID)
	}
	l.registry[p.Desc.ID] = p
	return nil
}

// Kind 实现 loader.Loader。
func (l *Loader) Kind() loader.BackendKind { return loader.BackendBuiltin }

// Load 实例化并启动一个已注册插件。
func (l *Loader) Load(ctx context.Context, d loader.Descriptor) (loader.Instance, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.registry[d.ID]
	if !ok {
		return nil, fmt.Errorf("builtin: plugin %q not registered", d.ID)
	}
	if _, ok := l.instances[d.ID]; ok {
		return nil, fmt.Errorf("builtin: plugin %q already loaded", d.ID)
	}
	env := l.envFor(d.ID)
	lc, err := p.New(env)
	if err != nil {
		return nil, fmt.Errorf("builtin: construct %q: %w", d.ID, err)
	}
	inst := &instance{
		id:       d.ID,
		plugin:   p,
		lc:       lc,
		state:    loader.StateReady,
		caller:   safecall.NewCaller(d.ID, l.callerOpt, nil),
		version:  d.Version,
		loadedAt: time.Now(),
	}
	inst.qa.Store(quota.NewSimple(l.quotaLim))
	l.instances[d.ID] = inst
	return inst, nil
}

// Unload 内置后端不支持卸载（见 ADR-001）。
func (l *Loader) Unload(ctx context.Context, id string) error {
	return loader.ErrNotSupported
}

// Watch 内置后端不支持产物热更，返回 ErrNotSupported。
//
// 关键决策（约束 G1：诚实标注限制，禁止半成品接口）：
// builtin 插件是**编译期链接**进宿主的，宿主进程里不存在"插件产物文件"，
// 因此根本不存在可监听的对象。返回永不触发的空通道会让调用方以为
// "我在监听，只是暂时没变化"，从而把热更逻辑配错了地方——
// 这正是自己第 8 条交接经验说的"看起来能用、实际裸奔"。
//
// 与 Unload 同样返回 ErrNotSupported：builtin 不支持热更，语义一致。
func (l *Loader) Watch(_ context.Context) (<-chan loader.ReloadEvent, error) {
	return nil, fmt.Errorf("%w: builtin plugins are linked at build time, there is no artifact to watch", loader.ErrNotSupported)
}

// instance 内置插件实例。
type instance struct {
	id     string
	plugin Plugin
	lc     lifecycle.Lifecycle
	state  loader.State
	caller *safecall.Caller
	// version/loadedAt 供 /plugins 端点展示（约束 E2）。
	version  string
	loadedAt time.Time
	// qa 是该实例的运行时配额（约束 C1）。
	qa atomic.Pointer[quota.Quota]
}

func (i *instance) ID() string          { return i.id }
func (i *instance) State() loader.State { return i.state }

// Invoke 通过类型安全 Invoker 调用，并被 safecall 边界包裹。
func (i *instance) Invoke(ctx context.Context, method string, args any) (any, error) {
	// 约束 C1：与 proc 后端保持同一顺序——先过配额（廉价快速失败），
	// 再进 safecall 边界。两个后端行为必须一致，否则配额形同虚设。
	if q := i.qa.Load(); q != nil && !q.Allow(time.Now()) {
		return nil, fmt.Errorf("builtin: invoke %q %w", i.id, quota.ErrQuotaExceeded)
	}

	var out any
	err := i.caller.Invoke(ctx, 0, func(cctx context.Context) error {
		v, err := i.plugin.Invoker(i.lc, method, args)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// Close 关闭实例（调用插件 Stop）。
func (i *instance) Close(ctx context.Context) error {
	if i.state == loader.StateStopped {
		return nil
	}
	err := i.lc.Stop(ctx)
	i.state = loader.StateStopped
	return err
}

// Lifecycle 暴露底层生命周期（供宿主编排 Init/Start）。
func (i *instance) Lifecycle() lifecycle.Lifecycle { return i.lc }

// Inspect 返回插件的可观测详情（约束 E2）。
//
// builtin 无重启/子进程语义，Restarts 与 ExitCode 恒为 0；
// 也永远不支持产物监听，WatchTracked 恒为 false。
func (l *Loader) Inspect(_ context.Context, id string) (loader.Info, error) {
	l.mu.RLock()
	inst, ok := l.instances[id]
	l.mu.RUnlock()
	if !ok {
		return loader.Info{}, fmt.Errorf("%w: builtin %q", loader.ErrNotLoaded, id)
	}
	info := loader.Info{
		State:    inst.State(),
		Version:  inst.version,
		LoadedAt: inst.loadedAt,
	}
	if q := inst.qa.Load(); q != nil {
		st := q.Stats()
		info.Quota = loader.QuotaStats{
			Allowed:        st.Allowed,
			RejectedQPS:    st.RejectedQPS,
			RejectedBudget: st.RejectedBudget,
		}
	}
	return info, nil
}
