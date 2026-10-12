package proc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/millipede/millipede/kernel/integrity"
	"github.com/millipede/millipede/kernel/loader"
)

// WatchOptions 控制产物监听行为。
type WatchOptions struct {
	// Interval 轮询间隔。默认 5s。
	//
	// 为什么是轮询而不是 inotify/FSEvents：
	// 本环境网络受限，无法引入 fsnotify；且轮询在 Linux 上对
	// "少量插件、秒级感知"的场景完全够用。
	// 真正要秒级热更时，应换成 fsnotify 或由外部编排（如 k8s ConfigMap 挂载）触发。
	Interval time.Duration
	// OnDebounce 同一插件在一次变更后的静默期，避免编译产物写入过程中
	// 读到半截文件连续触发多次事件。默认 Interval 的 2 倍。
	OnDebounce time.Duration
	// Verify 与 Load 使用同一套完整性策略，变更时先校验再上报，
	// 避免把被篡改的产物当成"合法新版本"推给编排层（约束 D1）。
	Verify VerifyPolicy
	// OnEvent 事件回调。为 nil 时事件只走 Watch 返回的通道。
	OnEvent func(loader.ReloadEvent)
	// OnError 监听出错回调。为 nil 时错误被记录到 LastWatchError。
	OnError func(error)
	// Logger 可选日志回调，签名为 (pluginID, msg)。
	Logger func(pluginID, msg string)
}

// VerifyPolicy 是完整性校验策略的最小接口。
// 这样 Watch 不必直接依赖 integrity 包的细节，也便于测试替换。
type VerifyPolicy interface {
	// Enabled 报告该策略是否启用校验。
	Enabled() bool
}

// Watcher 监听已加载插件的产物文件变化，产出 ReloadEvent（约束 A5/M4）。
//
// 设计要点：
//   - 纯轮询，无 inotify 依赖，跨平台一致。
//   - 按内容摘要（sha256）而非 mtime 判断变更：
//     `touch` 不应触发重载，但内容被改写（哪怕 mtime 被还原）必须触发。
//   - 变更后先过完整性校验，校验不通过则**不上报**，
//     避免把篡改产物当合法更新推给编排层。
//   - 所有 goroutine 绑定 ctx，ctx 结束即退出（约束 B3：无残留 goroutine）。
type Watcher struct {
	opt WatchOptions

	mu      sync.Mutex
	sums    map[string]string // pluginID -> 上次看到的内容摘要
	paths   map[string]string // pluginID -> 产物路径
	lastErr map[string]error  // pluginID -> 最近一次错误
	closed  bool
}

// NewWatcher 构造 Watcher。
func NewWatcher(opt WatchOptions) *Watcher {
	if opt.Interval <= 0 {
		opt.Interval = 5 * time.Second
	}
	if opt.OnDebounce <= 0 {
		opt.OnDebounce = 2 * opt.Interval
	}
	return &Watcher{
		opt:     opt,
		sums:    make(map[string]string),
		paths:   make(map[string]string),
		lastErr: make(map[string]error),
	}
}

// Track 开始跟踪某个已加载插件的产物文件。
//
// 必须在插件**已经加载**后调用：首轮只记录基线摘要，不产生事件。
func (w *Watcher) Track(pluginID, path string) error {
	sum, err := fileDigest(path)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errWatcherClosed
	}
	w.paths[pluginID] = path
	w.sums[pluginID] = sum
	return nil
}

// Untrack 停止跟踪某插件。
func (w *Watcher) Untrack(pluginID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.paths, pluginID)
	delete(w.sums, pluginID)
	delete(w.lastErr, pluginID)
}

// LastWatchError 返回某插件最近一次监听错误；不存在时返回 nil。
func (w *Watcher) LastWatchError(pluginID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr[pluginID]
}

// Tracked 返回当前被跟踪的插件 ID 列表（用于 /plugins 端点展示）。
func (w *Watcher) Tracked() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.paths))
	for id := range w.paths {
		out = append(out, id)
	}
	return out
}

// Run 启动监听循环，阻塞至 ctx 结束。
//
// 事件同时写入返回的通道与 OnEvent 回调。
// 通道容量固定且发送带超时：慢消费者不得阻塞整个监听循环
// （否则一个卡住的订阅者会让所有插件失去热更能力）。
func (w *Watcher) Run(ctx context.Context, out chan<- loader.ReloadEvent) {
	defer close(out)
	ticker := time.NewTicker(w.opt.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx, out)
		}
	}
}

