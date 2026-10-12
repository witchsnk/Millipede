// Package proc 宿主侧（父进程）Loader：拉起子进程、握手、完整性校验、
// 健康检查、心跳、自动重启、熔断退避与优雅关闭（约束 C4/D1）。
//
// 本包落地 docs/04 中的坑：
//   - 就绪竞态：指数退避 + 超时等待握手行。
//   - 僵尸进程/fd：CommandContext + Wait + Setpgid + 整组 Kill。
//   - crash loop：稳定期重置重启预算 + 指数退避 + 超限熔断放弃。
//   - stdout 污染：只认 MagicPrefix 行，其余视为日志忽略。
//   - 产物篡改/伪造：加载前强制 sha256 + HMAC 签名校验（D1）。
package proc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/rpc"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/millipede/millipede/kernel/integrity"
	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/loader/proc/protocol"
	"github.com/millipede/millipede/kernel/quota"
	"github.com/millipede/millipede/kernel/safecall"
)

// Options proc 后端参数。
type Options struct {
	HandshakeTimeout time.Duration // 握手等待超时，默认 5s
	InvokeTimeout    time.Duration // 单次 RPC 超时，默认 5s
	MaxRestarts      int           // 稳定期内允许的最大连续重启次数，默认 3
	RestartBackoff   time.Duration // 重启退避基数，默认 1s（指数增长）
	StabilityWindow  time.Duration // 稳定期：进程连续存活超过该时长则重置重启预算
	HealthInterval   time.Duration // 心跳间隔，默认 2s
	Caller           safecall.Options
	// Verify 定义插件产物完整性校验策略（约束 D1）。
	// 默认 Lenient（仅校验和）。生产环境应使用 integrity.Strict(key)。
	Verify integrity.Policy
	// Env 注入到插件子进程的环境变量。留空表示继承宿主环境。
	// 注意：插件配置应优先走 sdk/host 注入（约束 E3），
	// Env 仅用于进程级必需参数或测试场景切换。
	Env []string
	// Quota 每插件资源配额（约束 C1）。零值使用有界默认值（QPS/次数预算），
	// 不会"无限放行"。
	Quota quota.Limits
	// WatchInterval 产物变更轮询间隔。默认 5s。设为极大值可关闭产物监听
	// （关闭后 Watch 通道不再产生事件，但调用仍安全）。
	WatchInterval time.Duration
	// OnReloadEvent 产物变更回调（与 Watch 通道双路投递）。
	OnReloadEvent func(loader.ReloadEvent)
	// OnWatchError 监听出错回调。
	OnWatchError func(error)
	// OnWatchLog 监听日志回调，签名为 (pluginID, msg)。
	OnWatchLog func(pluginID, msg string)
}

func (o *Options) defaults() {
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 5 * time.Second
	}
	if o.InvokeTimeout <= 0 {
		o.InvokeTimeout = 5 * time.Second
	}
	if o.MaxRestarts <= 0 {
		o.MaxRestarts = 3
	}
	if o.RestartBackoff <= 0 {
		o.RestartBackoff = time.Second
	}
	if o.StabilityWindow <= 0 {
		o.StabilityWindow = time.Minute
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 2 * time.Second
	}
	// 零值 Policy 表示调用方未显式配置，按最安全的"仅校验和"处理。
	// 注意：这是默认值而非关闭校验；彻底跳过校验须显式传 integrity.Off()。
	if !o.Verify.Disabled && !o.Verify.RequireChecksum && !o.Verify.RequireSignature {
		o.Verify = integrity.Lenient()
	}
}

// Loader proc 后端。
type Loader struct {
	opt Options
	mu  sync.RWMutex
	ins map[string]*procInstance
	// quotas 是每插件的运行时配额（QPS + 调用次数预算，约束 C1）。
	// 与 ins 同生命周期，Unload 时释放。
	quotas map[string]*quota.Quota
	// watcher 监听插件产物变更（热更检测）。生命周期与 Loader 一致，
	// 不随单次 Watch 调用创建或销毁。
	watcher *Watcher
}

