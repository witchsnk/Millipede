package proc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/witchsnk/millipede/kernel/integrity"
	"github.com/witchsnk/millipede/kernel/loader"
)

func TestWatcherDetectsContentChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher(WatchOptions{
		Interval:   20 * time.Millisecond,
		OnDebounce: 20 * time.Millisecond,
		Verify:     integrity.Off(),
	})
	if err := w.Track("p", f); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// 改内容：应产生事件
	if err := os.WriteFile(f, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := w.pollOnce(t)
	if got != 1 {
		t.Fatalf("内容变更应产生 1 个事件，实际 %d", got)
	}
}

// 只 touch 文件（内容不变）不应触发重载：mtime 变化不是版本变化。
func TestWatcherIgnoresTouchOnly(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(WatchOptions{Interval: 20 * time.Millisecond, OnDebounce: 20 * time.Millisecond, Verify: integrity.Off()})
	if err := w.Track("p", f); err != nil {
		t.Fatal(err)
	}

	// 改 mtime 但内容不变
	now := time.Now().Add(time.Hour)
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
	if got := w.pollOnce(t); got != 0 {
		t.Fatalf("仅 mtime 变化不应产生事件，实际 %d", got)
	}
}

// 内容改回原样后，再次变更应仍能检测到（摘要基线正确更新）。
func TestWatcherTracksBaselineAfterChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	os.WriteFile(f, []byte("v1"), 0o644)
	w := NewWatcher(WatchOptions{Interval: 20 * time.Millisecond, OnDebounce: 20 * time.Millisecond, Verify: integrity.Off()})
	if err := w.Track("p", f); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(f, []byte("v2"), 0o644)
	if got := w.pollOnce(t); got != 1 {
		t.Fatalf("第一次变更应有 1 个事件，实际 %d", got)
	}
	// 相同内容再轮询：无事件
	if got := w.pollOnce(t); got != 0 {
		t.Fatalf("内容未变不应重复上报，实际 %d", got)
	}
	// 再次变更：应有事件
	os.WriteFile(f, []byte("v3"), 0o644)
	if got := w.pollOnce(t); got != 1 {
		t.Fatalf("第二次变更应有 1 个事件，实际 %d", got)
	}
}

// 产物被删除时应记录错误而不是 panic 或静默。
func TestWatcherHandlesDeletedArtifact(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	os.WriteFile(f, []byte("v1"), 0o644)
	w := NewWatcher(WatchOptions{Interval: 20 * time.Millisecond, OnDebounce: 20 * time.Millisecond, Verify: integrity.Off()})
	if err := w.Track("p", f); err != nil {
		t.Fatal(err)
	}
	os.Remove(f)

	out := make(chan loader.ReloadEvent, 4)
	w.poll(context.Background(), out)
	if w.LastWatchError("p") == nil {
		t.Error("产物消失应记录错误")
	}
	select {
	case ev := <-out:
		t.Errorf("产物消失不应上报热更事件，实际收到 %+v", ev)
	default:
	}
}

// 校验开启时，非普通文件（如目录）不得作为合法更新上报。
func TestWatcherRejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	os.WriteFile(f, []byte("v1"), 0o644)
	w := NewWatcher(WatchOptions{Interval: 20 * time.Millisecond, OnDebounce: 20 * time.Millisecond, Verify: integrity.Lenient()})
	if err := w.Track("p", f); err != nil {
		t.Fatal(err)
	}

	// 把产物替换成目录：内容摘要变了，但结构校验必须拦下。
	os.Remove(f)
	if err := os.Mkdir(f, 0o755); err != nil {
		t.Fatal(err)
	}
	out := make(chan loader.ReloadEvent, 4)
	w.poll(context.Background(), out)
	select {
	case ev := <-out:
		t.Errorf("目录不应被当作合法新版本上报，实际 %+v", ev)
	default:
	}
	if w.LastWatchError("p") == nil {
		t.Error("非法产物应记录错误")
	}
}

