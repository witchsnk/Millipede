// Package host 是插件侧门面：为插件提供日志、配置、上下文、后台任务监督等能力，
// 屏蔽底层后端（builtin/proc）的差异。插件只能依赖 sdk/contract 与 sdk/host。
package host

import (
	"context"
	"log/slog"
)

// Logger 是面向插件的结构化日志门面。
// 框架会自动注入 plugin_id 标签（约束 E1）。
type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
}

// Config 是配置只读视图（约束 E3：配置由框架统一注入）。
type Config interface {
	Get(key string) (any, bool)
	GetString(key, def string) string
	GetInt(key string, def int) int
}

// Env 在插件 Init 阶段注入的运行环境。
type Env struct {
	// PluginID 当前插件的唯一标识，框架自动注入。
	PluginID string
	// Logger 带 plugin_id 的日志门面。
	Logger Logger
	// Config 该插件的配置视图（已按插件隔离）。
	Config Config
}

// Log 从上下文中取出带 plugin_id 的 logger；若不存在则返回一个安全的空实现。
// 这样插件代码可以统一写作 host.Log(ctx).Info(...) 而不必判空。
func Log(ctx context.Context) Logger {
	if l, ok := ctx.Value(loggerKey{}).(Logger); ok && l != nil {
		return l
	}
	return nopLogger{}
}

type loggerKey struct{}

// WithLogger 把 logger 放入 context（框架内部使用）。
func WithLogger(ctx context.Context, l Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// nopLogger 空实现，保证插件在缺失门面时也不会 panic。
type nopLogger struct{}

func (nopLogger) Debug(context.Context, string, ...any) {}
func (nopLogger) Info(context.Context, string, ...any)  {}
func (nopLogger) Warn(context.Context, string, ...any)  {}
func (nopLogger) Error(context.Context, string, ...any) {}

// SlogLogger 把 *slog.Logger 适配为门面（框架内部使用）。
type SlogLogger struct {
	base *slog.Logger
}

// NewSlogLogger 构造一个注入固定属性的门面。
func NewSlogLogger(base *slog.Logger, pluginID string) *SlogLogger {
	return &SlogLogger{base: base.With(slog.String("plugin_id", pluginID))}
}

func (l *SlogLogger) Debug(ctx context.Context, msg string, kv ...any) {
	l.base.LogAttrs(ctx, slog.LevelDebug, msg, toAttrs(kv)...)
}
func (l *SlogLogger) Info(ctx context.Context, msg string, kv ...any) {
	l.base.LogAttrs(ctx, slog.LevelInfo, msg, toAttrs(kv)...)
}
func (l *SlogLogger) Warn(ctx context.Context, msg string, kv ...any) {
	l.base.LogAttrs(ctx, slog.LevelWarn, msg, toAttrs(kv)...)
}
func (l *SlogLogger) Error(ctx context.Context, msg string, kv ...any) {
	l.base.LogAttrs(ctx, slog.LevelError, msg, toAttrs(kv)...)
}

func toAttrs(kv []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		attrs = append(attrs, slog.Any(key, kv[i+1]))
	}
	return attrs
}