// New 构造 proc 后端。
func New(opt Options) *Loader {
	opt.defaults()
	l := &Loader{
		opt:    opt,
		ins:    make(map[string]*procInstance),
		quotas: make(map[string]*quota.Quota),
	}
	l.watcher = NewWatcher(WatchOptions{
		Interval: opt.WatchInterval,
		Verify:   opt.Verify,
		OnEvent: func(ev loader.ReloadEvent) {
			if opt.OnReloadEvent != nil {
				opt.OnReloadEvent(ev)
			}
		},
		OnError: func(err error) {
			if opt.OnWatchError != nil {
				opt.OnWatchError(err)
			}
		},
		Logger: func(pluginID, msg string) {
			if opt.OnWatchLog != nil {
				opt.OnWatchLog(pluginID, msg)
			}
		},
	})
	return l
}

// WatchEnabled 报告产物监听是否开启。
//
// 关闭时（WatchInterval 未配置）本后端的 Watch 不产生事件，
// 宿主可据此决定是否订阅，省掉一个无用的 goroutine。
func (o Options) WatchEnabled() bool { return o.WatchInterval > 0 }

// Watcher 暴露产物监听器，便于宿主读取跟踪状态与最近错误（约束 E1）。
func (l *Loader) Watcher() *Watcher { return l.watcher }

// QuotaFor 返回某插件当前的配额对象，用于观测（约束 E1）。
func (l *Loader) QuotaFor(id string) *quota.Quota {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.quotas[id]
}

// Kind 实现 loader.Loader。
func (l *Loader) Kind() loader.BackendKind { return loader.BackendProc }

// Load 校验产物、拉起子进程并完成握手与健康检查。
func (l *Loader) Load(ctx context.Context, d loader.Descriptor) (loader.Instance, error) {
	l.mu.Lock()
	if _, ok := l.ins[d.ID]; ok {
		l.mu.Unlock()
		return nil, fmt.Errorf("proc: plugin %q already loaded", d.ID)
	}
	l.mu.Unlock()

	inst, err := l.loadNew(ctx, d.ID, d)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.ins[d.ID] = inst
	l.mu.Unlock()
	return inst, nil
}

// loadNew 校验、拉起并注册一个插件实例（Load 与 Reap 共用）。
//
// 调用方负责保证注册表里没有同名实例。
func (l *Loader) loadNew(ctx context.Context, id string, d loader.Descriptor) (*procInstance, error) {
	d.ID = id
	// D1：完整性校验必须在任何进程创建之前完成，
	// 否则恶意二进制已经躺在机器上了。
	if err := integrity.Verify(d.URI, d.Checksum, d.Signature, l.opt.Verify); err != nil {
		return nil, fmt.Errorf("proc: refuse to load %q: %w", d.ID, err)
	}

	inst, err := newInstance(l.opt, d)
	if err != nil {
		return nil, err
	}
	if err := inst.start(ctx, d); err != nil {
		return nil, err
	}
	q := quota.NewSimple(l.opt.Quota)
	inst.setQuota(q)
	inst.startSupervisor()

	l.mu.Lock()
	l.quotas[d.ID] = q
	w := l.watcher
	l.mu.Unlock()

	// 纳入产物监听：首轮只记录基线摘要，不产生事件。
	if w != nil {
		if err := w.Track(d.ID, d.URI); err != nil {
			// 监听失败不影响加载本身，但要让调用方知道热更对该插件不可用。
			inst.setLastError(fmt.Errorf("proc: watch %q: %w", d.ID, err))
		}
	}
	return inst, nil
}

// Unload 停止监督并终止子进程（proc 支持卸载）。
func (l *Loader) Unload(ctx context.Context, id string) error {
	l.mu.Lock()
	inst, ok := l.ins[id]
	if ok {
		delete(l.ins, id)
	}
	q, hasQ := l.quotas[id]
	if hasQ {
		delete(l.quotas, id)
	}
	l.mu.Unlock()
	// 配额对象本身无后台 goroutine，但显式释放，保证 Close 后不再放行。
	if hasQ {
		q.Close()
	}
	if !ok {
		return fmt.Errorf("proc: plugin %q not loaded", id)
	}
	inst.stopSupervisor()
	return inst.kill()
}

