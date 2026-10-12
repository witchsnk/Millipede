package proc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
	"github.com/witchsnk/millipede/kernel/loader/proc/protocol"
	"github.com/witchsnk/millipede/kernel/quota"
	"github.com/witchsnk/millipede/kernel/safecall"
	"github.com/witchsnk/millipede/sdk/contract"
)

// procInstance 是一个子进程插件实例。
//
// 并发模型：
//   - cur 指向当前"代"（generation）的进程，用 mu 保护读写；
//   - supervisor 协程独占变更 cur（串行化重启），Invoke 只读快照；
//   - 每次重启生成新的 *runningProc 并 closeGeneration 旧代，
//     因此 Invoke 永远拿到自洽的一代连接，不会串代。
type procInstance struct {
	id     string
	uri    string
	opt    Options
	caller *safecall.Caller

	mu   sync.RWMutex
	cur  *runningProc
	desc loader.Descriptor

	state atomic.Value // loader.State

	svMu      sync.Mutex
	stopSv    chan struct{}
	svStopped atomic.Bool

	// stderr 保存该插件最近若干行 stderr（排障用，约束 E1）。
	stderr *stderrRing
	// qa 是该实例的运行时配额（约束 C1），由 Loader 在 Load 时注入。
	qa atomic.Pointer[quota.Quota]

	// loadedAt 是该实例的加载时刻，供 /plugins 端点展示 uptime（约束 E2）。
	loadedAt time.Time

	// restartMu 保护重启预算与稳定期判定。
	restartMu sync.Mutex
	restarts  int
	lastExit  int64
	lastErr   atomic.Value // error

	// svWG 只跟踪监督循环的生命周期。
	// 注意：绝不能把进程回收协程也塞进这里——cmd.Wait() 只有子进程退出才返回，
	// 而关闭顺序是"先停监督、再杀进程"，混合等待会在 kill 之前就永久阻塞。
	svWG sync.WaitGroup
}

func newInstance(opt Options, d loader.Descriptor) (*procInstance, error) {
	return &procInstance{
		id:     d.ID,
		uri:    d.URI,
		opt:    opt,
		desc:   d,
		caller: safecall.NewCaller(d.ID, opt.Caller, nil),
		stderr: stderrRingFor(d.ID),
	}, nil
}

func (i *procInstance) ID() string { return i.id }

func (i *procInstance) State() loader.State {
	if s, ok := i.state.Load().(loader.State); ok {
		return s
	}
	return loader.StateFailed
}

// start 启动第一代进程。
func (i *procInstance) start(ctx context.Context, d loader.Descriptor) error {
	rp, err := i.spawn(ctx, d)
	if err != nil {
		i.state.Store(loader.StateFailed)
		return err
	}
	i.mu.Lock()
	i.cur = rp
	i.desc = d
	i.loadedAt = time.Now()
	i.mu.Unlock()
	i.state.Store(loader.StateReady)
	return nil
}

// spawn 拉起一代进程并后台回收其退出码（防僵尸）。
//
// ctx 仅用于"等待握手"这一有限阶段；**不绑定**到子进程生命周期上。
// 关键坑：若把带 defer cancel() 的超时 ctx 传给 exec.CommandContext，
// spawn 一返回、子进程就会被 SIGKILL，表现为"重启总是成功但新进程秒退、退出码 -1"。
func (i *procInstance) spawn(ctx context.Context, d loader.Descriptor) (*runningProc, error) {
	// 进程生命周期独立于握手 ctx：只有 closeGeneration 才终止它。
	procCtx, procCancel := context.WithCancel(context.WithoutCancel(ctx))

	cmd := exec.CommandContext(procCtx, d.URI)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = i.childEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("proc: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("proc: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		procCancel()
		return nil, fmt.Errorf("proc: start %q: %w", d.URI, err)
	}
	// 插件日志走 stderr（stdout 专用于握手）。
	// 必须保留最后若干行：否则"新进程为什么立刻退出"这类问题在生产上无法排查（约束 E1）。
	go captureStderr(i.id, stderr)

	hctx, cancel := context.WithTimeout(ctx, i.opt.HandshakeTimeout)
	defer cancel()
	hs, err := waitHandshake(hctx, stdout)
	if err != nil {
		procCancel()
		_ = killGroup(cmd)
		_ = cmd.Wait()
		return nil, fmt.Errorf("proc: handshake %q: %w", d.ID, err)
	}
	client, err := dialWithRetry(hctx, hs.Port, i.opt.HandshakeTimeout)
	if err != nil {
		procCancel()
		_ = killGroup(cmd)
		_ = cmd.Wait()
		return nil, fmt.Errorf("proc: dial %q: %w", d.ID, err)
	}

	rp := &runningProc{
		cmd:       cmd,
		client:    client,
		cookie:    hs.Cookie,
		startedAt: time.Now(),
		cancel:    procCancel,
	}
	// 后台回收：记录退出码并标记退出，防止僵尸进程。
	// 该协程不纳入 svWG——它的生命周期绑定在单个进程代上。
	go func() {
		err := cmd.Wait()
		rp.exited.Store(true)
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				rp.exitCode.Store(int64(ee.ExitCode()))
			} else {
				rp.exitCode.Store(-1)
			}
		} else {
			rp.exitCode.Store(0)
		}
	}()
	return rp, nil
}

