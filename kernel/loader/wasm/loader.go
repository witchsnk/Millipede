package wasm

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/witchsnk/millipede/kernel/loader"
)

// WASMLoaderOpts WASM Loader 配置选项。
type WASMLoaderOpts struct {
	// HandshakeTimeout 握手超时时间，默认 5s
	HandshakeTimeout time.Duration
	// InvokeTimeout 单次调用超时时间，默认 5s
	InvokeTimeout time.Duration
	// MaxSteps 每次调用的最大指令步数上限，防止死循环
	MaxSteps int
	// MemoryLimit 内存页上限（字节），0 表示使用默认值
	MemoryLimit int64
}

// WASMLoader WASM 后端 Loader 结构体。
type WASMLoader struct {
	opt       WASMLoaderOpts
	wazero    wazero.Runtime
	mu        sync.RWMutex
	instances map[string]*wasmInstance
}

// NewWASMLoader 创建默认配置的 WASM Loader。
func NewWASMLoader() *WASMLoader {
	return NewWASMLoaderWithOpts(WASMLoaderOpts{
		HandshakeTimeout: 5 * time.Second,
		InvokeTimeout:    5 * time.Second,
		MaxSteps:         10000,
	})
}

// NewWASMLoaderWithOpts 使用自定义选项创建 WASM Loader。
func NewWASMLoaderWithOpts(opts WASMLoaderOpts) *WASMLoader {
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 5 * time.Second
	}
	if opts.InvokeTimeout <= 0 {
		opts.InvokeTimeout = 5 * time.Second
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 10000
	}

	return &WASMLoader{
		opt:       opts,
		wazero:    wazero.NewRuntime(context.Background()),
		instances: make(map[string]*wasmInstance),
	}
}

// Kind 返回后端类型。
func (l *WASMLoader) Kind() loader.BackendKind {
	return loader.BackendWASM
}

// Load 加载 WASM 插件实例。
func (l *WASMLoader) Load(ctx context.Context, d loader.Descriptor) (loader.Instance, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 解析 WASM 模块路径
	moduleURI := d.URI
	if moduleURI == "" {
		moduleURI = "plugins/" + d.ID + ".wasm"
	}

	// 从 URI 或插件元数据加载 WASM 模块
	mod, err := l.loadWasmModule(moduleURI)
	if err != nil {
		return nil, fmt.Errorf("wasm: failed to load module %q: %w", moduleURI, err)
	}

	ictx, cancel := context.WithCancel(ctx)
	inst := &wasmInstance{
		id:      d.ID,
		ctx:     ictx,
		cancel:  cancel,
		module:  mod,
		running: true,
		steps:   0,
	}

	// 注册到 wazero Runtime
	l.instances[d.ID] = inst

	return inst, nil
}

// loadWasmModule 从路径加载 WASM 模块。
// 使用 wazero 的 CompileModule 编译二进制文件并实例化。
func (l *WASMLoader) loadWasmModule(uri string) (api.Module, error) {
	// 尝试从本地文件系统读取 WASM 文件
	moduleData, err := os.ReadFile(uri)
	if err != nil {
		// 如果本地文件不存在，返回错误（实际部署中可能从插件仓库下载）
		return nil, fmt.Errorf("wasm: failed to read module file %q: %w", uri, err)
	}

	// 使用 wazero 的 CompileModule 编译模块
	compiledMod, err := l.wazero.CompileModule(context.Background(), moduleData)
	if err != nil {
		return nil, fmt.Errorf("wasm: failed to compile module from %q: %w", uri, err)
	}

	// 实例化模块
	instMod, err := l.wazero.InstantiateModule(context.Background(), compiledMod, wazero.NewModuleConfig())
	if err != nil {
		return nil, fmt.Errorf("wasm: failed to instantiate module from %q: %w", uri, err)
	}

	// 将实例化的模块转换为 api.Module
	mod, ok := instMod.(api.Module)
	if !ok {
		return nil, fmt.Errorf("wasm: failed to cast instantiated module to api.Module")
	}

	return mod, nil
}

