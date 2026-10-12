package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fake struct {
	initErr, startErr error
	stopCalls         int
	initCalled        bool
	startCalled       bool
}

func (f *fake) Init(ctx context.Context) error  { f.initCalled = true; return f.initErr }
func (f *fake) Start(ctx context.Context) error { f.startCalled = true; return f.startErr }
func (f *fake) Stop(ctx context.Context) error  { f.stopCalls++; return nil }

func TestBootSuccess(t *testing.T) {
	m := NewManager()
	a, b := &fake{}, &fake{}
	err := m.Boot(context.Background(), DefaultOptions(),
		BootItem{"a", a}, BootItem{"b", b})
	if err != nil {
		t.Fatal(err)
	}
	if !a.initCalled || !b.startCalled {
		t.Fatal("both should be inited/started")
	}
	if err := m.Shutdown(context.Background(), DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if a.stopCalls != 1 || b.stopCalls != 1 {
		t.Fatalf("stops = %d,%d", a.stopCalls, b.stopCalls)
	}
}

// 约束 B2：启动失败必须逆序回滚已启动实例。
func TestBootRollbackOnStartFailure(t *testing.T) {
	m := NewManager()
	a := &fake{}
	b := &fake{startErr: errors.New("boom")}
	err := m.Boot(context.Background(), DefaultOptions(),
		BootItem{"a", a}, BootItem{"b", b})
	if err == nil {
		t.Fatal("want error")
	}
	if a.stopCalls != 1 {
		t.Fatalf("a should be rolled back once, got %d", a.stopCalls)
	}
	if b.stopCalls != 0 {
		t.Fatalf("b never started, must not be stopped, got %d", b.stopCalls)
	}
}

// 约束 B1：Stop 幂等。
func TestBaseStopIdempotent(t *testing.T) {
	var b Base
	calls := 0
	b.SetStop(func(ctx context.Context) error { calls++; return nil })
	_ = b.Stop(context.Background())
	_ = b.Stop(context.Background())
	_ = b.Stop(context.Background())
	if calls != 1 {
		t.Fatalf("stop fn ran %d times, want 1", calls)
	}
}

func TestInitTimeout(t *testing.T) {
	m := NewManager()
	slow := &slowInit{d: 500 * time.Millisecond}
	opt := DefaultOptions()
	opt.InitTimeout = 50 * time.Millisecond
	err := m.Boot(context.Background(), opt, BootItem{"slow", slow})
	if err == nil {
		t.Fatal("want timeout error")
	}
}

type slowInit struct{ d time.Duration }

func (s *slowInit) Init(ctx context.Context) error {
	select {
	case <-time.After(s.d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *slowInit) Start(ctx context.Context) error { return nil }
func (s *slowInit) Stop(ctx context.Context) error  { return nil }