// snapshot 读取当前代的连接信息（自洽）。
func (i *procInstance) snapshot() (*rpc.Client, string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.cur == nil || i.cur.exited.Load() || i.cur.client == nil {
		return nil, "", false
	}
	return i.cur.client, i.cur.cookie, true
}

// Invoke 通过 RPC 调用插件方法，整体被 safecall 边界包裹。
func (i *procInstance) Invoke(ctx context.Context, method string, args any) (any, error) {
	// 约束 C1：先过配额（QPS + 调用次数预算），再进 safecall 边界。
	// 顺序很重要：配额是"廉价快速失败"，必须在超时计时器与 goroutine 分发之前，
	// 否则被限流的调用仍然会付出一次完整调用的开销，超时反而保护不了宿主。
	if q := i.quota(); q != nil && !q.Allow(time.Now()) {
		return nil, fmt.Errorf("proc: invoke %q %w", i.id, quota.ErrQuotaExceeded)
	}

	var out any
	err := i.caller.Invoke(ctx, i.opt.InvokeTimeout, func(cctx context.Context) error {
		client, cookie, ok := i.snapshot()
		if !ok {
			return errors.New("proc: plugin not connected")
		}
		var argBytes []byte
		switch b := args.(type) {
		case []byte:
			argBytes = b
		case nil:
		default:
			return fmt.Errorf("proc: args must be []byte, got %T", args)
		}
		req := protocol.InvokeRequest{Cookie: cookie, Method: method, Args: argBytes}
		var resp protocol.InvokeResponse
		call := client.Go("RPCService.Invoke", req, &resp, nil)
		select {
		case <-cctx.Done():
			return cctx.Err()
		case <-call.Done:
			if call.Error != nil {
				return fmt.Errorf("proc: rpc: %w", call.Error)
			}
		}
		if !resp.OK {
			return fmt.Errorf("proc: plugin error: %s", resp.Error)
		}
		out = resp.Data
		return nil
	})
	return out, err
}

// Close 优雅关闭：停止监督，终止当前进程，回收所有代。
func (i *procInstance) Close(ctx context.Context) error {
	i.stopSupervisor()
	return i.kill()
}

// kill 终止当前进程并标记停止，幂等。
func (i *procInstance) kill() error {
	if i.State() == loader.StateStopped {
		return nil
	}
	i.mu.Lock()
	rp := i.cur
	i.cur = nil
	i.mu.Unlock()
	if rp != nil {
		rp.closeGeneration()
	}
	i.state.Store(loader.StateStopped)
	return nil
}

// startSupervisor 启动监督循环（唯一写 cur 的地方）。
func (i *procInstance) startSupervisor() {
	i.svMu.Lock()
	i.stopSv = make(chan struct{})
	stop := i.stopSv
	i.svMu.Unlock()

	i.svWG.Add(1)
	go func() {
		defer i.svWG.Done()
		ticker := time.NewTicker(i.opt.HealthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				i.checkAndRestart()
			}
		}
	}()
}

