package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginLoader(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建测试插件子目录和文件
	subDir := filepath.Join(tmpDir, "demo-plugin")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `package demo
//plugin:export name="demo-plugin" api=1.0 backend=builtin extension=notifier
`
	if err := os.WriteFile(filepath.Join(subDir, "main.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	loader := NewPluginLoader(func(o *PluginLoaderOptions) {
		o.PluginDir = tmpDir
	})

	if err := loader.DiscoverAndLoad(); err != nil {
		t.Fatalf("DiscoverAndLoad failed: %v", err)
	}

	plugins := loader.ListPlugins()
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name != "demo-plugin" {
		t.Errorf("expected plugin name demo-plugin, got %s", plugins[0].Name)
	}

	loaded := loader.GetLoadedPlugins()
	if _, ok := loaded["demo-plugin"]; !ok {
		t.Errorf("expected demo-plugin in loaded plugins")
	}

	if err := loader.UnloadPlugin("demo-plugin"); err != nil {
		t.Errorf("UnloadPlugin failed: %v", err)
	}

	if len(loader.GetLoadedPlugins()) != 0 {
		t.Errorf("expected 0 loaded plugins after unload")
	}
}