// Watch 返回插件产物变更事件通道。
//
// 每个成功加载的插件都会被自动纳入监听（见 Load），
// 因此本后端的 Watch 是**真实生效**的产物热更检测，不是占位实现。
//
// 事件语义：产物内容（sha256）发生变化且通过完整性校验时上报
// ReloadEvent{Reason: "artifact-changed"}。本方法只负责"发现变更"，
// 实际替换（Load 新 → 切流 → Unload 旧）由编排层执行，见 Reap 热更方法。
func (l *Loader) Watch(ctx context.Context) (<-chan loader.ReloadEvent, error) {
	l.mu.Lock()
	w := l.watcher
	l.mu.Unlock()
	if w == nil {
		// 尚未创建 Watcher（例如所有加载都失败）。返回空通道而非报错，
		// 保持与其他后端一致的"Watch 总是可用"语义。
		ch := make(chan loader.ReloadEvent)
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}
	out := make(chan loader.ReloadEvent, watchChanBuffer)
	// 每个 Watch 调用启动一个 Run；由 ctx 结束即退出，不残留 goroutine（约束 B3）。
	// 使用 WithoutCancel 断开父 ctx 的 cancel 传播是**错误**的：
	// Watcher 生命周期应由 Loader 持有，而不是由单次 Watch 调用持有。
	go w.Run(ctx, out)
	return out, nil
}

// watchChanBuffer 是 Watch 事件通道的缓冲。
// 给一定缓冲，避免瞬时多个插件同时变更时丢事件；
// 真正的背压保护在 Watcher.deliver（带超时，不阻塞监听循环）。
const watchChanBuffer = 64

// Reap 执行受控热更：加载新代 → 切流 → 卸载旧代。
//
// 语义（对应 docs/05 M4）：
//   - 若新代加载或校验失败，**保留旧代继续服务**，绝不中途"没有插件可用"。
//   - 只有新代 Ready 之后才卸载旧代，实现无间隙切换。
//
// reason 用于日志与审计，例如 "artifact-changed"。
func (l *Loader) Reap(ctx context.Context, id string, next loader.Descriptor, reason string) error {
	// 关键坑：不能直接调 Load(next)，因为 Load 拒绝"ID 已存在"的插件，
	// 而热更场景下旧代正是同一个 ID。必须显式腾出这个 ID。
	l.mu.Lock()
	old := l.ins[id]
	delete(l.ins, id)
	l.mu.Unlock()

	// 旧代继续服务，直到新代真正 Ready —— 这是"无间隙切换"的关键。
	if _, err := l.loadNew(ctx, id, next); err != nil {
		// 新代失败：把旧代放回注册表，保证插件始终可用。
		if old != nil {
			l.mu.Lock()
			l.ins[id] = old
			l.mu.Unlock()
		}
		return fmt.Errorf("proc: hot reload %q (%s): %w; old generation kept alive", id, reason, err)
	}

	// 新代就绪，卸载旧代。
	if old != nil {
		old.stopSupervisor()
		if err := old.kill(); err != nil {
			// 旧代清理失败不影响新代可用性，如实报告即可。
			return fmt.Errorf("proc: hot reload %q: new generation ready, old unload failed: %w", id, err)
		}
	}

	// 重新登记监听基线，避免把"刚切换完"误判为一次新变更。
	l.mu.RLock()
	w := l.watcher
	l.mu.RUnlock()
	if w != nil {
		w.Untrack(id)
		if err := w.Track(id, next.URI); err != nil {
			return fmt.Errorf("proc: hot reload %q: tracking new artifact failed: %w", id, err)
		}
	}
	return nil
}

// startProcess 已被 procInstance.spawn 取代（后者同时负责退出码回收）。
// 本文件仅保留 Loader 编排、完整性校验与协议工具函数。