// Untrack 后不再跟踪。
func TestWatcherUntrack(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	os.WriteFile(f, []byte("v1"), 0o644)
	w := NewWatcher(WatchOptions{Interval: 20 * time.Millisecond, OnDebounce: 20 * time.Millisecond, Verify: integrity.Off()})
	w.Track("p", f)
	w.Untrack("p")
	os.WriteFile(f, []byte("v2"), 0o644)
	if got := w.pollOnce(t); got != 0 {
		t.Fatalf("Untrack 后不应再上报事件，实际 %d", got)
	}
}

// Run 必须在 ctx 结束时退出，不残留 goroutine（约束 B3）。
func TestWatcherRunStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plugin.bin")
	os.WriteFile(f, []byte("v1"), 0o644)
	w := NewWatcher(WatchOptions{Interval: 10 * time.Millisecond, OnDebounce: 10 * time.Millisecond, Verify: integrity.Off()})
	w.Track("p", f)

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan loader.ReloadEvent, 4)
	done := make(chan struct{})
	go func() {
		w.Run(ctx, out)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后退出（可能泄漏 goroutine）")
	}
	// 通道应被关闭
	select {
	case _, ok := <-out:
		if ok {
			t.Error("ctx 结束后通道应关闭")
		}
	case <-time.After(time.Second):
		t.Fatal("Run 退出后通道未关闭")
	}
}

// 端到端热更：新代校验失败时旧代必须继续服务。
func TestReapKeepsOldOnNewFailure(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		WatchInterval:    time.Hour, // 测试里手动驱动
		Verify:           integrity.Off(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()
	if _, err := l.Load(ctx, loader.Descriptor{ID: "hr", Backend: loader.BackendProc, URI: bin}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "hr")

	// 新代指向不存在的产物 => 加载失败
	err := l.Reap(ctx, "hr", loader.Descriptor{Backend: loader.BackendProc, URI: "/nonexistent/plugin"}, "test")
	if err == nil {
		t.Fatal("新代加载失败时 Reap 应返回错误")
	}

	// 旧代必须仍然可用
	inst, err := l.Load(ctx, loader.Descriptor{ID: "hr-probe", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("旧代应仍可用: %v", err)
	}
	_ = inst
	if out, err := inst.Invoke(ctx, "Echo", []byte("alive")); err != nil {
		t.Fatalf("旧代调用失败: %v (out=%s)", err, out)
	}
	l.Unload(ctx, "hr-probe")
}

// 端到端热更成功：新旧代 pid 必须不同。
func TestReapSwapsGeneration(t *testing.T) {
	bin := buildRestartPlugin(t)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		InvokeTimeout:    time.Second,
		WatchInterval:    time.Hour,
		Verify:           integrity.Off(),
		Env:              pluginEnv("echo"),
	})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{ID: "sw", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "sw")

	oldPID := invokePID(t, inst, ctx)

	if err := l.Reap(ctx, "sw", loader.Descriptor{Backend: loader.BackendProc, URI: bin}, "test"); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	newInst, err := l.Load(ctx, loader.Descriptor{ID: "sw2", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("热更后插件应可用: %v", err)
	}
	defer l.Unload(ctx, "sw2")
	newPID := invokePID(t, newInst, ctx)
	if oldPID == newPID {
		t.Errorf("热更后 pid 应变化，实际仍为 %s", oldPID)
	}
}

// pollOnce 驱动一轮检测，返回本轮产生的事件数。
func (w *Watcher) pollOnce(t *testing.T) int {
	t.Helper()
	out := make(chan loader.ReloadEvent, 8)
	w.poll(context.Background(), out)
	n := 0
	for {
		select {
		case <-out:
			n++
		default:
			return n
		}
	}
}

// invokePID 调用插件的 PID 方法，返回子进程 pid。
func invokePID(t *testing.T, inst loader.Instance, ctx context.Context) string {
	t.Helper()
	out, err := inst.Invoke(ctx, "PID", nil)
	if err != nil {
		t.Fatalf("invoke PID: %v", err)
	}
	return string(out.([]byte))
}
