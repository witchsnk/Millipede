//go:build linux

package quota

import (
	"fmt"
	"syscall"

	"github.com/millipede/millipede/sdk/contract"
)

// BuildLimits 返回 RLIMIT_* 常量与值，供子进程在 exec 前调用 setrlimit。
//
// 关键坑（务必保留，这三点决定了这个方案的形状）：
//
//  1. os/exec **不提供** 为子进程设置 rlimit 的钩子。
//     Go 的 syscall.SysProcAttr 在 linux 与 darwin 上都没有 Rlimit 字段
//     （linux 只有 Setsid/Setpgid/Pgid/Credential 等）。
//     因此无法在父进程侧"给即将 exec 的子进程"设 rlimit。
//
//  2. 唯一可行的方式是让**子进程自己在 exec 前**调用 setrlimit(2)。
//     即：父进程通过环境变量把限制传给子进程，子进程 main() 第一件事
//     就是套用限制，之后任何内存分配都已在约束内。
//     这比"fork 后 exec 前由父进程设置"更晚，但仍然早于用户代码运行，
//     对"插件自身疯狂分配内存/死循环烧 CPU"是有效的。
//
//  3. RLIMIT_AS（地址空间）会连带影响 Go runtime 的 mmap 虚拟地址预留。
//     Go runtime 会预留大量虚拟地址（arena），设置过低的 AS 上限会让插件
//     在**启动阶段**就 "runtime: out of memory"，即使它实际只用了几 MB。
//     因此 AS 缺省为 0（不限制），只有显式配置才生效——
//     这是"设错比重设更糟"的典型例子。
//
//  4. RLIMIT_CPU：soft 到期发 SIGXCPU（可捕获），hard 到期发 SIGKILL。
//     惯例 hard = soft*2。父进程若想直接强杀，可只设 soft 并让插件默认
//     不捕获 SIGXCPU。
//
// 对应约束 C1。缺省不设 CPU/内存上限不等于没有约束：
// QPS 限流与调用次数预算是始终生效的有界默认值（见 quota.go）。
func BuildLimits(l Limits) []contract.RLimit {
	l = l.WithDefaults()
	var out []contract.RLimit

	if l.CPUSeconds > 0 {
		soft := uint64(l.CPUSeconds)
		out = append(out, contract.RLimit{
			Resource: contract.RLimitCPU,
			Soft:     soft,
			Hard:     soft * 2,
		})
	}
	if l.AddressSpaceBytes > 0 {
		v := uint64(l.AddressSpaceBytes)
		out = append(out, contract.RLimit{
			Resource: contract.RLimitAS,
			Soft:     v,
			Hard:     v,
		})
	}
	if l.MaxProcs > 0 {
		v := uint64(l.MaxProcs)
		out = append(out, contract.RLimit{
			Resource: contract.RLimitNPROC,
			Soft:     v,
			Hard:     v,
		})
	}
	return out
}

// PlatformSupported 报告当前平台是否支持进程级 rlimit。
func PlatformSupported() bool { return true }

// EncodeEnv 把限制编码为可放入子进程环境变量的字符串。
// 格式：MILLIPEDE_RLIMITS=RESOURCE:SOFT:HARD,RESOURCE:SOFT:HARD
func EncodeEnv(l Limits) string {
	rl := BuildLimits(l)
	if len(rl) == 0 {
		return ""
	}
	buf := make([]byte, 0, len(rl)*24)
	for i, r := range rl {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, r.Resource...)
		buf = append(buf, ':')
		buf = appendUint(buf, r.Soft)
		buf = append(buf, ':')
		buf = appendUint(buf, r.Hard)
	}
	return string(buf)
}

func appendUint(b []byte, v uint64) []byte {
	if v == 0 {
		return append(b, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(b, tmp[i:]...)
}

// ParseEnv 解析 EncodeEnv 的输出。
func ParseEnv(s string) ([]contract.RLimit, error) {
	if s == "" {
		return nil, nil
	}
	var out []contract.RLimit
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != ',' {
			continue
		}
		item := s[start:i]
		start = i + 1
		if item == "" {
			continue
		}
		res, rest, ok := cutByte(item, ':')
		if !ok {
			return nil, fmt.Errorf("quota: bad rlimit spec %q", item)
		}
		softStr, hardStr, ok := cutByte(rest, ':')
		if !ok {
			return nil, fmt.Errorf("quota: bad rlimit spec %q", item)
		}
		soft, err := parseUint(softStr)
		if err != nil {
			return nil, fmt.Errorf("quota: bad rlimit soft %q: %w", softStr, err)
		}
		hard, err := parseUint(hardStr)
		if err != nil {
			return nil, fmt.Errorf("quota: bad rlimit hard %q: %w", hardStr, err)
		}
		out = append(out, contract.RLimit{Resource: string(res), Soft: soft, Hard: hard})
	}
	return out, nil
}

func cutByte(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}

// selfRLimitSetter 是可替换的系统调用点，便于测试与跨平台。
var selfRLimitSetter = syscall.Setrlimit

// SelfApply 供子进程在 main 开头调用，套用父进程下发的限制。
//
// 返回生效的限制条数；解析失败时返回错误（**不静默忽略**——
// 静默忽略会让人以为限制生效了，正是约束 C1 要防的情况）。
func SelfApply(limits []contract.RLimit) (int, error) {
	applied := 0
	for _, r := range limits {
		res, err := resolveResource(r.Resource)
		if err != nil {
			return applied, err
		}
		if err := selfRLimitSetter(res, &syscall.Rlimit{Cur: r.Soft, Max: r.Hard}); err != nil {
			// EPERM 表示试图抬高硬限制（普通用户进程不允许）。
			// 这类情况应如实报错，而不是假装成功。
			return applied, fmt.Errorf("quota: setrlimit %s: %w", r.Resource, err)
		}
		applied++
	}
	return applied, nil
}

func resolveResource(name string) (int, error) {
	switch name {
	case contract.RLimitCPU:
		return syscall.RLIMIT_CPU, nil
	case contract.RLimitAS:
		return syscall.RLIMIT_AS, nil
	case contract.RLimitNPROC:
		return rlimitNPROC, nil
	default:
		return 0, fmt.Errorf("quota: unknown rlimit resource %q", name)
	}
}

// rlimitNPROC 是 RLIMIT_NPROC 的资源编号。
//
// Go 的 syscall 包在 linux 各架构上都没有导出 RLIMIT_NPROC
// （只导出 CPU=0/FSIZE=1/DATA=2/STACK=3/CORE=4/RSS=5/NPROC=6 的子集，
// 见 zerrors_linux_*.go）。此处按 Linux 内核 ABI 硬编码为 6。
//
// 该值来自 <asm-generic/resource.h>，在 Linux 所有架构上都是 6，
// 属于稳定的内核 ABI，不随版本变化。
const rlimitNPROC = 6
