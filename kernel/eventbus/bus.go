// Package eventbus 提供插件间解耦的异步事件总线（约束 B6）。
//
// 设计要点：异步投递（生产者不阻塞）+ 每订阅者超时 + 死信队列。
// 禁止在回调里做阻塞 IO——回调以独立 goroutine + 超时执行，
// 慢消费者被超时切断并计入死信，不拖垮其他订阅者。
package eventbus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Event 总线上的事件。
type Event struct {
	Topic   string
	Payload any
	Source  string // 发布者 plugin_id，便于追踪
}

// DeadLetter 死信。
type DeadLetter struct {
	Event  Event
	Reason string
	At     time.Time
}

// Handler 事件回调。回调内禁止阻塞 IO（约束 B6）。
type Handler func(ctx context.Context, e Event)

// Options 总线参数。
type Options struct {
	QueueSize     int           // 队列容量，<=0 默认 1024
	Workers       int           // 消费协程数，<=0 默认 4
	HandleTimeout time.Duration // 单个回调超时，<=0 默认 3s
}

// Bus 异步事件总线。
type Bus struct {
	opt       Options
	queue     chan Event
	mu        sync.RWMutex
	subs      map[string][]Handler
	deadMu    sync.Mutex
	dead      []DeadLetter
	wg        sync.WaitGroup
	stop      chan struct{}
	stopped   atomic.Bool
	dropped   atomic.Int64
	published atomic.Int64
}

// New 构造并启动事件总线。
func New(opt Options) *Bus {
	if opt.QueueSize <= 0 {
		opt.QueueSize = 1024
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.HandleTimeout <= 0 {
		opt.HandleTimeout = 3 * time.Second
	}
	b := &Bus{
		opt:   opt,
		queue: make(chan Event, opt.QueueSize),
		subs:  make(map[string][]Handler),
		stop:  make(chan struct{}),
	}
	for i := 0; i < opt.Workers; i++ {
		b.wg.Add(1)
		go b.worker()
	}
	return b
}

// Subscribe 订阅主题。
func (b *Bus) Subscribe(topic string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[topic] = append(b.subs[topic], h)
}

// Publish 异步发布事件。队列满时丢弃并计数（不阻塞生产者）。
func (b *Bus) Publish(e Event) {
	b.published.Add(1)
	select {
	case b.queue <- e:
	default:
		b.dropped.Add(1)
		b.addDead(e, "queue full")
	}
}

// worker 消费队列并分发。
func (b *Bus) worker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.stop:
			return
		case e := <-b.queue:
			b.dispatch(e)
		}
	}
}

// dispatch 把事件分发给该主题的所有订阅者，每个回调独立 goroutine + 超时。
func (b *Bus) dispatch(e Event) {
	b.mu.RLock()
	handlers := append([]Handler{}, b.subs[e.Topic]...)
	b.mu.RUnlock()

	for _, h := range handlers {
		h := h
		ctx, cancel := context.WithTimeout(context.Background(), b.opt.HandleTimeout)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					b.addDead(e, "handler panic")
				}
			}()
			h(ctx, e)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			b.addDead(e, "handler timeout")
		}
		cancel()
	}
}

func (b *Bus) addDead(e Event, reason string) {
	b.deadMu.Lock()
	defer b.deadMu.Unlock()
	b.dead = append(b.dead, DeadLetter{Event: e, Reason: reason, At: time.Now()})
}

// DeadLetters 返回死信快照。
func (b *Bus) DeadLetters() []DeadLetter {
	b.deadMu.Lock()
	defer b.deadMu.Unlock()
	out := make([]DeadLetter, len(b.dead))
	copy(out, b.dead)
	return out
}

// Stats 返回运行统计。
func (b *Bus) Stats() (published, dropped int64) {
	return b.published.Load(), b.dropped.Load()
}

// Close 停止总线并等待 worker 退出。
func (b *Bus) Close() {
	if b.stopped.Swap(true) {
		return
	}
	close(b.stop)
	b.wg.Wait()
}
