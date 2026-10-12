package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/witchsnk/millipede/kernel/lifecycle"
	"github.com/witchsnk/millipede/kernel/loader"
	"github.com/witchsnk/millipede/kernel/quota"
	"github.com/witchsnk/millipede/kernel/safecall"
	"github.com/witchsnk/millipede/sdk/host"
)

// testLifecycle 是满足 Lifecycle 接口的最小实现。
//
// 不能直接用 lifecycle.Base：它只实现了 Stop，
// 缺 Init/Start，不满足接口。
type testLifecycle struct {
	*lifecycle.Base
}

func (t *testLifecycle) Init(ctx context.Context) error  { return nil }
func (t *testLifecycle) Start(ctx context.Context) error { return nil }

// testPlugin 构造一个最小可用的 builtin 插件。
func testPlugin(id string) Plugin {
	return Plugin{
		Desc: loader.Descriptor{ID: id, Kind: "test", Backend: loader.BackendBuiltin},
		New: func(env host.Env) (lifecycle.Lifecycle, error) {
			lc := &lifecycle.Base{}
			return &testLifecycle{Base: lc}, nil
		},
		Invoker: func(_ lifecycle.Lifecycle, method string, args any) (any, error) {
			if method != "Echo" {
				return nil, errors.New("unknown method: " + method)
			}
			s, _ := args.(string)
			return "echo:" + s, nil
		},
	}
}

func newLoaded(t *testing.T, id string, lim quota.Limits) (*Loader, loader.Instance) {
	t.Helper()
	l := New(safecall.Options{MaxConcurrency: 8}, nil)
	l.quotaLim = lim
	if err := l.Register(testPlugin(id)); err != nil {
		t.Fatalf("register: %v", err)
	}
	inst, err := l.Load(context.Background(), loader.Descriptor{
		ID: id, Backend: loader.BackendBuiltin,
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return l, inst
}

// 约束 C1：builtin 后端也必须执行配额，不能只有 proc 有。
// 这是"每插件配额"名副其实的关键——两个后端行为必须一致。
func TestBuiltinQuotaRejectsExcessCalls(t *testing.T) {
	l, inst := newLoaded(t, "bq1", quota.Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 2})
	ctx := context.Background()

	for i := range 2 {
		if _, err := inst.Invoke(ctx, "Echo", "ok"); err != nil {
			t.Fatalf("第 %d 次调用应成功: %v", i+1, err)
		}
	}
	_, err := inst.Invoke(ctx, "Echo", "over")
	if !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("预算耗尽后应返回 ErrQuotaExceeded，实际 %v", err)
	}

	info, err := l.Inspect(ctx, "bq1")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Quota.Allowed != 2 {
		t.Errorf("Allowed = %d, 期望 2", info.Quota.Allowed)
	}
	if info.Quota.RejectedBudget == 0 {
		t.Error("应有预算拒绝计数")
	}
}

// 配额拒绝不应计入插件错误统计：限流不是插件的错。
func TestBuiltinQuotaNotCountedAsPluginError(t *testing.T) {
	_, inst := newLoaded(t, "bq2", quota.Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 1})
	ctx := context.Background()

	if _, err := inst.Invoke(ctx, "Echo", "first"); err != nil {
		t.Fatalf("首次调用应成功: %v", err)
	}
	if _, err := inst.Invoke(ctx, "Echo", "second"); !errors.Is(err, quota.ErrQuotaExceeded) {
		t.Fatalf("应被配额拒绝，实际 %v", err)
	}

	bi := inst.(*instance)
	if e := bi.caller.Metrics().Snapshot().Errors; e != 0 {
		t.Errorf("配额拒绝不应计入插件错误，实际 Errors=%d", e)
	}
}

// 缺省配额也必须有界（约束 C1：不允许"无限"缺省）。
func TestBuiltinQuotaBoundedByDefault(t *testing.T) {
	_, inst := newLoaded(t, "bq3", quota.Limits{})
	ctx := context.Background()

	// 缺省突发桶 = ceil(200 QPS) = 200；一次性打满后应开始拒绝。
	rejected := false
	for range 500 {
		if _, err := inst.Invoke(ctx, "Echo", "x"); errors.Is(err, quota.ErrQuotaExceeded) {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Error("缺省配额下大量调用应触发限流（说明缺省不是无限）")
	}
}

func TestBuiltinInspectReportsState(t *testing.T) {
	l, _ := newLoaded(t, "bq4", quota.Limits{})
	info, err := l.Inspect(context.Background(), "bq4")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.State != loader.StateReady {
		t.Errorf("State = %q, 期望 Ready", info.State)
	}
	if info.LoadedAt.IsZero() {
		t.Error("LoadedAt 不应为零值")
	}
}

func TestBuiltinInspectUnknownPlugin(t *testing.T) {
	l := New(safecall.Options{}, nil)
	if _, err := l.Inspect(context.Background(), "nope"); !errors.Is(err, loader.ErrNotLoaded) {
		t.Errorf("未加载插件应返回 ErrNotLoaded，实际 %v", err)
	}
}

// builtin 不支持产物热更：必须明确报错，而不是返回空通道（约束 H4）。
func TestBuiltinWatchReturnsNotSupported(t *testing.T) {
	l := New(safecall.Options{}, nil)
	ch, err := l.Watch(context.Background())
	if !errors.Is(err, loader.ErrNotSupported) {
		t.Fatalf("builtin Watch 应返回 ErrNotSupported，实际 err=%v ch=%v", err, ch)
	}
	if ch != nil {
		t.Error("返回 ErrNotSupported 时通道必须为 nil，避免调用方误用")
	}
}

func TestBuiltinUnloadReturnsNotSupported(t *testing.T) {
	l, _ := newLoaded(t, "bq5", quota.Limits{})
	if err := l.Unload(context.Background(), "bq5"); !errors.Is(err, loader.ErrNotSupported) {
		t.Errorf("builtin Unload 应返回 ErrNotSupported，实际 %v", err)
	}
}
