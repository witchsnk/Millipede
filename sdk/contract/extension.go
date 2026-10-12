// Package contract 定义插件作者唯一依赖的契约层。
// 本包必须独立 module、独立版本，且只增不改（见 docs/03-约束与门禁.md A2）。
package contract

import "context"

// APIVersion 语义化版本，仅 Major.Minor。
// Major 不一致即不兼容；Minor 仅向后兼容地新增。
type APIVersion struct {
	Major int
	Minor int
}

// String 返回 "Major.Minor" 形式。
func (v APIVersion) String() string {
	return itoa(v.Major) + "." + itoa(v.Minor)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// Extension 是所有扩展点的公共契约。
// 实现者必须提供稳定的名字与自身声明的 API 版本。
type Extension interface {
	Name() string
	APIVersion() APIVersion
}

// Message 是一个示例跨边界消息。
// 约束 A5：跨边界参数/返回值只能是可序列化值类型，
// 禁止指针、禁止含未导出字段、禁止 any 携带宿主类型。
type Message struct {
	ID      string
	Topic   string
	Payload []byte
	Headers map[string]string
}

// SendResult 发送结果。
type SendResult struct {
	Accepted bool
	Detail   string
}

// Notifier 是一个具体扩展点示例。
// 约束 A3：所有扩展方法必须带 ctx 并返回 error。
type Notifier interface {
	Extension
	Send(ctx context.Context, msg Message) (SendResult, error)
}

// BatchNotifier 是"可选能力"的示例（约束 A6：大接口拆小接口）。
// 宿主通过类型断言探测插件是否支持批量，而不是把方法塞进 Notifier。
type BatchNotifier interface {
	Extension
	SendBatch(ctx context.Context, msgs []Message) ([]SendResult, error)
}
