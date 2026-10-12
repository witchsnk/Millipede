package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
	"github.com/witchsnk/millipede/kernel/safecall"
)

// stubCollector 是可控的采集器桩。
type stubCollector struct {
	mu      sync.Mutex
	entries []Entry
	calls   int
	// panics 为 true 时 Plugins 会 panic，用于验证端点自身不崩。
	panics bool
}

func (s *stubCollector) Plugins() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.panics {
		panic("collector exploded")
	}
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *stubCollector) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func getJSON(t *testing.T, h http.Handler, method string) (int, StatusResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/plugins", nil)
	h.ServeHTTP(rec, req)

	var resp StatusResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec.Code, resp
}

func TestHandlerReturnsPluginStatus(t *testing.T) {
	c := &stubCollector{entries: []Entry{
		{ID: "proc-notifier", Backend: "proc", State: loader.StateReady, Version: "1.0.0",
			Restarts: 2, WatchTracked: true, Metrics: MetricSnapshot{Total: 10, AvgLatencyMicros: 12.5}},
		{ID: "builtin-notifier", Backend: "builtin", State: loader.StateReady},
	}}
	code, resp := getJSON(t, Handler(c), http.MethodGet)
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", code)
	}
	if resp.Count != 2 || len(resp.Plugins) != 2 {
		t.Fatalf("应返回 2 个插件，实际 %d", resp.Count)
	}
	if resp.Plugins[0].ID != "builtin-notifier" {
		t.Errorf("结果应按 ID 排序，首项 = %s", resp.Plugins[0].ID)
	}
	p := resp.Plugins[1]
	if p.Restarts != 2 || !p.WatchTracked || p.Version != "1.0.0" {
		t.Errorf("详情丢失: %+v", p)
	}
}

// 插件处于 Failed 状态时端点仍必须正常返回（约束 B5 的同源要求）。
func TestHandlerSurvivesFailedPlugin(t *testing.T) {
	c := &stubCollector{entries: []Entry{
		{ID: "broken", Backend: "proc", State: loader.StateFailed, LastError: "exit code 7"},
	}}
	code, resp := getJSON(t, Handler(c), http.MethodGet)
	if code != http.StatusOK {
		t.Fatalf("Failed 插件不应导致端点失败，状态码 %d", code)
	}
	if resp.Plugins[0].LastError != "exit code 7" {
		t.Errorf("LastError 丢失: %+v", resp.Plugins[0])
	}
}

// 采集器自身 panic 时端点不能崩（约束 B5）。
func TestHandlerRecoversFromCollectorPanic(t *testing.T) {
	c := &stubCollector{panics: true}
	code, _ := getJSON(t, Handler(c), http.MethodGet)
	if code != http.StatusInternalServerError {
		t.Fatalf("采集器 panic 时应返回 500，实际 %d", code)
	}
}

// 端点必须只读。
func TestHandlerRejectsNonGET(t *testing.T) {
	h := Handler(&stubCollector{})
	for _, m := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		code, _ := getJSON(t, h, m)
		if code != http.StatusMethodNotAllowed {
			t.Errorf("%s 应返回 405，实际 %d", m, code)
		}
	}
}

// 输出必须有界：插件数超限时截断并标注。
func TestHandlerTruncatesExcessiveEntries(t *testing.T) {
	entries := make([]Entry, maxEntries+50)
	for i := range entries {
		entries[i] = Entry{ID: "p", State: loader.StateReady}
	}
	code, resp := getJSON(t, Handler(&stubCollector{entries: entries}), http.MethodGet)
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d", code)
	}
	if !resp.Truncated {
		t.Error("超限时应标记 Truncated")
	}
	if resp.Count != maxEntries {
		t.Errorf("Count = %d, 期望截断到 %d", resp.Count, maxEntries)
	}
}

// 关键回归：Total=0 时平均耗时必须是 0 而不是 NaN
// （json.Marshal 对 NaN 报错，会让整个端点 500）。
func TestMetricsZeroTotalHasNoNaN(t *testing.T) {
	m := metricsOf(safecall.NewCaller("p", safecall.Options{}, nil))
	if m.Total != 0 {
		t.Fatalf("Total = %d, 期望 0", m.Total)
	}
	if m.AvgLatencyMicros != 0 {
		t.Errorf("未调用过时平均耗时必须为 0，实际 %v（若为 NaN 说明未防住）", m.AvgLatencyMicros)
	}
	// 端到端：把这种 entry 放进响应，确认能正常序列化
	c := &stubCollector{entries: []Entry{{ID: "fresh", State: loader.StateReady, Metrics: m}}}
	code, resp := getJSON(t, Handler(c), http.MethodGet)
	if code != http.StatusOK {
		t.Fatalf("零调用插件不应导致 500，实际 %d", code)
	}
	if resp.Plugins[0].Metrics.AvgLatencyMicros != 0 {
		t.Errorf("序列化后平均耗时异常: %v", resp.Plugins[0].Metrics.AvgLatencyMicros)
	}
}

