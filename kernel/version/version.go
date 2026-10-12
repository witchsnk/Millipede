// Package version 实现 API 版本双向协商（约束 A4）。
//
// 规则：插件上报自身 APIVersion；宿主为每个扩展点声明支持的 [min, max]。
// 不匹配则拒绝注册并给出明确原因，而不是运行到一半 panic（见 docs/04 风险清单）。
package version

import (
	"fmt"

	"github.com/millipede/millipede/sdk/contract"
)

// Range 表示宿主对某扩展点支持的版本区间（含端点）。
type Range struct {
	Min contract.APIVersion
	Max contract.APIVersion
}

// Check 校验插件版本 plugin 是否落在宿主支持区间内。
// 返回 nil 表示兼容；否则返回带明确原因的错误。
func Check(point string, r Range, plugin contract.APIVersion) error {
	if less(plugin, r.Min) {
		return fmt.Errorf("version: extension point %q requires >= %s, plugin is %s (plugin too old)",
			point, r.Min.String(), plugin.String())
	}
	if greater(plugin, r.Max) {
		return fmt.Errorf("version: extension point %q supports <= %s, plugin is %s (host too old, upgrade host)",
			point, r.Max.String(), plugin.String())
	}
	// Major 必须一致：Major 变更即破坏性变更（约束 A2）。
	if plugin.Major != r.Max.Major {
		return fmt.Errorf("version: extension point %q major mismatch: host supports major %d, plugin is major %d",
			point, r.Max.Major, plugin.Major)
	}
	return nil
}

// less a<b（按 Major 再 Minor）。
func less(a, b contract.APIVersion) bool {
	if a.Major != b.Major {
		return a.Major < b.Major
	}
	return a.Minor < b.Minor
}

func greater(a, b contract.APIVersion) bool { return less(b, a) }
