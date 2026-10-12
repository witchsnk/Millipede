package proc

import (
	"context"
	"testing"
	"time"

	"github.com/witchsnk/millipede/kernel/integrity"
	"github.com/witchsnk/millipede/kernel/loader"
)

// 集成测试：用真实子进程跑 proc 后端。
// 子进程二进制由 buildPlugin 在测试内编译，保证与当前代码一致。

// newTestLoader 构造测试用 proc 后端：关闭完整性校验（由专门用例覆盖 D1）。
func newTestLoader(opt Options) *Loader {
	opt.Verify = integrity.Off()
	return New(opt)
}

func TestProcHandshakeAndInvoke(t *testing.T) {
	bin := buildTestPlugin(t)
	l := newTestLoader(Options{
		HandshakeTimeout: 3 * time.Second,
		InvokeTimeout:    2 * time.Second,
	})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{ID: "tp", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer l.Unload(ctx, "tp")

	out, err := inst.Invoke(ctx, "Echo", []byte("ping"))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if string(out.([]byte)) != "pong:ping" {
		t.Fatalf("echo = %q", out)
	}
	if inst.State() != loader.StateReady {
		t.Fatalf("state = %s", inst.State())
	}
}

// 坑点：子进程崩溃后宿主不崩，且 Unload 幂等（约束 C4）。
func TestProcCrashIsolation(t *testing.T) {
	bin := buildTestPlugin(t)
	l := newTestLoader(Options{HandshakeTimeout: 3 * time.Second, InvokeTimeout: time.Second})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{ID: "tp2", Backend: loader.BackendProc, URI: bin})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 杀掉子进程，宿主调用应返回错误而非 panic。
	inst.(*procInstance).kill()
	_, err = inst.Invoke(ctx, "Echo", []byte("x"))
	if err == nil {
		t.Fatal("want error after kill")
	}
	if err := l.Unload(ctx, "tp2"); err != nil {
		t.Fatalf("unload after kill: %v", err)
	}
	// 再次 unload 应报错（已移除），但不 panic。
	_ = l.Unload(ctx, "tp2")
}

// ---- 约束 D1：加载前完整性校验 ----

// 校验和不匹配时必须拒绝加载，且不得启动任何进程。
// 注意：这里用默认策略（Lenient，强制校验和），不能用 newTestLoader 的 Off()。
func TestLoadRefusesChecksumMismatch(t *testing.T) {
	bin := buildTestPlugin(t)
	l := New(Options{HandshakeTimeout: 2 * time.Second})
	ctx := context.Background()

	_, err := l.Load(ctx, loader.Descriptor{
		ID:       "bad",
		Backend:  loader.BackendProc,
		URI:      bin,
		Checksum: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil {
		t.Fatal("应拒绝校验和不匹配的插件")
	}
}

// 默认策略为 Lenient：提供正确校验和时应正常加载。
func TestLoadWithCorrectChecksum(t *testing.T) {
	bin := buildTestPlugin(t)
	sum, err := integrity.SumFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	l := New(Options{HandshakeTimeout: 3 * time.Second, InvokeTimeout: 2 * time.Second})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{
		ID: "good", Backend: loader.BackendProc, URI: bin, Checksum: sum,
	})
	if err != nil {
		t.Fatalf("load with checksum: %v", err)
	}
	defer l.Unload(ctx, "good")
	if inst.State() != loader.StateReady {
		t.Fatalf("state = %s", inst.State())
	}
}

// Strict 策略下缺少签名必须拒绝加载。
func TestLoadRefusesMissingSignatureUnderStrict(t *testing.T) {
	bin := buildTestPlugin(t)
	sum, _ := integrity.SumFile(bin)
	l := New(Options{
		HandshakeTimeout: 2 * time.Second,
		Verify:           integrity.Strict([]byte("signing-key")),
	})
	ctx := context.Background()
	_, err := l.Load(ctx, loader.Descriptor{
		ID: "strict", Backend: loader.BackendProc, URI: bin, Checksum: sum,
	})
	if err == nil {
		t.Fatal("Strict 策略下缺少签名应拒绝加载")
	}
}

// Strict 策略 + 正确校验和与签名应加载成功。
func TestLoadPassesStrictWithValidSignature(t *testing.T) {
	bin := buildTestPlugin(t)
	sum, _ := integrity.SumFile(bin)
	sig, _ := integrity.Sign(bin, []byte("signing-key"))
	l := New(Options{
		HandshakeTimeout: 3 * time.Second,
		InvokeTimeout:    2 * time.Second,
		Verify:           integrity.Strict([]byte("signing-key")),
	})
	ctx := context.Background()
	inst, err := l.Load(ctx, loader.Descriptor{
		ID: "strict-ok", Backend: loader.BackendProc, URI: bin,
		Checksum: sum, Signature: sig,
	})
	if err != nil {
		t.Fatalf("strict load: %v", err)
	}
	defer l.Unload(ctx, "strict-ok")
	if inst.State() != loader.StateReady {
		t.Fatalf("state = %s", inst.State())
	}
}
