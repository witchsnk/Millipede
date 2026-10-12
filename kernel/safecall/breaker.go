package safecall

import (
	"sync"
	"time"
)

// BreakerOptions 熔断器参数。
type BreakerOptions struct {
	FailureThreshold int           // 连续失败多少次后断开，<=0 默认 5
	OpenDuration     time.Duration // 断开持续多久后进入半开，<=0 默认 10s
	HalfOpenProbes   int           // 半开状态允许的探测数，<=0 默认 1
}

// CircuitBreaker 经典三态熔断器：closed -> open -> half-open。
type CircuitBreaker struct {
	mu       sync.Mutex
	state    state
	failures int
	openedAt time.Time
	opt      BreakerOptions
	probing  int
}

type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

// NewCircuitBreaker 构造熔断器。
func NewCircuitBreaker(opt BreakerOptions) *CircuitBreaker {
	if opt.FailureThreshold <= 0 {
		opt.FailureThreshold = 5
	}
	if opt.OpenDuration <= 0 {
		opt.OpenDuration = 10 * time.Second
	}
	if opt.HalfOpenProbes <= 0 {
		opt.HalfOpenProbes = 1
	}
	return &CircuitBreaker{opt: opt}
}

// Allow 判断是否放行本次调用。
func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateClosed:
		return true
	case stateOpen:
		if time.Since(b.openedAt) >= b.opt.OpenDuration {
			b.state = stateHalfOpen
			b.probing = 0
			return true
		}
		return false
	case stateHalfOpen:
		if b.probing < b.opt.HalfOpenProbes {
			b.probing++
			return true
		}
		return false
	}
	return true
}

// RecordSuccess 记录一次成功。
func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = stateClosed
}

// RecordFailure 记录一次失败，达到阈值则断开。
func (b *CircuitBreaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.opt.FailureThreshold {
		b.state = stateOpen
		b.openedAt = time.Now()
	}
}

// Reset 强制回到闭合状态并清零失败计数。
// 由"宿主已重启插件进程"这类进程级恢复动作调用（见 Caller.ResetBreaker）。
func (b *CircuitBreaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probing = 0
	b.state = stateClosed
}
