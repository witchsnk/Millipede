package eventbus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	b := New(Options{Workers: 2, QueueSize: 16})
	defer b.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	var got Event
	b.Subscribe("t", func(ctx context.Context, e Event) {
		got = e
		wg.Done()
	})
	b.Publish(Event{Topic: "t", Payload: "hello", Source: "test"})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
	if got.Payload != "hello" || got.Source != "test" {
		t.Fatalf("got %+v", got)
	}
}

// 约束 B6：慢消费者超时应被切断并计入死信，不拖垮总线。
func TestSlowHandlerDeadLetter(t *testing.T) {
	b := New(Options{Workers: 1, QueueSize: 8, HandleTimeout: 50 * time.Millisecond})
	defer b.Close()
	b.Subscribe("slow", func(ctx context.Context, e Event) {
		time.Sleep(time.Second) // 慢消费者
	})
	b.Publish(Event{Topic: "slow"})
	time.Sleep(300 * time.Millisecond)
	dead := b.DeadLetters()
	if len(dead) == 0 {
		t.Fatal("expected dead letter for slow handler")
	}
	if dead[0].Reason != "handler timeout" {
		t.Fatalf("reason = %s", dead[0].Reason)
	}
}

// 约束 B6：handler panic 不应杀死总线。
func TestHandlerPanicContained(t *testing.T) {
	b := New(Options{Workers: 1, QueueSize: 8})
	defer b.Close()
	b.Subscribe("panic", func(ctx context.Context, e Event) {
		panic("boom")
	})
	b.Publish(Event{Topic: "panic"})
	time.Sleep(200 * time.Millisecond)
	dead := b.DeadLetters()
	if len(dead) == 0 {
		t.Fatal("expected dead letter for panic")
	}
	// 总线仍可用
	var wg sync.WaitGroup
	wg.Add(1)
	b.Subscribe("ok", func(ctx context.Context, e Event) { wg.Done() })
	b.Publish(Event{Topic: "ok"})
	c := make(chan struct{})
	go func() { wg.Wait(); close(c) }()
	select {
	case <-c:
	case <-time.After(time.Second):
		t.Fatal("bus dead after panic")
	}
}

// 队列满时丢弃并计数，生产者不阻塞。
func TestQueueFullDrop(t *testing.T) {
	b := New(Options{Workers: 1, QueueSize: 1, HandleTimeout: time.Second})
	defer b.Close()
	block := make(chan struct{})
	b.Subscribe("x", func(ctx context.Context, e Event) { <-block })
	for i := 0; i < 100; i++ {
		b.Publish(Event{Topic: "x"}) // 应不阻塞
	}
	close(block)
	time.Sleep(100 * time.Millisecond)
	pub, drop := b.Stats()
	if pub != 100 {
		t.Fatalf("published = %d", pub)
	}
	if drop == 0 {
		t.Fatal("expected some drops")
	}
}
