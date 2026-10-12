package safecall

import (
	"context"
	"testing"
)

// BenchmarkInvoke 测量 safecall 边界的开销（运行效率验证）。
func BenchmarkInvoke(b *testing.B) {
	c := NewCaller("bench", Options{MaxConcurrency: 256}, nil)
	ctx := context.Background()
	fn := func(ctx context.Context) error { return nil }
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := c.Invoke(ctx, 0, fn); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRawCall 基线：无保护的直接调用。
func BenchmarkRawCall(b *testing.B) {
	fn := func(ctx context.Context) error { return nil }
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = fn(ctx)
		}
	})
}