// runningProc 是一代进程的运行态。
type runningProc struct {
	cmd    *exec.Cmd
	client *rpc.Client
	cookie string
	// startedAt 该代进程的启动时刻，用于稳定期判定。
	startedAt time.Time
	// exitCode 该代进程退出码（由 wait 协程填充）。
	exitCode atomic.Int64
	// exited 标记该代进程是否已退出。
	exited atomic.Bool
	// cancel 结束该代进程绑定的 context。
	//
	// 关键坑：exec.CommandContext 会在其 context 结束时向进程发 SIGKILL。
	// 因此绝不能把"握手用的超时 context"传给 CommandContext，
	// 否则 spawn 一返回、调用方 defer cancel() 一执行，新进程立刻被杀，
	// 表现为"重启永远成功但新进程立刻消失、退出码 -1"。
	// 进程生命周期必须由该字段独立管理，只在 closeGeneration 时取消。
	cancel context.CancelFunc
}

// closeGeneration 关闭一代进程的连接与进程（不标记实例为停止）。
func (r *runningProc) closeGeneration() {
	if r == nil {
		return
	}
	// 先取消进程 context（触发 CommandContext 的 SIGKILL），再回收连接。
	if r.cancel != nil {
		r.cancel()
	}
	if r.client != nil {
		_ = r.client.Close()
	}
	if r.cmd != nil {
		_ = killGroup(r.cmd)
	}
}

// waitHandshake 从 stdout 读取握手行（忽略非握手行）。
func waitHandshake(ctx context.Context, stdout io.Reader) (protocol.Handshake, error) {
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return protocol.Handshake{}, ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return protocol.Handshake{}, errors.New("proc: stdout closed before handshake")
			}
			if hs, ok := protocol.Decode(line); ok {
				return hs, nil
			}
			// 非握手行：视为日志，忽略（stdout 污染防护）。
		}
	}
}

// dialWithRetry 指数退避拨号，直到成功或超时。
func dialWithRetry(ctx context.Context, port int, maxWait time.Duration) (*rpc.Client, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	backoff := 20 * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for {
		if time.Now().After(deadline) {
			return nil, errors.New("proc: dial timeout")
		}
		c, err := rpc.Dial("tcp", addr)
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// killGroup 终止整个进程组，避免子进程再生子进程残留（约束 C4）。
func killGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return cmd.Process.Kill()
}

// Inspect 返回插件的可观测详情（约束 E2）。
//
// 约定：后端实现该接口是可选项。observability 层通过接口断言探测，
// 未实现时把它当作"无详情"而不是报错，保证老后端不会被破坏（约束 A2）。
func (l *Loader) Inspect(_ context.Context, id string) (loader.Info, error) {
	l.mu.RLock()
	inst, ok := l.ins[id]
	w := l.watcher
	l.mu.RUnlock()
	if !ok {
		return loader.Info{}, fmt.Errorf("%w: proc %q", loader.ErrNotLoaded, id)
	}

	info := loader.Info{
		State:    inst.State(),
		Version:  inst.Version(),
		LoadedAt: inst.LoadedAt(),
		Restarts: int64(inst.Restarts()),
		ExitCode: inst.ExitCode(),
	}
	if err := inst.LastError(); err != nil {
		info.LastError = err
	}
	if q := l.quotas[id]; q != nil {
		st := q.Stats()
		info.Quota = loader.QuotaStats{
			Allowed:        st.Allowed,
			RejectedQPS:    st.RejectedQPS,
			RejectedBudget: st.RejectedBudget,
		}
	}
	if w != nil && w.Tracking(id) {
		info.WatchTracked = true
	}
	return info, nil
}

// CallerFor 返回某插件的调用器，供可观测层采集指标（约束 E1/E2）。
// 未加载时返回 nil，调用方需判空。
func (l *Loader) CallerFor(id string) *safecall.Caller {
	l.mu.RLock()
	inst, ok := l.ins[id]
	l.mu.RUnlock()
	if !ok {
		return nil
	}
	return inst.Caller()
}
