package safecall

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 约束 B5：panic 必须被边界捕获并转为 ErrPanic，不穿透宿主。
func TestPanicContained(t *testing.T) {
	var panicked atomic.Int64
	c := NewCaller("p1", Options{}, func(id string, r any, stack []byte) {
		panicked.Add(1)
	})
	err := c.Invoke(context.Background(), 0, func(ctx context.Context) error {
		panic("boom")
	})
	if !errors.Is(err, ErrPanic) {
		t.Fatalf("want ErrPanic, got %v", err)
	}
	if panicked.Load() != 1 {
		t.Fatal("onPanic should be called")
	}
	if c.Metrics().Panics.Load() != 1 {
		t.Fatal("panic metric should be 1")
	}
}

// 约束 C1：超时必须兜底（同进程无法硬阻断死循环，超时是唯一防线）。
func TestTimeout(t *testing.T) {
	c := NewCaller("p2", Options{}, nil)
	err := c.Invoke(context.Background(), 50*time.Millisecond, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	})
	if err == nil {
		t.Fatal("want timeout error")
	}
	if c.Metrics().Timeouts.Load() != 1 {
		t.Fatal("timeout metric should be 1")
	}
}

// 约束 C1：并发配额满则快速失败，保护系统。
func TestConcurrencyLimit(t *testing.T) {
	c := NewCaller("p3", Options{MaxConcurrency: 1}, nil)
	release := make(chan struct{})
	var firstErr, secondErr error
	done := make(chan struct{})
	go func() {
		firstErr = c.Invoke(context.Background(), 0, func(ctx context.Context) error {
			<-release
			return nil
		})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // 让第一个调用先占住配额
	secondErr = c.Invoke(context.Background(), 0, func(ctx context.Context) error {
		return nil
	})
	close(release)
	<-done
	if firstErr != nil {
		t.Fatalf("first invoke: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrConcurrencyLimit) {
		t.Fatalf("want ErrConcurrencyLimit, got %v", secondErr)
	}
}

// 约束 C4 思想：连续失败触发熔断，之后快速失败。
func TestCircuitBreaker(t *testing.T) {
	c := NewCaller("p4", Options{Breaker: BreakerOptions{FailureThreshold: 3, OpenDuration: 100 * time.Millisecond}}, nil)
	fail := func(ctx context.Context) error { return errors.New("x") }
	for i := 0; i < 3; i++ {
		_ = c.Invoke(context.Background(), 0, fail)
	}
	// 熔断后应快速失败
	err := c.Invoke(context.Background(), 0, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	// 半开后成功则恢复
	time.Sleep(120 * time.Millisecond)
	if err := c.Invoke(context.Background(), 0, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("half-open probe should succeed: %v", err)
	}
}

func TestSuccessMetric(t *testing.T) {
	c := NewCaller("p5", Options{}, nil)
	if err := c.Invoke(context.Background(), 0, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if c.Metrics().Total.Load() != 1 {
		t.Fatal("total should be 1")
	}
}
