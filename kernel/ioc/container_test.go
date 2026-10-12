package ioc

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type service struct{ name string }

func TestProvideResolve(t *testing.T) {
	c := New()
	MustProvide(c, func(c *Container) (*service, error) {
		return &service{name: "svc"}, nil
	})
	s, err := Resolve[*service](c)
	if err != nil {
		t.Fatal(err)
	}
	if s.name != "svc" {
		t.Fatalf("name = %s", s.name)
	}
}

func TestNoProvider(t *testing.T) {
	c := New()
	_, err := Resolve[*service](c)
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v", err)
	}
}

// 约束 B7：单例并发初始化必须只构造一次。
func TestConcurrentSingleton(t *testing.T) {
	c := New()
	var built atomic.Int64
	MustProvide(c, func(c *Container) (*service, error) {
		built.Add(1)
		return &service{name: "single"}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Resolve[*service](c)
			if err != nil {
				t.Error(err)
				return
			}
			if s.name != "single" {
				t.Errorf("bad instance %s", s.name)
			}
		}()
	}
	wg.Wait()
	if built.Load() != 1 {
		t.Fatalf("provider built %d times, want 1", built.Load())
	}
}

func TestDuplicateProvide(t *testing.T) {
	c := New()
	MustProvide(c, func(c *Container) (*service, error) { return &service{}, nil })
	err := Provide(c, func(c *Container) (*service, error) { return &service{}, nil })
	if err == nil {
		t.Fatal("want duplicate provider error")
	}
}

type svcA struct{}
type svcB struct{}

// 约束 B7：循环依赖必须被检出并报错，而不是死锁。
//
// 这是一条真实踩过的坑：provider 处于 building 状态时，
// 若构造者递归请求同一类型并阻塞在 done 通道上，容器会永久死锁。
// 本用例以超时兜底，确保回归时能立刻暴露。
func TestCircularDependencyDetected(t *testing.T) {
	c := New()
	MustProvide(c, func(c *Container) (*svcA, error) {
		_, err := Resolve[*svcB](c)
		return &svcA{}, err
	})
	MustProvide(c, func(c *Container) (*svcB, error) {
		_, err := Resolve[*svcA](c)
		return &svcB{}, err
	})

	done := make(chan error, 1)
	go func() {
		_, err := Resolve[*svcA](c)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCircular) {
			t.Fatalf("want ErrCircular, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("死锁：循环依赖未被检出，构造者永久阻塞在 done 通道上")
	}
}

// 构造失败后应允许重试（不能把失败态永久缓存成死值）。
func TestFailedProviderCanRetry(t *testing.T) {
	c := New()
	var n atomic.Int64
	MustProvide(c, func(c *Container) (*service, error) {
		if n.Add(1) == 1 {
			return nil, errors.New("transient")
		}
		return &service{name: "ok"}, nil
	})
	if _, err := Resolve[*service](c); err == nil {
		t.Fatal("first resolve should fail")
	}
	s, err := Resolve[*service](c)
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if s.name != "ok" {
		t.Fatalf("name = %s", s.name)
	}
}