// checkAndRestart 检查当前代健康度，必要时真正拉起新进程。
func (i *procInstance) checkAndRestart() {
	if i.svStopped.Load() || i.State() == loader.StateStopped {
		return
	}

	i.restartMu.Lock()
	// 稳定期判定：当前代存活超过 StabilityWindow => 重置重启预算。
	if i.cur != nil && !i.cur.exited.Load() && time.Since(i.cur.startedAt) >= i.opt.StabilityWindow {
		i.restarts = 0
	}
	healthy := i.ping()
	i.restartMu.Unlock()

	if healthy {
		return
	}
	i.restart()
}

// ping 心跳检测当前代。
func (i *procInstance) ping() bool {
	i.mu.RLock()
	rp := i.cur
	i.mu.RUnlock()
	if rp == nil || rp.exited.Load() || rp.client == nil {
		return false
	}
	var resp protocol.PingResponse
	req := protocol.PingRequest{Cookie: rp.cookie}
	call := rp.client.Go("RPCService.Ping", req, &resp, nil)
	select {
	case <-time.After(i.opt.InvokeTimeout):
		return false
	case <-call.Done:
		return call.Error == nil && resp.Alive
	}
}

// restart 在退避后拉起新进程；超过预算则熔断放弃。
func (i *procInstance) restart() {
	i.restartMu.Lock()
	i.restarts++
	attempt := i.restarts
	budgetExceeded := attempt > i.opt.MaxRestarts
	var oldCode int64
	if i.cur != nil {
		oldCode = i.cur.exitCode.Load()
	}
	i.restartMu.Unlock()

	i.state.Store(loader.StateDegraded)

	if budgetExceeded {
		// 熔断：不再重启，标记 Failed（约束 C4）。
		i.lastExit = oldCode
		i.lastErr.Store(fmt.Errorf("proc: plugin %q crash loop: exit code %d, restarts exceeded %d (circuit open)",
			i.id, oldCode, i.opt.MaxRestarts))
		i.state.Store(loader.StateFailed)
		return
	}

	// 指数退避：backoff * 2^(attempt-1)，设上限防止过长等待。
	backoff := i.opt.RestartBackoff * time.Duration(1<<(attempt-1))
	if cap := i.opt.HandshakeTimeout * 2; backoff > cap {
		backoff = cap
	}
	select {
	case <-i.svStopChan():
		return // 已停止，不再重启
	case <-time.After(backoff):
	}

	// 关闭旧代连接，拉起新代。
	ctx, cancel := context.WithTimeout(context.Background(), i.opt.HandshakeTimeout*3)
	defer cancel()
	newRP, err := i.spawn(ctx, i.desc)
	if err != nil {
		i.lastErr.Store(fmt.Errorf("proc: restart %q failed (exit code %d): %w", i.id, oldCode, err))
		// 重启失败也消耗预算，下一个心跳继续尝试；预算耗尽则熔断。
		return
	}
	i.lastErr.Store(fmt.Errorf("proc: restart %q recovered on attempt %d", i.id, attempt))

	i.mu.Lock()
	old := i.cur
	i.cur = newRP
	i.mu.Unlock()
	if old != nil {
		old.closeGeneration()
	}
	// 进程级恢复已完成：重置调用级熔断器，
	// 否则崩溃累计的失败计数会把新进程继续挡在门外（"重启成功但仍 circuit open"）。
	i.caller.ResetBreaker()
	i.state.Store(loader.StateReady)
}

func (i *procInstance) svStopChan() <-chan struct{} {
	i.svMu.Lock()
	defer i.svMu.Unlock()
	if i.stopSv == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return i.stopSv
}

// stopSupervisor 停止监督循环并等待退出。
func (i *procInstance) stopSupervisor() {
	if i.svStopped.Swap(true) {
		return
	}
	i.svMu.Lock()
	if i.stopSv != nil {
		close(i.stopSv)
	}
	i.svMu.Unlock()
	i.svWG.Wait()
}

// Restarts 返回稳定期内已使用的重启次数（可观测）。
func (i *procInstance) Restarts() int {
	i.restartMu.Lock()
	defer i.restartMu.Unlock()
	return i.restarts
}

// stderrTail 保存每个插件最近若干行 stderr，供 /plugins 端点与排障使用（约束 E1）。
const stderrTailLines = 20

type stderrRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *stderrRing) push(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > stderrTailLines {
		r.lines = r.lines[len(r.lines)-stderrTailLines:]
	}
}

