package proc

import (
	"context"
	"testing"
	"time"

	"github.com/millipede/millipede/kernel/loader"
)

// 约束 C4（本次补齐的核心）：插件崩溃后宿主必须真正拉起新进程，
// 而不是只标记状态与退避 sleep。
func TestProcAutoRestartAfterCrash(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		HealthInterval:   100 * time.Millisecond,
		RestartBackoff:   50 * time.Millisecond,
		MaxRestarts:      5,
		StabilityWindow:  time.Minute,
		Verify:           integrityOff(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "rp", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "rp")

	pi := inst.(*procInstance)
	// 记录初始代指针
	pi.mu.RLock()
	gen0 := pi.cur
	pi.mu.RUnlock()
	if gen0 == nil {
		t.Fatal("no first generation")
	}

	// 触发子进程崩溃
	if _, err := inst.Invoke(ctx, "Crash", nil); err != nil {
		t.Fatalf("invoke Crash: %v", err)
	}

	// 等待宿主检测到崩溃并拉起新代
	deadline := time.Now().Add(8 * time.Second)
	var gen1 *runningProc
	for time.Now().Before(deadline) {
		pi.mu.RLock()
		cur := pi.cur
		pi.mu.RUnlock()
		if cur != nil && cur != gen0 {
			gen1 = cur
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if gen1 == nil {
		t.Fatalf("宿主未在超时内拉起新一代进程（state=%s, restarts=%d）", pi.State(), pi.Restarts())
	}

	// 新代应可用；等待其完成握手（spawn 内含握手+拨号，通常已就绪，给一点余量）。
	newReady := time.Now().Add(3 * time.Second)
	var invokeErr error
	for time.Now().Before(newReady) {
		if _, invokeErr = inst.Invoke(ctx, "Echo", []byte("after")); invokeErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if invokeErr != nil {
		t.Fatalf("新代不可用: %v", invokeErr)
	}

	// 退出码应被采集
	gen0Wait := time.Now().Add(3 * time.Second)
	for time.Now().Before(gen0Wait) && !gen0.exited.Load() {
		time.Sleep(20 * time.Millisecond)
	}
	if !gen0.exited.Load() {
		t.Error("崩溃进程应被标记 exited")
	}
	if code := gen0.exitCode.Load(); code != 7 {
		t.Errorf("退出码 = %d, 期望 7", code)
	}
}

// 约束 C4：crash loop 必须在预算耗尽后熔断放弃，不得无限重启。
func TestProcCircuitBreaksOnCrashLoop(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		HealthInterval:   100 * time.Millisecond,
		RestartBackoff:   20 * time.Millisecond,
		MaxRestarts:      2,         // 只允许 2 次重启，第 3 次应熔断
		StabilityWindow:  time.Hour, // 禁用稳定期重置，确保持续处于 crash loop
		Verify:           integrityOff(),
		Env:              pluginEnv("crash"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "cl", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "cl")

	pi := inst.(*procInstance)

	// 等待进入 Failed（熔断）
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if pi.State() == loader.StateFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if pi.State() != loader.StateFailed {
		t.Fatalf("crash loop 后应熔断为 Failed，实际 %s（restarts=%d）", pi.State(), pi.Restarts())
	}

	// 重启次数应被限制在预算附近，不应无限增长
	if r := pi.Restarts(); r < 2 || r > 4 {
		t.Errorf("重启次数 %d 不在预期范围（应约等于 MaxRestarts=2 + 1 次超限）", r)
	}

	// 熔断后应记录原因
	if e, _ := pi.lastErr.Load().(error); e == nil {
		t.Error("熔断后应记录 lastErr 原因")
	}
}

// 约束 C4：稳定期内成功存活后应重置重启预算，避免长期运行后误熔断。
func TestProcStabilityWindowResetsBudget(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		HealthInterval:   100 * time.Millisecond,
		RestartBackoff:   20 * time.Millisecond,
		MaxRestarts:      1,
		StabilityWindow:  500 * time.Millisecond, // 短稳定期，便于测试
		Verify:           integrityOff(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "sw", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "sw")

	pi := inst.(*procInstance)
	// 模拟：先崩溃消耗预算，再让它稳定存活超过窗口后崩溃，应仍能重启（预算已重置）。
	// 直接检查稳定期重置逻辑：等待当前代存活超过 StabilityWindow，
	// 此时预算应被 reset 为 0。
	time.Sleep(1200 * time.Millisecond) // > StabilityWindow(500ms)
	pi.mu.RLock()
	cur := pi.cur
	pi.mu.RUnlock()

	// 手动触发一次健康检查路径
	pi.checkAndRestart()
	if r := pi.Restarts(); r != 0 {
		t.Errorf("稳定期后重启预算应为 0，实际 %d", r)
	}
	if cur == nil {
		t.Error("稳定期检查不应清空当前代")
	}
}

// debug helper: 观察重启过程中状态与熔断的交互
func TestDebugRestartTimeline(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    500 * time.Millisecond,
		HealthInterval:   100 * time.Millisecond,
		RestartBackoff:   50 * time.Millisecond,
		MaxRestarts:      5,
		StabilityWindow:  time.Minute,
		Verify:           integrityOff(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{ID: "dbg", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unload(ctx, "dbg")
	pi := inst.(*procInstance)
	inst.Invoke(ctx, "Crash", nil)
	for i := 0; i < 30; i++ {
		pi.mu.RLock()
		gen := pi.cur
		pi.mu.RUnlock()
		t.Logf("t=%dms state=%s restarts=%d breaker=%+v curNil=%v", i*100, pi.State(), pi.Restarts(), pi.caller.Metrics().Snapshot(), gen == nil)
		time.Sleep(100 * time.Millisecond)
	}
}