func TestMetricsOfRealCaller(t *testing.T) {
	c := safecall.NewCaller("p", safecall.Options{}, nil)
	for range 5 {
		_ = c.Invoke(context.Background(), time.Second, func(context.Context) error { return nil })
	}
	m := metricsOf(c)
	if m.Total != 5 {
		t.Errorf("Total = %d, 期望 5", m.Total)
	}
	if m.AvgLatencyMicros <= 0 {
		t.Errorf("有调用时平均耗时应 > 0，实际 %v", m.AvgLatencyMicros)
	}
}

func TestServerStartsAndShutsDown(t *testing.T) {
	srv := NewServer("127.0.0.1:0", &stubCollector{entries: []Entry{{ID: "a"}}})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	// 等待监听就绪：轮询 Addr 非空后发请求。
	var resp *http.Response
	var err error
	for range 100 {
		if srv.Addr() != "" {
			resp, err = http.Get("http://" + srv.Addr() + "/plugins")
			if err == nil {
				break
			}
			if strings.Contains(err.Error(), "operation not permitted") {
				cancel()
				t.Skipf("跳过网络请求测试: 当前环境限制网络连接: %v", err)
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		cancel()
		t.Fatalf("状态端点未能响应: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("优雅关闭返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 Server 未退出（可能泄漏 goroutine）")
	}
}

// LoaderCollector：未实现 Inspect 的后端必须被当作"无详情"而不是报错。
func TestCollectorToleratesLoaderWithoutInspect(t *testing.T) {
	lc := NewLoaderCollector(0, &Source{
		Loader: &noopLoader{},
		Desc: map[string]loader.Descriptor{
			"plain": {ID: "plain", Backend: loader.BackendProc, Kind: "notifier"},
		},
	})
	got := lc.Plugins()
	if len(got) != 1 {
		t.Fatalf("应采集到 1 个插件，实际 %d", len(got))
	}
	if got[0].State != loader.StateReady {
		t.Errorf("无 Inspect 时应给默认状态，实际 %q", got[0].State)
	}
}

// Inspect 返回错误时只影响该插件，不应丢掉整个列表。
func TestCollectorKeepsGoingOnInspectError(t *testing.T) {
	lc := NewLoaderCollector(0, &Source{
		Loader: &errInspectLoader{},
		Desc: map[string]loader.Descriptor{
			"ok":    {ID: "ok", Backend: loader.BackendProc},
			"bad":   {ID: "bad", Backend: loader.BackendProc},
			"other": {ID: "other", Backend: loader.BackendProc},
		},
	})
	got := lc.Plugins()
	if len(got) != 3 {
		t.Fatalf("Inspect 失败不应丢掉其他插件，实际 %d/3", len(got))
	}
	byID := map[string]Entry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID["bad"].LastError == "" {
		t.Error("Inspect 失败的插件应记录 LastError")
	}
	if byID["ok"].LastError != "" {
		t.Errorf("正常插件不应有 LastError: %q", byID["ok"].LastError)
	}
}

// 缓存：短 TTL 内不应重复采集；Invalidate 后应重新采集。
//
// 用计数型 Loader 验证真实行为，而不是靠 sleep 猜。
type countingLoader struct {
	noopLoader
	inspectCalls atomic.Int64
}

func (c *countingLoader) Inspect(context.Context, string) (loader.Info, error) {
	c.inspectCalls.Add(1)
	return loader.Info{State: loader.StateReady}, nil
}

func TestCollectorCachesAndInvalidates(t *testing.T) {
	cl := &countingLoader{}
	lc := NewLoaderCollector(time.Hour, &Source{
		Loader: cl,
		Desc:   map[string]loader.Descriptor{"a": {ID: "a", Backend: loader.BackendProc}},
	})

	lc.Plugins()
	lc.Plugins()
	lc.Plugins()
	if n := cl.inspectCalls.Load(); n != 1 {
		t.Errorf("TTL 内应只采集 1 次，实际 %d 次", n)
	}

	lc.Invalidate()
	lc.Plugins()
	if n := cl.inspectCalls.Load(); n != 2 {
		t.Errorf("Invalidate 后应重新采集，实际调用 %d 次", n)
	}
}

// noopLoader 是最小 Loader 实现（不支持任何操作）。
type noopLoader struct{}

func (n *noopLoader) Kind() loader.BackendKind { return loader.BackendProc }
func (n *noopLoader) Load(context.Context, loader.Descriptor) (loader.Instance, error) {
	return nil, errors.New("unsupported")
}
func (n *noopLoader) Unload(context.Context, string) error { return errors.New("unsupported") }
func (n *noopLoader) Watch(context.Context) (<-chan loader.ReloadEvent, error) {
	return nil, errors.New("unsupported")
}

// errInspectLoader 的 Inspect 对 bad 报错，其余正常。
type errInspectLoader struct{ noopLoader }

func (e *errInspectLoader) Inspect(_ context.Context, id string) (loader.Info, error) {
	if id == "bad" {
		return loader.Info{}, errors.New("boom")
	}
	return loader.Info{State: loader.StateReady, Version: "v1"}, nil
}

func TestMuxRegistersAndCustomPattern(t *testing.T) {
	mux := Mux(nil, "/internal/plugins", &stubCollector{})
	req := httptest.NewRequest(http.MethodGet, "/internal/plugins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("自定义路径应可用，状态码 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"plugins\"") {
		t.Errorf("响应体缺少 plugins 字段: %s", rec.Body.String())
	}
}