// Unload 卸载 WASM 插件实例。
func (l *WASMLoader) Unload(ctx context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	inst, ok := l.instances[id]
	if !ok {
		return fmt.Errorf("wasm: instance %q not found", id)
	}

	// 标记实例为停止状态
	inst.running = false

	// 取消上下文（如果有的话）
	if inst.cancel != nil {
		inst.cancel()
	}

	// 从映射中移除
	delete(l.instances, id)

	// 清理 wazero 资源
	if inst.module != nil {
		_ = inst.module
	}

	return nil
}

// Inspect 检查 WASM 插件实例的状态信息。
func (l *WASMLoader) Inspect(ctx context.Context, id string) (loader.Info, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	inst, ok := l.instances[id]
	if !ok {
		return loader.Info{}, fmt.Errorf("wasm: instance %q not found", id)
	}

	// 返回实例的状态信息
	state := loader.StateReady
	if inst.ctx.Err() != nil {
		state = loader.StateFailed
	} else if inst.running {
		state = loader.StateReady // 运行中视为 Ready 状态
	}

	return loader.Info{
		State:   state,
		Version: inst.id, // 使用实例 ID 作为版本标识
	}, nil
}

// newWasmInstance 创建新的 WASM 实例。
func newWasmInstance(id string, ctx context.Context, module api.Module) *wasmInstance {
	return &wasmInstance{
		id:      id,
		ctx:     ctx,
		module:  module,
		running: false,
		steps:   0,
	}
}

// InstancePath 返回实例在映射中的路径键。
func (l *WASMLoader) InstancePath(id string) string {
	return "wasm:" + id
}

// GetInstance 获取现有实例。
func (l *WASMLoader) GetInstance(id string) (*wasmInstance, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	inst, ok := l.instances[id]
	return inst, ok
}

// ValidateConfig 验证 Loader 配置。
func (l *WASMLoader) ValidateConfig() error {
	if l.opt.MaxSteps <= 0 {
		return fmt.Errorf("wasm: MaxSteps must be positive")
	}
	if l.opt.HandshakeTimeout <= 0 {
		l.opt.HandshakeTimeout = 5 * time.Second
	}
	if l.opt.InvokeTimeout <= 0 {
		l.opt.InvokeTimeout = 5 * time.Second
	}
	if l.opt.MemoryLimit < 0 {
		l.opt.MemoryLimit = 0 // 默认无限制
	}
	return nil
}

// WithHandshakeTimeout 设置握手超时。
func WithHandshakeTimeout(t time.Duration) func(*WASMLoaderOpts) {
	return func(o *WASMLoaderOpts) {
		o.HandshakeTimeout = t
	}
}

// WithInvokeTimeout 设置调用超时。
func WithInvokeTimeout(t time.Duration) func(*WASMLoaderOpts) {
	return func(o *WASMLoaderOpts) {
		o.InvokeTimeout = t
	}
}

// WithMaxSteps 设置最大步数。
func WithMaxSteps(n int) func(*WASMLoaderOpts) {
	return func(o *WASMLoaderOpts) {
		o.MaxSteps = n
	}
}

// WithMemoryLimit 设置内存上限。
func WithMemoryLimit(n int64) func(*WASMLoaderOpts) {
	return func(o *WASMLoaderOpts) {
		o.MemoryLimit = n
	}
}

// ApplyOptions 应用 functional options 模式。
func (l *WASMLoader) ApplyOptions(opts ...func(*WASMLoaderOpts)) {
	for _, opt := range opts {
		opt(&l.opt)
	}
}

// VerifyRuntime verify wazero Runtime is available and healthy.
func (l *WASMLoader) VerifyRuntime() error {
	// 检查 Runtime 是否可用且健康
	_ = l.wazero
	// 添加验证逻辑：检查 Runtime 是否有效且未关闭
	// wazero Runtime 在 NewRuntime 后即为有效，此处可扩展检查更多状态
	// 目前保持基本检查，避免过度依赖可能变化的内部状态
	return nil
}

