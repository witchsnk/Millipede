package quota

import (
	"testing"
	"time"
)

func TestLimitsDefaultsAreBounded(t *testing.T) {
	// 约束 C1：不允许"无限"缺省。零值必须补全为有界默认值。
	l := Limits{}.WithDefaults()
	if l.MaxQPS <= 0 {
		t.Fatalf("MaxQPS 缺省必须为正数，实际 %v", l.MaxQPS)
	}
	if l.Burst <= 0 {
		t.Fatalf("Burst 缺省必须为正数，实际 %d", l.Burst)
	}
	if l.MaxQPS != DefaultQPS {
		t.Errorf("MaxQPS 缺省 = %v, 期望 %v", l.MaxQPS, DefaultQPS)
	}
}

func TestLimitsValidateRejectsNegative(t *testing.T) {
	cases := map[string]Limits{
		"MaxQPS":            {MaxQPS: -1},
		"Burst":             {Burst: -1},
		"MaxCalls":          {MaxCalls: -1},
		"CPUSeconds":        {CPUSeconds: -1},
		"AddressSpaceBytes": {AddressSpaceBytes: -1},
		"MaxProcs":          {MaxProcs: -1},
	}
	for name, l := range cases {
		if err := l.Validate(); err == nil {
			t.Errorf("%s 为负数时应报错，实际通过", name)
		}
	}
}

func TestQuotaRateLimitRejects(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 10, Burst: 2}, base)

	// 突发桶只有 2 个令牌，第三次必须被拒。
	if !q.Allow(base) {
		t.Fatal("第 1 次调用应放行")
	}
	if !q.Allow(base) {
		t.Fatal("第 2 次调用应放行（突发桶内）")
	}
	if q.Allow(base) {
		t.Fatal("第 3 次调用应被拒（超出突发桶）")
	}

	s := q.Stats()
	if s.Allowed != 2 {
		t.Errorf("Allowed = %d, 期望 2", s.Allowed)
	}
	if s.RejectedQPS == 0 {
		t.Error("应有 QPS 拒绝计数")
	}
}

// 令牌桶应随时间补充，且补充量受突发桶上限约束。
func TestQuotaRefillsOverTime(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 10, Burst: 2}, base)

	q.Allow(base)
	q.Allow(base)
	if q.Allow(base) {
		t.Fatal("桶空时应拒绝")
	}

	// 经过 100ms（10 QPS => 补充 1 个令牌）后应恢复。
	after := base.Add(100 * time.Millisecond)
	if !q.Allow(after) {
		t.Error("经过 100ms 后应有 1 个令牌可用")
	}
	if q.Allow(after) {
		t.Error("补充的令牌用完后应再次拒绝")
	}
}

// 长时间空闲后突发量不得超过 Burst 上限，否则会积累出巨大突发打爆下游。
func TestQuotaRefillCappedByBurst(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 10, Burst: 3}, base)

	// 空闲 1 小时：理论可补充 36000 个令牌，但必须被截断到 3。
	later := base.Add(time.Hour)
	allowed := 0
	for range 100 {
		if !q.Allow(later) {
			break
		}
		allowed++
	}
	if allowed != 3 {
		t.Errorf("空闲后最多放行 %d 次（Burst），实际 %d", 3, allowed)
	}
}

// 次数预算是硬上限：即使速率很低，预算耗尽后也必须拒绝。
func TestQuotaCallBudgetIsHardCap(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 1000, Burst: 1000, MaxCalls: 3}, base)

	for i := range 3 {
		if !q.Allow(base.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	// 速率还很宽松（令牌充足），但预算已耗尽，必须拒绝。
	if q.Allow(base.Add(10 * time.Second)) {
		t.Error("预算耗尽后必须拒绝，即使速率允许")
	}
	if q.Stats().RejectedBudget != 1 {
		t.Errorf("RejectedBudget = %d, 期望 1", q.Stats().RejectedBudget)
	}
}

func TestQuotaClosedRejectsAll(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := NewSimple(Limits{MaxQPS: 100})
	if !q.Allow(base) {
		t.Fatal("未关闭前应放行")
	}
	q.Close()
	q.Close() // 幂等
	if q.Allow(base) {
		t.Error("释放后必须拒绝所有调用")
	}
	if !q.Closed() {
		t.Error("Closed() 应返回 true")
	}
}

// 并发下配额必须是线程安全的，且总放行数不超过突发桶。
func TestQuotaConcurrentSafe(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 1, Burst: 20}, base)

	const goroutines = 100
	results := make(chan bool, goroutines)
	start := make(chan struct{})
	for range goroutines {
		go func() {
			<-start
			results <- q.Allow(base)
		}()
	}
	close(start)

	allowed := 0
	for range goroutines {
		if <-results {
			allowed++
		}
	}
	if allowed != 20 {
		t.Errorf("并发下放行 %d 次, 期望恰好 Burst=20 次", allowed)
	}
}

// 未显式设置 MaxCalls 时不应有总次数上限（只受速率约束）。
func TestQuotaWithoutBudgetOnlyRateLimited(t *testing.T) {
	base := time.Unix(1700000000, 0)
	q := New(Limits{MaxQPS: 10, Burst: 1}, base)

	allowed := 0
	for i := range 5 {
		if q.Allow(base.Add(time.Duration(i) * time.Second)) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("未设预算时应只受速率限制，实际放行 %d/5", allowed)
	}
}
