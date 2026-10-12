// Command host 是 Millipede 框架的宿主可执行文件示例：
// 演示契约加载、版本协商、builtin + proc 双后端、生命周期编排与统一调用边界。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/millipede/millipede/kernel/config"
	"github.com/millipede/millipede/kernel/integrity"
	"github.com/millipede/millipede/kernel/lifecycle"
	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/kernel/loader/builtin"
	"github.com/millipede/millipede/kernel/loader/proc"
	"github.com/millipede/millipede/kernel/observability"
	"github.com/millipede/millipede/kernel/safecall"
	"github.com/millipede/millipede/kernel/version"
	notifier "github.com/millipede/millipede/plugins/example-notifier"
	"github.com/millipede/millipede/sdk/contract"
	"github.com/millipede/millipede/sdk/host"
)

// procVersion 是示例插件版本；与清单键 pluginID@version 对应。
const procVersion = "1.0.0"

// pluginsAddr 返回插件状态端点监听地址；空串表示不开启（默认关闭）。
func pluginsAddr() string {
	if a := os.Getenv("HOST_PLUGINS_ADDR"); a != "" {
		return a
	}
	return flag.Lookup("plugins-addr").Value.String()
}

func main() {
	// 插件状态端点监听地址（约束 E2）；默认关闭。
	flag.String("plugins-addr", "", "插件状态端点监听地址，如 127.0.0.1:9099；空=关闭")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := host.WithLogger(context.Background(), host.NewSlogLogger(logger, "host"))

	// 1) 配置中心：为插件注入配置（约束 E3）。
	cfgStore := config.New()
	cfgStore.Set("example-notifier", "endpoint", "https://sms.internal/api")

	// 2) 版本协商（约束 A4）：宿主声明 notifier 扩展点支持区间。
	notifierRange := version.Range{
		Min: contract.APIVersion{Major: 1, Minor: 0},
		Max: contract.APIVersion{Major: 1, Minor: 9},
	}

	// 3) builtin 后端：注册并加载内置插件。
	// envFor 为每个插件注入隔离的 Config 视图与带 plugin_id 的 Logger（约束 E1/E3）。
	builtinLoader := builtin.New(safecall.Options{MaxConcurrency: 32}, func(pluginID string) host.Env {
		return host.Env{
			PluginID: pluginID,
			Logger:   host.NewSlogLogger(logger, pluginID),
			Config:   cfgStore.View(pluginID),
		}
	})
	if err := notifier.Register(builtinLoader); err != nil {
		logger.Error("register builtin plugin", "err", err)
		os.Exit(1)
	}

	builtinDesc := loader.Descriptor{
		ID:         "example-notifier",
		Kind:       "notifier",
		Backend:    loader.BackendBuiltin,
		APIVersion: contract.APIVersion{Major: 1, Minor: 0},
	}
	if err := version.Check("notifier", notifierRange, builtinDesc.APIVersion); err != nil {
		logger.Error("version negotiation failed", "err", err)
		os.Exit(1)
	}

	inst, err := builtinLoader.Load(ctx, builtinDesc)
	if err != nil {
		logger.Error("load builtin", "err", err)
		os.Exit(1)
	}
	// 编排生命周期：Init -> Start（约束 B1/B2）。
	bi, ok := inst.(interface{ Lifecycle() lifecycle.Lifecycle })
	if !ok {
		logger.Error("builtin instance missing lifecycle")
		os.Exit(1)
	}
	lm := lifecycle.NewManager()
	opt := lifecycle.DefaultOptions()
	if err := lm.Boot(ctx, opt, lifecycle.BootItem{Name: inst.ID(), LC: bi.Lifecycle()}); err != nil {
		logger.Error("boot builtin", "err", err)
		os.Exit(1)
	}

	// 4) 通过统一边界调用 builtin 插件。
	res, err := inst.Invoke(ctx, "Send", contract.Message{ID: "m1", Topic: "welcome", Payload: []byte("hello")})
	if err != nil {
		logger.Error("builtin invoke", "err", err)
	} else {
		logger.Info("builtin invoke ok", "result", fmt.Sprintf("%+v", res))
	}

	// 5) proc 后端：拉起子进程插件（隔离最强）。
	procPath := os.Getenv("PROC_NOTIFIER_PATH")
	if procPath == "" {
		procPath = "./bin/proc-notifier"
	}
	// 插件日志接入宿主日志系统（约束 E1）。
	proc.SetStderrSink(func(pluginID, line string) {
		logger.Warn("plugin stderr", "plugin_id", pluginID, "line", line)
	})
	// statusCollector 在下方开启状态端点时赋值；未开启时保持 nil（约束 E2）。
	var statusCollector *observability.LoaderCollector

	// 产物热更：开启监听后，产物变更会自动触发受控换代（约束 H1–H6）。
	// 关掉（不设置下面两个环境变量）则保持一次性演示行为。
	procOpt := proc.Options{
		WatchInterval: procWatchInterval(),
		OnWatchLog: func(pluginID, msg string) {
			logger.Warn("plugin watch", "plugin_id", pluginID, "msg", msg)
		},
		HandshakeTimeout: 5 * time.Second,
		InvokeTimeout:    5 * time.Second,
		MaxRestarts:      3,
		RestartBackoff:   500 * time.Millisecond,
		StabilityWindow:  time.Minute,
		HealthInterval:   time.Second,
		// D1：生产环境请使用 integrity.Strict(key) 同时校验签名。
		// 这里默认 Lenient（校验和），签名留作部署侧强化。
		Verify: integrity.Lenient(),
	}
	procLoader := proc.New(procOpt)
	// D1：期望的校验和/签名必须来自**可信清单**（发布侧生成），
	// 绝不能对将要校验的文件现场哈希——那是自证，永远等于自己，等于没校验。
	// 清单缺失即拒绝加载，而不是降级放行。
	pdesc := loader.Descriptor{
		ID:      "proc-notifier",
		Kind:    "notifier",
		Backend: loader.BackendProc,
		URI:     procPath,
		Version: procVersion,
	}
	manifestPath := os.Getenv("PROC_MANIFEST")
	if manifestPath == "" {
		manifestPath = "./bin/manifest.json"
	}
	// 关键：每一代都要**重新**从清单解析期望摘要。
	// 若只在启动时解析一次，热更会用"旧版本的摘要"去校验新产物，
	// 必然不匹配——表现为"产物明明换了，却永远热更失败"（本轮实测踩到）。
	resolveChecksum := func(d loader.Descriptor) (loader.Descriptor, bool) {
		sum, sig, merr := integrity.ResolveFor(manifestPath, d.ID, procVersion)
		if merr != nil {
			logger.Warn("refuse to load proc plugin: no trusted manifest entry",
				"err", merr, "manifest", manifestPath)
			return d, false
		}
		d.Checksum, d.Signature = sum, sig
		return d, true
	}
	loadedDesc, hasManifest := resolveChecksum(pdesc)

	var pinst loader.Instance
	if !hasManifest {
		logger.Warn("skip proc demo: integrity verification unavailable")
	} else if pinst, err = procLoader.Load(ctx, loadedDesc); err != nil {
		logger.Warn("load proc plugin refused", "err", err, "path", procPath)
	} else {
		msg, _ := json.Marshal(contract.Message{ID: "m2", Topic: "proc", Payload: []byte("hi")})
		pres, err := pinst.Invoke(ctx, "Send", msg)
		if err != nil {
			logger.Error("proc invoke", "err", err)
		} else {
			var sr contract.SendResult
			_ = json.Unmarshal(toBytes(pres), &sr)
			logger.Info("proc invoke ok", "accepted", sr.Accepted, "detail", sr.Detail)

			// 6) 插件状态端点（约束 E2）。默认关闭，用 -plugins-addr 或环境变量开启，
			// 保证 make demo 的输出保持干净。
			if addr := pluginsAddr(); addr != "" {
				known := map[string]loader.Descriptor{}
				if inst != nil {
					known[inst.ID()] = builtinDesc
				}
				if pinst != nil {
					known[pinst.ID()] = pdesc
				}
				// 每个后端一个 Source：Inspect 必须问对后端，
				// 否则查 proc 插件会问到 builtin 加载器上，必然 not loaded。
				collector := observability.NewLoaderCollector(500*time.Millisecond,
					&observability.Source{
						Loader: builtinLoader,
						Desc:   builtinOnly(known, loader.BackendBuiltin),
					},
					&observability.Source{
						Loader: procLoader,
						Desc:   builtinOnly(known, loader.BackendProc),
						Caller: procLoader.CallerFor,
					},
				)
				statusCollector = collector
				srv := observability.NewServer(addr, collector)
				srvCtx, stopSrv := context.WithCancel(ctx)
				srvDone := make(chan error, 1)
				go func() { srvDone <- srv.Start(srvCtx) }()
				// 等监听就绪，拿到真实地址（含内核分配的端口）。
				for range 200 {
					if a := srv.Addr(); a != "" {
						logger.Info("plugins endpoint listening", "addr", "http://"+a+"/plugins")
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				defer func() {
					stopSrv()
					if err := <-srvDone; err != nil {
						logger.Error("plugins endpoint shutdown", "err", err)
					}
				}()
			}
		}
	}

	// 消费热更事件：事件由 Watcher 产出，这里做实际的受控换代（H1–H6）。
	// 没有消费者时事件会被丢弃（Watcher 带超时、不阻塞），
	// 因此**必须**真的订阅，否则热更等于没开。
	if procOpt.WatchEnabled() {
		watchCh, werr := procLoader.Watch(ctx)
		if werr != nil {
			logger.Warn("watch unavailable", "err", werr)
		} else {
			logger.Info("plugin watch enabled",
				"plugin_id", pdesc.ID, "interval", procOpt.WatchInterval)
			go consumeReloadEvents(ctx, procLoader, watchCh, pdesc, resolveChecksum, logger)
		}
	}

	// 可选的"存活窗口"：让 /plugins 与产物热更都有可观察/可触发的时间。
	// 位置关键：必须在**端点已监听、监听器已订阅**之后，
	// 且在**卸载之前**——否则要么端点读到的全是"已卸载"，
	// 要么热更对着已回收的加载器跑（约束 E2/H1 的演示前提）。
	if d, err := time.ParseDuration(os.Getenv("HOST_PLUGINS_HOLD")); err == nil && d > 0 {
		logger.Info("holding", "duration", d.String())
		time.Sleep(d)
	}

	// 演示卸载（proc 支持卸载，builtin 不支持）。
	// 端点若已开启，需先失效缓存，使 /plugins 反映"已卸载"状态。
	_ = procLoader.Unload(ctx, "proc-notifier")
	if statusCollector != nil {
		statusCollector.Invalidate()
	}
	logger.Info("proc plugin unloaded")

	// 7) 优雅关闭（约束 B1：Stop 幂等 + 超时）。
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lm.Shutdown(shCtx, opt); err != nil {
		logger.Error("shutdown", "err", err)
		os.Exit(1)
	}
	logger.Info("host exited cleanly")
}

func toBytes(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	default:
		out, _ := json.Marshal(v)
		return out
	}
}

// builtinOnly 按后端过滤描述符表，避免把 builtin 插件交给 proc 加载器查询。
func builtinOnly(all map[string]loader.Descriptor, kind loader.BackendKind) map[string]loader.Descriptor {
	out := make(map[string]loader.Descriptor)
	for id, d := range all {
		if d.Backend == kind {
			out[id] = d
		}
	}
	return out
}

// procWatchInterval 返回产物监听轮询间隔；0 表示不开启产物热更。
func procWatchInterval() time.Duration {
	d, err := time.ParseDuration(os.Getenv("HOST_WATCH_INTERVAL"))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// consumeReloadEvents 订阅热更事件并执行受控换代（约束 H1–H6）。
//
// 关键设计：换代由**事件驱动**而不是轮询，
// 因为 Watcher 已经在做变更检测；这里只负责"收到事件 → 执行 Reap"。
//
// Reap 内部保证"新代先 Ready 再卸旧代"，新代失败则保留旧代继续服务（H3），
// 因此本循环即使遇到失败也只需记录日志并继续订阅，不应退出。
func consumeReloadEvents(
	ctx context.Context,
	l *proc.Loader,
	ch <-chan loader.ReloadEvent,
	next loader.Descriptor,
	// resolve 为该代重新解析期望摘要；返回 false 表示清单无该条目。
	resolve func(loader.Descriptor) (loader.Descriptor, bool),
	logger *slog.Logger,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			logger.Info("plugin artifact changed, hot reloading",
				"plugin_id", ev.PluginID, "reason", ev.Reason)
			// 每一代都重新解析期望摘要：清单在热更前可能已被发布流程更新。
			resolved, ok := resolve(next)
			if !ok {
				logger.Warn("skip hot reload: no trusted manifest entry",
					"plugin_id", ev.PluginID)
				continue
			}
			if err := l.Reap(ctx, ev.PluginID, resolved, ev.Reason); err != nil {
				// 失败不致命：旧代仍在服务，记日志后继续订阅。
				logger.Error("hot reload failed, keeping old generation",
					"plugin_id", ev.PluginID, "err", err)
				continue
			}
			logger.Info("hot reload done", "plugin_id", ev.PluginID)
		}
	}
}
