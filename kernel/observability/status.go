// Package observability 提供插件运行状态的可观测接口（约束 E2/E1）。
//
// 本包只做两件事：
//   - Registry：聚合各插件的状态、指标与最近错误，供宿主查询。
//   - Handler：把 Registry 渲染成一个只读的 HTTP 端点（默认 /plugins）。
//
// 设计约束：
//   - 只用标准库，不引入新依赖（A1）。
//   - 任何后端插件即使状态异常也不能让端点 panic（约束 B5 的同源要求）。
//   - 不创建后台常驻 goroutine（约束 B3），由宿主的 ctx 控制生命周期。
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
)

// Registry 聚合插件状态。
//
// 并发安全：注册表在宿主运行期可能被热更流程增删，
// 同时 HTTP 端点又在读取，因此所有访问都要持锁。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	version uint64 // 每次变更递增，供调用方判断是否有更新
}

// Entry 是一个插件的状态记录。
type Entry struct {
	// ID 插件唯一标识。
	ID string `json:"id"`
	// Backend 后端类型（builtin/proc）。
	Backend string `json:"backend"`
	// Kind 扩展点类型。
	Kind string `json:"kind,omitempty"`
	// State 当前状态（Ready/Degraded/Failed/...）。
	State loader.State `json:"state"`
	// Version 插件版本（若已知）。
	Version string `json:"version,omitempty"`
	// LoadedAt 加载时刻。
	LoadedAt time.Time `json:"loaded_at"`
	// UptimeSeconds 自加载以来的秒数。
	UptimeSeconds float64 `json:"uptime_seconds"`
	// Restarts 累计重启次数（proc 后端）。
	Restarts int64 `json:"restarts,omitempty"`
	// ExitCode 最近一代进程的退出码（0 表示运行中或未知）。
	ExitCode int64 `json:"exit_code,omitempty"`
	// LastError 最近一次错误（字符串形式，便于直接展示）。
	LastError string `json:"last_error,omitempty"`
	// WatchTracked 该插件是否被产物监听跟踪。
	WatchTracked bool `json:"watch_tracked"`
	// Metrics 调用指标快照。
	Metrics MetricSnapshot `json:"metrics"`
	// Quota 配额用量（约束 C1）。被限流/超预算时这里能直接看出来，
	// 否则"插件变慢"与"插件被限流"在监控上无法区分。
	Quota QuotaSnapshot `json:"quota"`
}

// QuotaSnapshot 是配额用量的只读投影。
type QuotaSnapshot struct {
	// Allowed 通过限流与预算检查的调用数。
	Allowed uint64 `json:"allowed"`
	// RejectedQPS 因 QPS 超限被拒的调用数。
	RejectedQPS uint64 `json:"rejected_qps"`
	// RejectedBudget 因预算耗尽被拒的调用数。
	RejectedBudget uint64 `json:"rejected_budget"`
}

// MetricSnapshot 是 safecall 指标的只读投影。
//
// 这里不复用 safecall.MetricsSnapshot，是为了**避免可观测层反向依赖
// 调用边界包**（那会让依赖方向倒过来）。字段语义保持一致。
type MetricSnapshot struct {
	Total              int64 `json:"total"`
	Errors             int64 `json:"errors"`
	Panics             int64 `json:"panics"`
	Timeouts           int64 `json:"timeouts"`
	CircuitTrips       int64 `json:"circuit_trips"`
	ConcurrencyRejects int64 `json:"concurrency_rejects"`
	// AvgLatencyMicros 平均调用耗时（微秒）。
	AvgLatencyMicros float64 `json:"avg_latency_micros"`
}

// NewRegistry 构造注册表。
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Upsert 新增或覆盖某插件的状态记录。
func (r *Registry) Upsert(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.UptimeSeconds = uptime(e.LoadedAt)
	r.entries[e.ID] = &e
	r.version++
}

// Remove 移除某插件记录。
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[id]; ok {
		delete(r.entries, id)
		r.version++
	}
}

// Snapshot 返回当前全部插件状态，按 ID 排序（便于稳定输出与 diff）。
func (r *Registry) Snapshot() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		c := *e
		c.UptimeSeconds = uptime(e.LoadedAt)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len 返回当前记录数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// uptime 计算已运行秒数；LoadedAt 为零时返回 0。
func uptime(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return d.Seconds()
}

// Inspector 是 loader.Inspector 的别名，便于本包使用者只 import observability。
//
// 真实定义在 kernel/loader（与 Descriptor/Instance 同处一个包），
// 避免可观测层反向定义一份导致两个接口"看起来一样但不是同一个"。
type Inspector = loader.Inspector