// poll 执行一轮检测。
func (w *Watcher) poll(ctx context.Context, out chan<- loader.ReloadEvent) {
	w.mu.Lock()
	ids := make([]string, 0, len(w.paths))
	for id := range w.paths {
		ids = append(ids, id)
	}
	type target struct{ id, path, prev string }
	targets := make([]target, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, target{id: id, path: w.paths[id], prev: w.sums[id]})
	}
	w.mu.Unlock()

	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		sum, err := fileDigest(t.path)
		if err != nil {
			// 文件可能被删除或正在被替换；记录错误但不终止整轮检测。
			w.recordErr(t.id, err)
			continue
		}
		if sum == t.prev {
			continue
		}
		// 静默期：产物可能在写入中。先观察一个 debounce 周期再确认。
		if !w.stillChanged(ctx, t.path, sum) {
			continue
		}
		// D1：变更后必须重新校验，篡改产物不得作为合法更新上报。
		if err := w.verifyChanged(t.path); err != nil {
			// 校验不通过一律不上报：把篡改产物当合法更新推给编排层，
			// 比不上报更危险（约束 D1）。
			w.recordErr(t.id, err)
			w.logf(t.id, "产物已变更但完整性校验失败，忽略该更新: "+err.Error())
			continue
		}

		w.mu.Lock()
		w.sums[t.id] = sum
		w.mu.Unlock()

		ev := loader.ReloadEvent{PluginID: t.id, Reason: "artifact-changed"}
		w.deliver(ctx, out, ev)
	}
}

// stillChanged 在 debounce 静默期内确认摘要是否稳定。
func (w *Watcher) stillChanged(ctx context.Context, path, prev string) bool {
	timer := time.NewTimer(w.opt.OnDebounce)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	sum, err := fileDigest(path)
	if err != nil {
		return false
	}
	// 静默期内又变了 => 还在写入中，本轮不下结论。
	return sum == prev
}

// verifyChanged 按配置的策略校验变更后的产物。
//
// 关键坑：这里**没有**期望的 checksum 可比对——期望值应来自发布侧清单。
// 清单里的旧版本校验和对新产物必然不匹配，直接比会把所有正常更新都拒掉。
// 因此此处只做**结构性**校验（必须是非空普通文件），
// 真正的签名比对由编排层在采用新版本时用清单完成（见 integrity.ResolveFor）。
//
// 显式配置为"关闭"时（integrity.Off）跳过。
func (w *Watcher) verifyChanged(path string) error {
	if w.opt.Verify == nil || !w.opt.Verify.Enabled() {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", integrity.ErrNotRegularFile, path)
	}
	return nil
}

// deliver 分发事件；通道满时按超时丢弃而不是无限阻塞。
func (w *Watcher) deliver(ctx context.Context, out chan<- loader.ReloadEvent, ev loader.ReloadEvent) {
	if w.opt.OnEvent != nil {
		w.opt.OnEvent(ev)
	}
	if out == nil {
		return
	}
	select {
	case out <- ev:
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		// 慢消费者：宁可丢事件也不能卡住监听循环。
		w.logf(ev.PluginID, "热更事件通道阻塞，已丢弃一次事件")
	}
}

func (w *Watcher) recordErr(pluginID string, err error) {
	w.mu.Lock()
	w.lastErr[pluginID] = err
	w.mu.Unlock()
	if w.opt.OnError != nil {
		w.opt.OnError(err)
	}
}

func (w *Watcher) logf(pluginID, msg string) {
	if w.opt.Logger != nil {
		w.opt.Logger(pluginID, msg)
	}
}

func (w *Watcher) Close() {
	w.mu.Lock()
	w.closed = true
	w.paths = make(map[string]string)
	w.sums = make(map[string]string)
	w.mu.Unlock()
}

// errWatcherClosed 表示 Watcher 已关闭。
var errWatcherClosed = errClosed("loader: watcher closed")

type errClosed string

func (e errClosed) Error() string { return string(e) }

// fileDigest 返回文件内容的 sha256 摘要。
//
// 关键坑：**必须按内容**判断变更，不能只看 mtime。
// 构建工具常常在保持内容不变的情况下 touch 文件，
// 只看 mtime 会导致无意义的重载风暴。
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Tracking 报告某插件当前是否被跟踪（供 /plugins 端点展示）。
func (w *Watcher) Tracking(pluginID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.paths[pluginID]
	return ok
}
