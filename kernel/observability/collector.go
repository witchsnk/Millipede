package observability

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/safecall"
)

// Source 是一个可被端点采集的后端加载器。
//
// 只依赖最小面：Kind + Load 出来的实例。
// 能提供 Inspect 的后端会被额外采集状态详情（约束 E2）。
type Source struct {
	// Loader 后端加载器。
	Loader loader.Loader
	// Desc 已加载插件的描述符，按实例 ID 索引。
	Desc map[string]loader.Descriptor
	// Caller 返回该插件的调用器（用于指标）。可为 nil。
	Caller func(pluginID string) *safecall.Caller
}

// LoaderCollector 把多个后端聚合成端点的数据源。
//
// 为什么需要聚合：builtin 与 proc 是两个独立的 Loader 实例，
// 而 /plugins 端点要一次看全所有后端的插件。
type LoaderCollector struct {
	sources []*Source

	mu      sync.RWMutex
	refresh time.Time
	cache   []Entry
	ttl     time.Duration
	now     func() time.Time
}

// NewLoaderCollector 构造聚合采集器。
//
// ttl 是缓存有效期：端点可能被高频轮询，
// 而 Inspect 要跨后端查询；用短 TTL 换掉高频跨后端调用。
// ttl<=0 时使用默认 500ms。
func NewLoaderCollector(ttl time.Duration, sources ...*Source) *LoaderCollector {
	if ttl <= 0 {
		ttl = 500 * time.Millisecond
	}
	return &LoaderCollector{
		sources: sources,
		ttl:     ttl,
		now:     time.Now,
	}
}

// Plugins 返回全部插件状态（满足 Collector 接口）。
func (c *LoaderCollector) Plugins() []Entry {
	now := c.now()
	c.mu.RLock()
	if c.cache != nil && now.Sub(c.refresh) < c.ttl {
		out := c.cache
		c.mu.RUnlock()
		return out
	}
	c.mu.RUnlock()

	// 在锁外采集：Inspect 可能触发跨后端 IO，不能持锁做。
	var entries []Entry
	ctx := context.Background()
	for _, src := range c.sources {
		if src == nil || src.Loader == nil {
			continue
		}
		entries = append(entries, collectFrom(ctx, src)...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	c.mu.Lock()
	c.cache = entries
	c.refresh = now
	c.mu.Unlock()
	return entries
}

// collectFrom 从单个后端采集插件状态。
func collectFrom(ctx context.Context, src *Source) []Entry {
	insp, hasInspect := src.Loader.(loader.Inspector)
	var out []Entry

	for id, d := range src.Desc {
		e := Entry{
			ID:      id,
			Backend: string(d.Backend),
			Kind:    d.Kind,
			Version: d.Version,
		}

		if hasInspect {
			info, err := insp.Inspect(ctx, id)
			if err != nil {
				// 查不到详情不应当让整个端点失败：
				// 记录状态未知，继续输出其余插件。
				e.State = loader.StateFailed
				e.LastError = err.Error()
			} else {
				e.State = info.State
				if info.Version != "" {
					e.Version = info.Version
				}
				e.LoadedAt = info.LoadedAt
				e.Restarts = info.Restarts
				e.ExitCode = info.ExitCode
				e.WatchTracked = info.WatchTracked
				if info.LastError != nil {
					e.LastError = info.LastError.Error()
				}
				e.Quota = QuotaSnapshot{
					Allowed:        info.Quota.Allowed,
					RejectedQPS:    info.Quota.RejectedQPS,
					RejectedBudget: info.Quota.RejectedBudget,
				}
			}
		}
		if e.State == "" {
			e.State = loader.StateReady
		}

		if src.Caller != nil {
			if caller := src.Caller(id); caller != nil {
				e.Metrics = metricsOf(caller)
			}
		}
		out = append(out, e)
	}
	return out
}

// metricsOf 把 safecall 指标投影为端点用的快照。
//
// 关键坑：Total 为 0 时不能计算平均耗时，
// 否则会得到 NaN 并污染 JSON（json.Marshal 对 NaN 直接报错，
// 会让整个端点 500——一个插件没有调用过，不该让运维页面不可用）。
func metricsOf(c *safecall.Caller) MetricSnapshot {
	s := c.Metrics().Snapshot()
	m := MetricSnapshot{
		Total:              s.Total,
		Errors:             s.Errors,
		Panics:             s.Panics,
		Timeouts:           s.Timeouts,
		CircuitTrips:       s.CircuitTrips,
		ConcurrencyRejects: s.ConcurrencyRejects,
	}
	if s.Total > 0 {
		m.AvgLatencyMicros = float64(s.TotalNanos) / float64(s.Total) / 1000
	}
	return m
}

// Invalidate 使缓存失效（下一次 Plugins 调用会重新采集）。
//
// 热更/加载/卸载后应由宿主调用，避免读到过期状态。
func (c *LoaderCollector) Invalidate() {
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
}
