// Package spi 提供类型安全、并发安全的扩展点注册表，
// 等价于 Java 的 ServiceLoader/SPI 发现机制。
//
// 并发约定（约束 B4）：启动期写、运行期只读。
// 热更新不原地增删，而是通过 Snapshot/Replace 以 copy-on-write 整表替换。
package spi

import (
	"errors"
	"fmt"
	"sync"
)

// ErrDuplicate 表示同一名称的实现被重复注册。
var ErrDuplicate = errors.New("spi: duplicate implementation")

// Registry 是泛型扩展点注册表。
// T 通常为契约接口（如 contract.Notifier），存放其实现或工厂产物。
type Registry[T any] struct {
	mu    sync.RWMutex
	impl  map[string]T
	order []string // 保留注册顺序，便于责任链/优先级遍历
}

// NewRegistry 构造空注册表。
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{impl: make(map[string]T)}
}

// Register 注册一个实现，重名返回 ErrDuplicate（约束 A4：失败要明确报错）。
func (r *Registry[T]) Register(name string, v T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.impl[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	r.impl[name] = v
	r.order = append(r.order, name)
	return nil
}

// MustRegister 注册失败即 panic。仅供 init()/启动期使用，
// 因为启动期失败必须快速失败，而不是运行到一半才暴露。
func (r *Registry[T]) MustRegister(name string, v T) {
	if err := r.Register(name, v); err != nil {
		panic(err)
	}
}

// Get 按名取出实现。
func (r *Registry[T]) Get(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.impl[name]
	return v, ok
}

// List 按注册顺序返回全部实现的快照。
// 返回的是新切片，调用方可安全遍历，但元素本身共享（T 通常为接口/指针）。
func (r *Registry[T]) List() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]T, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.impl[n])
	}
	return out
}

// Names 按注册顺序返回名称快照。
func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Snapshot 返回当前注册表的只读副本（copy-on-write 的"读"侧）。
func (r *Registry[T]) Snapshot() map[string]T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]T, len(r.impl))
	for k, v := range r.impl {
		cp[k] = v
	}
	return cp
}

// Replace 以 copy-on-write 方式整体替换注册表内容（热更新路径）。
// 不在原表上增删，避免运行期读写竞争。
func (r *Registry[T]) Replace(impl map[string]T, order []string) {
	cp := make(map[string]T, len(impl))
	for k, v := range impl {
		cp[k] = v
	}
	ord := make([]string, len(order))
	copy(ord, order)

	r.mu.Lock()
	r.impl = cp
	r.order = ord
	r.mu.Unlock()
}