func (r *stderrRing) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// StderrTail 返回该插件进程最近的 stderr 输出。
func (i *procInstance) StderrTail() []string { return i.stderr.tail() }

// captureStderr 持续读取子进程 stderr：既保留尾部供排障，也转发给宿主日志。
func captureStderr(pluginID string, r io.Reader) {
	ring := stderrRingFor(pluginID)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		ring.push(line)
		stderrSinkMu.RLock()
		fn := stderrSinkFn
		stderrSinkMu.RUnlock()
		if fn != nil {
			fn(pluginID, line)
		}
	}
}

var (
	stderrSinkMu sync.RWMutex
	// stderrSinkFn 由宿主注入，用于把插件日志接入统一日志系统（约束 E1）。
	stderrSinkFn func(pluginID, line string)
	// stderrRings 保存每个插件的 stderr 尾部，供排障与 /plugins 端点使用。
	stderrRings sync.Map // pluginID -> *stderrRing
)

// SetStderrSink 设置 stderr 汇聚回调（宿主可接到自己的日志系统）。
func SetStderrSink(fn func(pluginID, line string)) {
	stderrSinkMu.Lock()
	stderrSinkFn = fn
	stderrSinkMu.Unlock()
}

func stderrRingFor(pluginID string) *stderrRing {
	v, _ := stderrRings.LoadOrStore(pluginID, &stderrRing{})
	return v.(*stderrRing)
}

// quota 返回该实例的配额对象；未注入时返回 nil 表示不额外限流
// （Loader 未接入配额时的兼容路径；生产由 Loader 保证注入）。
func (i *procInstance) quota() *quota.Quota { return i.qa.Load() }

// setQuota 由 Loader 在 Load 成功后注入配额。
func (i *procInstance) setQuota(q *quota.Quota) { i.qa.Store(q) }

// childEnv 构造子进程环境变量。
//
// 关键坑：必须**始终**显式设置 cmd.Env。
// exec.Cmd 在 Env 为 nil 时继承父进程环境；但一旦我们为注入进程级限制而
// 构造新切片，就必须保证包含调用方指定的 Env，否则插件会丢失自定义环境
// （例如 PATH、RESTART_SCENARIO 之类）。
//
// 进程级 rlimit 通过 contract.RLimitEnvVar 下发：Go 的 os/exec 无法为子进程
// 设置 rlimit，因此约定插件在 main 第一件事自行套用（见 quota 包注释）。
// 非支持平台上 EncodeEnv 返回空串，此处不会引入无效变量。
func (i *procInstance) childEnv() []string {
	var env []string
	if len(i.opt.Env) > 0 {
		env = append(env, i.opt.Env...)
	} else {
		env = os.Environ()
	}
	if v := quota.EncodeEnv(i.opt.Quota); v != "" {
		env = append(env, contract.RLimitEnvVar+"="+v)
	}
	return env
}

// setLastError 记录实例级最近错误（供 /plugins 端点展示，约束 E1）。
//
// 关键坑：只在实例已经 Failed/Degraded 时覆盖正常错误，
// 避免把"某次调用失败"永久钉在状态里误导排查。
func (i *procInstance) setLastError(err error) {
	if err == nil {
		return
	}
	st := i.State()
	if st == loader.StateFailed || st == loader.StateDegraded {
		i.lastErr.Store(err)
	}
}

// Version 返回插件版本（来自 Descriptor，宿主加载时给定）。
func (i *procInstance) Version() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.desc.Version
}

// LoadedAt 返回插件加载时刻。
func (i *procInstance) LoadedAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.loadedAt
}

// ExitCode 返回最近一代子进程的退出码；仍在运行为 0。
func (i *procInstance) ExitCode() int64 {
	i.mu.RLock()
	cur := i.cur
	i.mu.RUnlock()
	if cur == nil {
		return 0
	}
	return cur.exitCode.Load()
}

// LastError 返回实例级最近一次错误。
func (i *procInstance) LastError() error {
	err, _ := i.lastErr.Load().(error)
	return err
}

// Caller 返回该实例的受保护调用器（供可观测层采集指标，约束 E1）。
func (i *procInstance) Caller() *safecall.Caller { return i.caller }
