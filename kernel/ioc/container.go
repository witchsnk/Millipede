// Package ioc 提供最小可用的泛型依赖注入容器。
//
// 约束（docs/03-约束与门禁.md B7）：单例并发初始化必须防重入；
// 必须做循环依赖检测；对外只暴露本包 API，不把第三方容器类型泄漏给业务。
package ioc

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ErrNoProvider 表示未注册某类型的提供者。
var ErrNoProvider = errors.New("ioc: no provider")

// ErrCircular 表示检测到循环依赖。
var ErrCircular = errors.New("ioc: circular dependency")

const (
	stIdle     = 0 // 未开始构造
	stBuilding = 1 // 正在构造
	stDone     = 2 // 构造成功
	stFailed   = 3 // 构造失败，可重试
)

// provider 用状态机管理单例构造，支持并发安全与循环依赖检测：
//   - idle -> building：只有胜者真正执行构造函数；
//   - 其他 goroutine 在 done 通道上等待结果，绝不重复构造；
//   - 同一 goroutine 在构造期间再次请求同一类型 => 循环依赖，直接报错，
//     而不是阻塞在 done 上造成死锁（这是本实现最容易踩的坑）。
type provider struct {
	mu      sync.Mutex
	state   int
	done    chan struct{}
	val     any
	err     error
	buildFn func(c *Container) (any, error)
	// builderGID 记录正在执行构造的 goroutine，用于识别"递归构造"。
	builderGID uint64
}

// Container 是线程安全的 IoC 容器。
type Container struct {
	mu        sync.RWMutex
	providers map[reflect.Type]*provider
}

// New 构造空容器。
func New() *Container {
	return &Container{providers: make(map[reflect.Type]*provider)}
}

// Provide 注册类型 T 的提供者。重复注册返回错误（防止静默覆盖）。
func Provide[T any](c *Container, f func(c *Container) (T, error)) error {
	t := typeOf[T]()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.providers[t]; ok {
		return fmt.Errorf("ioc: provider for %s already registered", t)
	}
	p := &provider{done: make(chan struct{})}
	p.buildFn = func(cc *Container) (any, error) { return f(cc) }
	c.providers[t] = p
	return nil
}

// MustProvide 注册失败即 panic（仅启动期使用，快速失败）。
func MustProvide[T any](c *Container, f func(c *Container) (T, error)) {
	if err := Provide(c, f); err != nil {
		panic(err)
	}
}

// Resolve 解析类型 T 的单例。
//
// 热路径（单例已构造）为 O(1) 直接返回；仅在需要构造时才做一次 goid
// 采样用于循环检测，开销只发生在低频的构造阶段。
func Resolve[T any](c *Container) (T, error) {
	var zero T
	t := typeOf[T]()

	c.mu.RLock()
	p, ok := c.providers[t]
	c.mu.RUnlock()
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNoProvider, t)
	}

	val, err := p.get(c, t)
	if err != nil {
		return zero, err
	}
	tv, ok := val.(T)
	if !ok {
		return zero, fmt.Errorf("ioc: provider returned wrong type for %s", t)
	}
	return tv, nil
}

// get 获取单例：胜者构造，其余等待；构造失败允许后续重试。
func (p *provider) get(c *Container, t reflect.Type) (any, error) {
	// 快路径：已构造完成，零开销返回（不触碰 goid）。
	p.mu.Lock()
	if p.state == stDone {
		v := p.val
		p.mu.Unlock()
		return v, nil
	}
	if p.state == stBuilding {
		gid := p.builderGID
		done := p.done
		p.mu.Unlock()

		// 关键坑：若"正在构造自己"的 goroutine 又递归请求同一类型，
		// 等待 done 会永久阻塞（死锁）。此时必须立即判定为循环依赖。
		if goid() == gid {
			return nil, fmt.Errorf("%w: %s (recursive construction)", ErrCircular, t)
		}
		<-done
		p.mu.Lock()
		v, err := p.val, p.err
		st := p.state
		p.mu.Unlock()
		if st == stFailed {
			return nil, err
		}
		return v, nil
	}
	// idle 或 stFailed：本 goroutine 成为构造者（失败态允许重试，
	// 因此这里重置 done 通道与状态机）。
	if p.state == stFailed {
		p.done = make(chan struct{})
		p.err = nil
		p.val = nil
	}
	gid := goid()
	p.state = stBuilding
	p.builderGID = gid
	p.mu.Unlock()

	v, err := p.buildFn(c)

	p.mu.Lock()
	if err != nil {
		p.state = stFailed
		p.err = err
	} else {
		p.state = stDone
		p.val = v
		p.err = nil
	}
	p.builderGID = 0
	close(p.done)
	p.mu.Unlock()
	return v, err
}

// typeOf 取 T 的 reflect.Type，对接口类型也能正确工作。
func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}
