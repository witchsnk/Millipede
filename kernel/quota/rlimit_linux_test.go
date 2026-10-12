//go:build linux

package quota

import (
	"testing"

	"github.com/witchsnk/millipede/sdk/contract"
)

func TestEncodeParseEnvRoundTrip(t *testing.T) {
	l := Limits{CPUSeconds: 30, AddressSpaceBytes: 2147483648, MaxProcs: 64}
	env := EncodeEnv(l)
	if env == "" {
		t.Fatal("配置了进程级限制时编码结果不应为空")
	}
	got, err := ParseEnv(env)
	if err != nil {
		t.Fatalf("解析编码结果失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应还原 3 条限制，实际 %d: %+v", len(got), got)
	}

	byName := map[string]contract.RLimit{}
	for _, r := range got {
		byName[r.Resource] = r
	}
	cpu := byName[contract.RLimitCPU]
	if cpu.Soft != 30 || cpu.Hard != 60 {
		t.Errorf("cpu 限制 = soft:%d hard:%d, 期望 30/60", cpu.Soft, cpu.Hard)
	}
	as := byName[contract.RLimitAS]
	if as.Soft != 2147483648 || as.Hard != 2147483648 {
		t.Errorf("as 限制 = soft:%d hard:%d", as.Soft, as.Hard)
	}
	np := byName[contract.RLimitNPROC]
	if np.Soft != 64 || np.Hard != 64 {
		t.Errorf("nproc 限制 = soft:%d hard:%d", np.Soft, np.Hard)
	}
}

// 未配置任何进程级限制时，环境变量应为空（不污染子进程环境）。
func TestEncodeEnvEmptyWhenUnset(t *testing.T) {
	if env := EncodeEnv(Limits{}); env != "" {
		t.Errorf("未配置进程级限制时应编码为空，实际 %q", env)
	}
}

func TestParseEnvErrors(t *testing.T) {
	cases := map[string]string{
		"缺 hard":   "cpu:30",
		"格式错":      "cpu",
		"非数字 soft": "cpu:abc:60",
		"非数字 hard": "cpu:30:xyz",
	}
	for name, in := range cases {
		if _, err := ParseEnv(in); err == nil {
			t.Errorf("%s: 输入 %q 应报错", name, in)
		}
	}
	if _, err := ParseEnv(""); err != nil {
		t.Errorf("空串应解析为空且不报错，实际 %v", err)
	}
}

// 子进程必须真的能套用限制——用真 setrlimit 验证，而不是只测编码。
func TestSelfApplyRealRlimit(t *testing.T) {
	if !PlatformSupported() {
		t.Skip("当前平台不支持进程级 rlimit")
	}
	// 抬高硬限制需要特权，普通进程会 EPERM；
	// 因此这里用一个"必然成功且不会伤到测试进程"的方式验证 API 可用：
	// 对 NPROC 设置当前值（不抬高），成功即说明解析与调用链正确。
	applied, err := SelfApply([]contract.RLimit{
		{Resource: contract.RLimitNPROC, Soft: 1024, Hard: 1024},
	})
	if err != nil {
		t.Fatalf("套用 NPROC 限制失败: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, 期望 1", applied)
	}
}

// 未知资源名必须报错，不能静默忽略。
func TestSelfApplyUnknownResource(t *testing.T) {
	if _, err := SelfApply([]contract.RLimit{{Resource: "no-such-resource"}}); err == nil {
		t.Error("未知资源名应报错")
	}
}