// Info 是 loader.Info 的别名。
type Info = loader.Info

// ErrUnknownPlugin 表示注册表里没有该插件。
var ErrUnknownPlugin = errors.New("observability: unknown plugin")

// Collector 是端点渲染时的数据来源。
//
// 它把"从 Loader 拉详情"与"渲染 HTTP"解耦，使得
// 既可以用真实 Loader，也可以用测试桩。
type Collector interface {
	// Plugins 返回当前所有插件的状态记录。
	Plugins() []Entry
}

// Handler 返回只读状态端点。
//
// 安全要求（对应约束 E1/E2）：
//   - 入口 recover：任何插件状态异常都不得让端点 500 或 panic（约束 B5）。
//   - 只读：仅 GET，其他方法返回 405。
//   - 输出有界：记录数超过上限时截断并标注，避免异常插件把响应撑爆。
func Handler(c Collector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// 端点自身崩溃也不能影响宿主，更不能泄漏堆栈给调用方。
				http.Error(w, `{"error":"internal error building plugin status"}`, http.StatusInternalServerError)
			}
		}()
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		plugins := c.Plugins()
		// 防御性排序：Collector 接口不保证顺序，
		// 而运维页面与自动化 diff 都依赖稳定输出。
		sort.Slice(plugins, func(i, j int) bool { return plugins[i].ID < plugins[j].ID })
		truncated := false
		if len(plugins) > maxEntries {
			plugins = plugins[:maxEntries]
			truncated = true
		}

		body, err := json.MarshalIndent(StatusResponse{
			Count:     len(plugins),
			Truncated: truncated,
			Plugins:   plugins,
		}, "", "  ")
		if err != nil {
			http.Error(w, `{"error":"cannot encode plugin status"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(body, '\n'))
	})
}

// maxEntries 限制单次响应的插件数上限。
const maxEntries = 500

// StatusResponse 是端点响应体。
type StatusResponse struct {
	// Count 本次返回的插件数。
	Count int `json:"count"`
	// Truncated 是否因数量超限而截断。
	Truncated bool `json:"truncated,omitempty"`
	// Plugins 插件状态列表。
	Plugins []Entry `json:"plugins"`
}

// Mux 把状态端点挂到给定 mux 上，返回 mux 便于链式注册其他路由。
func Mux(mux *http.ServeMux, pattern string, c Collector) *http.ServeMux {
	if mux == nil {
		mux = http.NewServeMux()
	}
	if pattern == "" {
		pattern = "/plugins"
	}
	mux.Handle(pattern, Handler(c))
	return mux
}

// Server 是带优雅关闭的状态端点服务器。
//
// 生命周期完全由 Start 的 ctx 控制：ctx 取消后 Shutdown，
// 不残留 goroutine（约束 B3）。
type Server struct {
	srv *http.Server
	// ln 是实际监听的 listener；Start 后写入，用于读取真实地址。
	ln net.Listener
	// mu 保护 ln 的并发读写（Addr 可能被多线程调用）。
	mu sync.Mutex
}

// NewServer 构造状态端点服务器。
//
// addr 传 ":0" 或 "127.0.0.1:0" 时由内核分配端口；
// 实际地址在 Start 之后通过 Addr() 读取（必须读实际值，
// 写死 ":0" 会让调用方拿不到可用的 URL）。
func NewServer(addr string, c Collector) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           Mux(nil, "/plugins", c),
			ReadHeaderTimeout: 5 * time.Second,
			// 端点只读且数据量有界，超时给宽松一些即可。
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Addr 返回实际监听地址；尚未监听时返回空串。
//
// 关键坑：不能用 srv.Addr 作为"可连的地址"——
// 当传入 ":0" 时它仍然是 ":0"，调用方拿到后无法拨号。
// 必须返回 ln.Addr()。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Start 启动状态端点并阻塞至 ctx 结束时优雅关闭。
func (s *Server) Start(ctx context.Context) error {
	// 关键坑：先 net.Listen 再 Serve。
	// 若用 ListenAndServeS 拿到实际端口要靠 Addr()，但 http.Server.Addr()
	// 在 ":0" 场景下不给真实端口；手动 Listen 才能拿到内核分配的地址。
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("observability: listen on %s: %w", s.srv.Addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("observability: status server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("observability: shutdown: %w", err)
		}
		<-errCh
		return nil
	}
}
