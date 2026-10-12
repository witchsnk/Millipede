package proc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/millipede/millipede/kernel/integrity"
	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/quota"
)

// 约束 C1：配额必须在**真实调用路径**上生效，而不只是 quota 包自己的单测。
// 这个用例是防止"quota 实现正确但接线是死代码"的关键。
func TestProcQuotaRejectsExcessCalls(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		// 极小预算：只允许 3 次调用
		Quota:  quota.Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 3},
		Verify: integrity.Off(),
		Env:    pluginEnv("echo"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "q1", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "q1")

	// 前 3 次必须成功
	for i := range 3 {
		if _, err := inst.Invoke(ctx, "Echo", []byte("ok")); err != nil {
			t.Fatalf("第 %d 次调用应成功: %v", i+1, err)
		}
	}

	// 第 4 次必须被配额拒绝
	_, err = inst.Invoke(ctx, "Echo", []byte("over"))
	if !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("预算耗尽后应返回 ErrQuotaExceeded，实际 %v", err)
	}

	// 配额统计应体现拒绝
	q := l.QuotaFor("q1")
	if q == nil {
		t.Fatal("QuotaFor 应返回配额对象")
	}
	st := q.Stats()
	if st.Allowed != 3 {
		t.Errorf("Allowed = %d, 期望 3", st.Allowed)
	}
	if st.RejectedBudget == 0 {
		t.Error("应有预算拒绝计数")
	}
}

// 配额拒绝必须发生在插件实际执行之前：即使插件完全健康，
// 预算耗尽后也不应该再发出 RPC。
func TestProcQuotaStopsBeforeRPC(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		Quota:            quota.Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 1},
		Verify:           integrity.Off(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "q2", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "q2")

	if _, err := inst.Invoke(ctx, "Echo", []byte("first")); err != nil {
		t.Fatalf("首次调用应成功: %v", err)
	}
	// 第二次被拒时，不应污染 safecall 的错误计数（配额在更外层）
	_, err = inst.Invoke(ctx, "Echo", []byte("second"))
	if !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("应被配额拒绝，实际 %v", err)
	}
	// safecall 的错误计数不应因配额拒绝而增加（配额不是插件的错）
	pi := inst.(*procInstance)
	if e := pi.Caller().Metrics().Snapshot().Errors; e != 0 {
		t.Errorf("配额拒绝不应计入插件错误计数，实际 Errors=%d", e)
	}
}

// Unload 后配额应被释放：再次 Allow 必须失败。
func TestProcQuotaReleasedOnUnload(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		Quota:            quota.Limits{MaxQPS: 100, Burst: 100},
		Verify:           integrity.Off(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()

	if _, err := l.Load(ctx, loader.Descriptor{ID: "q3", Backend: loader.BackendProc, URI: bin}); err != nil {
		t.Fatalf("load: %v", err)
	}
	q := l.QuotaFor("q3")
	if q == nil {
		t.Fatal("应能取到配额对象")
	}
	if err := l.Unload(ctx, "q3"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if !q.Closed() {
		t.Error("Unload 后配额对象应被关闭")
	}
	if q.Allow(time.Now()) {
		t.Error("已关闭的配额不应再放行")
	}
	if l.QuotaFor("q3") != nil {
		t.Error("Unload 后不应再能取到配额对象")
	}
}

// 配额用量必须能被 Inspect 看到（约束 E2 的可观测要求）。
func TestProcQuotaVisibleViaInspect(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		Quota:            quota.Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 2},
		Verify:           integrity.Off(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()

	inst, err := l.Load(ctx, loader.Descriptor{ID: "q4", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "q4")

	// 真正调用两次，预算为 2：两次都应放行。
	for i := range 2 {
		if _, err := inst.Invoke(ctx, "Echo", []byte("x")); err != nil {
			t.Fatalf("第 %d 次调用应成功: %v", i+1, err)
		}
	}

	info, err := l.Inspect(ctx, "q4")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Quota.Allowed != 2 {
		t.Errorf("Inspect 应能看到 2 次放行，实际 %d", info.Quota.Allowed)
	}
}