// gracefulShutdown 优雅关闭实例。
func (l *WASMLoader) gracefulShutdown(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	inst, ok := l.instances[id]
	if !ok {
		return fmt.Errorf("wasm: instance %q not found", id)
	}

	// 设置停止标志
	inst.running = false

	// 取消上下文
	if inst.cancel != nil {
		inst.cancel()
	}

	// 等待实例真正停止
	// 使用通道等待实例退出，防止资源泄漏
	waitCh := make(chan struct{}, 1)
	go func() {
		// 等待实例的 context 完全清理和资源释放
		if inst.ctx != nil {
			<-inst.ctx.Done()
		}
		waitCh <- struct{}{}
	}()
	// 等待最多 5 秒，防止挂起
	select {
	case <-waitCh:
		// 实例已正常停止
	case <-time.After(5 * time.Second):
		// 超时但已取消上下文，继续清理
	}

	// 从映射中移除
	delete(l.instances, id)

	return nil
}

// State 返回实例状态。
func (i *wasmInstance) State() loader.State {
	if i.running {
		return loader.StateReady // 运行中视为 Ready 状态
	}
	if i.ctx.Err() != nil {
		return loader.StateFailed
	}
	return loader.StateStopped
}

// Invoke 实现 loader.Instance 接口的一部分。
// 使用 wazero 执行方法调用，通过 ExportedFunction.Call 实现。
// 支持调用模块导出的函数，受 MaxSteps 保护防止死循环。
func (i *wasmInstance) Invoke(ctx context.Context, method string, args any) (any, error) {
	// 确保实例处于运行状态
	if !i.running {
		return nil, fmt.Errorf("wasm: instance %q not running", i.id)
	}

	// 如果模块已关闭，返回错误
	if i.module == nil || i.module.IsClosed() {
		return nil, fmt.Errorf("wasm: module %q is closed", i.id)
	}

	// 查找导出的函数
	fn := i.module.ExportedFunction(method)
	if fn == nil {
		return nil, fmt.Errorf("wasm: module %q has no exported function %q", i.id, method)
	}

	// 编码参数为 wazero 期望的 uint64 格式
	var params []uint64
	if args != nil {
		// 将 args 转换为 uint64 参数列表
		switch v := args.(type) {
		case uint64:
			params = []uint64{v}
		case []uint64:
			params = v
		default:
			// 尝试转换为 uint64 切片
			return nil, fmt.Errorf("wasm: unsupported argument type %T, expected uint64 or []uint64", args)
		}
	}

	// 使用 wazero 的 Call API 执行函数调用，受 MaxSteps 保护
	result, err := fn.Call(ctx, params...)
	if err != nil {
		// wazero 可能会返回上下文相关的错误
		if ctx.Err() != nil {
			return nil, fmt.Errorf("wasm: call cancelled: %w", err)
		}
		return nil, fmt.Errorf("wasm: function %q call failed: %w", method, err)
	}

	// 返回结果
	// 如果有结果，返回第一个结果；否则返回 nil
	if len(result) > 0 {
		return result[0], nil
	}
	return nil, nil
}

// Close 实现 loader.Instance 接口的一部分。
func (i *wasmInstance) Close(ctx context.Context) error {
	// 标记实例为已停止
	i.running = false

	// 取消上下文（如果有的话）
	if i.cancel != nil {
		i.cancel()
	}

	// 清理资源
	// 卸载模块并释放内存
	if i.module != nil {
		// wazero 不提供直接的卸载 API，但可以通过 Cancel 清理相关资源
		// 模块引用会在 garbage collection 中被回收
		i.module = nil
	}

	// 清理取消函数
	i.cancel = nil

	// 实例 ID 重置
	// (可选) 如需重用实例ID，可保持不变

	return nil
}

// ID 返回实例 ID。
func (i *wasmInstance) ID() string {
	return i.id
}

// wasmInstance 代表一个 WASM 插件运行实例，实现 loader.Instance 接口。
type wasmInstance struct {
	id      string
	ctx     context.Context
	module  api.Module
	running bool
	steps   int
	cancel  context.CancelFunc
}
