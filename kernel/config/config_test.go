package config

import (
	"sync"
	"testing"
	"time"
)

func TestViewIsolation(t *testing.T) {
	s := New()
	s.Set("plugin-a", "endpoint", "http://a")
	s.Set("plugin-b", "endpoint", "http://b")

	va := s.View("plugin-a")
	vb := s.View("plugin-b")
	if va.GetString("endpoint", "") != "http://a" {
		t.Fatal("plugin-a endpoint wrong")
	}
	if vb.GetString("endpoint", "") != "http://b" {
		t.Fatal("plugin-b endpoint wrong")
	}
	// 隔离：a 看不到 b 的独立键
	s.Set("plugin-b", "secret", "s3cr3t")
	if _, ok := va.Get("secret"); ok {
		t.Fatal("plugin-a should not see plugin-b secret")
	}
}

func TestGetIntDefault(t *testing.T) {
	s := New()
	v := s.View("p")
	if got := v.GetInt("timeout", 3000); got != 3000 {
		t.Fatalf("default = %d", got)
	}
	s.Set("p", "timeout", 5000)
	if got := v.GetInt("timeout", 3000); got != 5000 {
		t.Fatalf("set = %d", got)
	}
}

func TestWatch(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	wg.Add(1)
	var gotKey string
	var gotNew any
	s.Watch("p", func(key string, old, new any) {
		gotKey, gotNew = key, new
		wg.Done()
	})
	s.Set("p", "k", "v")
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch not fired")
	}
	if gotKey != "p.k" || gotNew != "v" {
		t.Fatalf("watch got %q=%v", gotKey, gotNew)
	}
}

func TestRollbackOldValue(t *testing.T) {
	s := New()
	s.Set("p", "k", "v1")
	old := s.Set("p", "k", "v2")
	if old != "v1" {
		t.Fatalf("old = %v", old)
	}
	// 回滚
	s.Set("p", "k", old)
	if s.View("p").GetString("k", "") != "v1" {
		t.Fatal("rollback failed")
	}
}
