package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/millipede/millipede/kernel/integrity"
)

// buildPlugin 编译测试辅助插件二进制（每次测试编译，保证与当前代码一致）。
func buildPlugin(t *testing.T, pkg string, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./"+pkg)
	cmd.Dir = "."
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// buildTestPlugin 编译常规测试插件。
func buildTestPlugin(t *testing.T) string {
	return buildPlugin(t, "testplugin", "testplugin")
}

// buildRestartPlugin 编译崩溃/重启测试插件。
func buildRestartPlugin(t *testing.T) string {
	return buildPlugin(t, "restartplugin", "restartplugin")
}

// pluginEnv 构造带场景变量与校验和的子进程环境。
func pluginEnv(scenario string) []string {
	return append(os.Environ(), "RESTART_SCENARIO="+scenario)
}

// integrityOff 关闭产物校验（重启/崩溃类用例不关注校验）。
func integrityOff() integrity.Policy {
	return integrity.Off()
}
