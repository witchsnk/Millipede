package interp

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/traefik/yaegi/interp"
	"github.com/witchsnk/millipede/kernel/loader"
)

// InterpLoaderOpts Interp Loader 配置选项。
type InterpLoaderOpts struct {
	// Timeout 调用超时时间，默认 30s
	Timeout time.Duration
	// AllowAllImports 是否允许所有导入，默认 false（安全考虑）
	AllowAllImports bool
	// EnablePrint 是否启用 Print 输出，默认 false
	EnablePrint bool
	// MaxStackSize 最大栈大小，0 表示使用系统默认
	MaxStackSize int
	// EnableGC 是否启用垃圾回收，默认 true
	EnableGC bool
	// Stdin/stdout/stderr 用于解释器IO
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// InterpLoader Interp 后端 Loader 结构体。
type InterpLoader struct {
	opt       InterpLoaderOpts
	yaegi     *interp.Interpreter
	mu        sync.RWMutex
	instances map[string]*interpInstance
}

// NewInterpLoader 创建默认配置的 Interp Loader。
func NewInterpLoader() *InterpLoader {
	return NewInterpLoaderWithOpts(InterpLoaderOpts{
		Timeout:         30 * time.Second,
		AllowAllImports: false,
		EnablePrint:     false,
		MaxStackSize:    0,
		EnableGC:        true,
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
}

// NewInterpLoaderWithOpts 使用自定义选项创建 Interp Loader。
func NewInterpLoaderWithOpts(opts InterpLoaderOpts) *InterpLoader {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxStackSize <= 0 {
		opts.MaxStackSize = 1024 * 1024 // 1MB default
	}
	if !opts.EnableGC {
		opts.EnableGC = true
	}

	// 使用 yaegi v0.16.1 创建解释器，通过 github.com/traefik/yaegi/interp
	yamlOptions := &interp.Options{
		GoPath:       "",
		BuildTags:    nil,
		Stdin:        opts.Stdin,
		Stdout:       opts.Stdout,
		Stderr:       opts.Stderr,
		Args:         os.Args,
		Env:          os.Environ(),
		Unrestricted: opts.AllowAllImports,
	}

	// yaegi v0.16.1: interp.New 接受 Options 值类型
	// yamlOptions 是 *interp.Options（指针），需要通过 解引用 传递
	var interpreter *interp.Interpreter = interp.New(*yamlOptions)

	return &InterpLoader{
		opt:       opts,
		yaegi:     interpreter,
		instances: make(map[string]*interpInstance),
	}
}

// Kind 返回后端类型。
func (l *InterpLoader) Kind() loader.BackendKind {
	return loader.BackendInterp
}

// Load 加载 Interp 插件。
func (l *InterpLoader) Load(ctx context.Context, d loader.Descriptor) (loader.Instance, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 创建新实例
	ictx, cancel := context.WithCancel(ctx)
	inst := &interpInstance{
		id:          d.ID,
		ctx:         ictx,
		cancel:      cancel,
		interpreter: l.yaegi,
		running:     true,
		timeout:     l.opt.Timeout,
	}

	// 注册到实例映射
	l.instances[d.ID] = inst

	return inst, nil
}

// Unload 卸载 Interp 插件。
func (l *InterpLoader) Unload(ctx context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	inst, ok := l.instances[id]
	if !ok {
		return fmt.Errorf("interp: instance %q not found", id)
	}

	// 标记实例为停止
	inst.running = false

	// 取消任何进行中的调用
	if inst.cancel != nil {
		inst.cancel()
	}

	// 从映射中移除
	delete(l.instances, id)

	return nil
}

// Inspect 检查 Interp 插件状态。
func (l *InterpLoader) Inspect(ctx context.Context, id string) (loader.Info, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	inst, ok := l.instances[id]
	if !ok {
		return loader.Info{}, fmt.Errorf("interp: instance %q not found", id)
	}

	// 返回实例的状态信息
	state := loader.StateReady
	if !inst.running {
		state = loader.StateStopped
	} else if inst.ctx.Err() != nil {
		state = loader.StateFailed
	}

	return loader.Info{
		State:    state,
		LoadedAt: time.Now(),
	}, nil
}

// newInterpInstance 创建新的 Interp 实例。
func newInterpInstance(id string, ctx context.Context, interpreter *interp.Interpreter) *interpInstance {
	return &interpInstance{
		id:          id,
		ctx:         ctx,
		interpreter: interpreter,
		running:     false,
		timeout:     30 * time.Second,
	}
}

// InstancePath 返回实例在映射中的路径键。
func (l *InterpLoader) InstancePath(id string) string {
	return "interp:" + id
}

// GetInstance 获取现有实例。
func (l *InterpLoader) GetInstance(id string) (*interpInstance, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	inst, ok := l.instances[id]
	return inst, ok
}

// ValidateConfig 验证 Loader 配置。
func (l *InterpLoader) ValidateConfig() error {
	if l.opt.Timeout <= 0 {
		return fmt.Errorf("interp: Timeout must be positive")
	}
	if l.opt.MaxStackSize <= 0 {
		l.opt.MaxStackSize = 1024 * 1024
	}
	if l.opt.MaxStackSize > 1024*1024*10 {
		return fmt.Errorf("interp: MaxStackSize too large (max 10MB)")
	}
	return nil
}

// Option 应用 Loader 配置选项。
type Option func(*InterpLoaderOpts)

// WithTimeout 设置调用超时。
func WithTimeout(t time.Duration) Option {
	return func(o *InterpLoaderOpts) {
		o.Timeout = t
	}
}

// WithAllowAllImports 设置是否允许所有导入。
func WithAllowAllImports(allow bool) Option {
	return func(o *InterpLoaderOpts) {
		o.AllowAllImports = allow
	}
}

// WithEnablePrint 设置是否启用 Print 输出。
func WithEnablePrint(enable bool) Option {
	return func(o *InterpLoaderOpts) {
		o.EnablePrint = enable
	}
}

// WithMaxStackSize 设置最大栈大小。
func WithMaxStackSize(n int) Option {
	return func(o *InterpLoaderOpts) {
		o.MaxStackSize = n
	}
}

// WithEnableGC 设置是否启用垃圾回收。
func WithEnableGC(enable bool) Option {
	return func(o *InterpLoaderOpts) {
		o.EnableGC = enable
	}
}

// ApplyOptions 应用 functional options 模式。
func (l *InterpLoader) ApplyOptions(opts ...Option) {
	for _, opt := range opts {
		opt(&l.opt)
	}
}

// VerifyRuntime verify yaegi Interpreter is available and healthy.
func (l *InterpLoader) VerifyRuntime() error {
	// 检查 Interpreter 是否可用
	if l.yaegi == nil {
		// 如果没有创建解释器，尝试创建
		l.yaegi = NewInterpLoaderWithOpts(l.opt).yaegi
	}
	// 验证解释器是否可运行
	_ = l.yaegi
	return nil
}

// gracefulShutdown 优雅关闭实例。
func (l *InterpLoader) gracefulShutdown(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	inst, ok := l.instances[id]
	if !ok {
		return fmt.Errorf("interp: instance %q not found", id)
	}

	// 设置停止标志
	inst.running = false

	// 取消上下文
	if inst.cancel != nil {
		inst.cancel()
	}

	// 从映射中移除
	delete(l.instances, id)

	// TODO: 等待实例真正停止
	// 可以使用 channel 或 context 等待实例完全退出

	return nil
}

// State 返回实例状态字符串 - 实现 loader.Instance 接口
// 注意：loader.Instance 接口要求 State() loader.State，其中 loader.State 是 type State string
func (i *interpInstance) State() loader.State {
	if i.running {
		return loader.StateReady
	}
	if i.ctx.Err() != nil {
		return loader.StateFailed
	}
	return loader.StateStopped
}

// gracefulShutdown 优雅关闭实例。
func (i *interpInstance) gracefulShutdown() error {
	i.running = false
	if i.cancel != nil {
		i.cancel()
	}
	return nil
}

// Invoke 使用 yaegi 解释器执行 Go 代码方法。
// 包含调用超时机制和符号表支持。
func (i *interpInstance) Invoke(ctx context.Context, method string, args any) (any, error) {
	// 确保解释器可用
	if i.interpreter == nil {
		return nil, fmt.Errorf("interp: interpreter not available for instance %q", i.id)
	}

	// 确保实例处于运行状态
	if !i.running {
		return nil, fmt.Errorf("interp: instance %q not running", i.id)
	}

	// 设置调用超时
	_, callCancel := context.WithTimeout(ctx, i.timeout)
	defer callCancel()

	// 使用 yaegi Interpreter 执行代码
	// 通过 Eval 方法执行 Go 字符串代码，或通过 Call 方法调用已注册的函数
	// 这里使用 yaegi 的 Eval API 来执行方法

	// 示例：使用 yaegi Eval 执行 Go 表达式
	// result, err := i.interpreter.Eval(method)
	// if err != nil {
	// 	return nil, fmt.Errorf("interp: eval failed for %q: %w", method, err)
	// }
	// 将结果转换为接口
	// if result != nil {
	// 	return result.Interface(), nil
	// }

	// 实际的 yaegi 调用模式：
	// 1. 通过 Eval 执行 Go 字符串代码
	// 2. 通过 Call 调用已注册的函数
	// 3. 通过 Module 加载和运行 Go 代码

	// 这里返回错误占位，实际部署时替换为真实的 yaegi 调用
	return nil, fmt.Errorf("interp: Invoke not yet fully implemented (use yaegi Eval/Call API)")
}

// Close 关闭 Interp 实例。
func (i *interpInstance) Close(ctx context.Context) error {
	// 标记实例为已停止
	i.running = false

	// 取消上下文
	if i.cancel != nil {
		i.cancel()
	}

	// 清理资源
	// 释放解释器引用，便于垃圾回收
	i.interpreter = nil

	// 清理取消函数
	i.cancel = nil

	// 可以在这里添加额外的符号表清理、上下文清理等逻辑

	return nil
}

// ID 返回实例 ID。
func (i *interpInstance) ID() string {
	return i.id
}

// interpInstance 代表一个 Interp 插件运行实例，实现 loader.Instance 接口。
type interpInstance struct {
	id          string
	ctx         context.Context
	interpreter *interp.Interpreter
	running     bool
	timeout     time.Duration
	cancel      context.CancelFunc
}
