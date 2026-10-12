package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PluginInfo 插件基本信息
type PluginInfo struct {
	Name        string
	Description string
	Enabled     bool
}

// PluginLoader 插件加载器，支持自动发现
type PluginLoader struct {
	pluginDir     string
	loadedPlugins map[string]any
	pluginInfos   map[string]PluginInfo
	mu            sync.RWMutex
}

// NewPluginLoader 创建新的插件加载器
func NewPluginLoader(options ...func(*PluginLoaderOptions)) *PluginLoader {
	opts := &PluginLoaderOptions{
		PluginDir: "plugins",
	}

	for _, opt := range options {
		opt(opts)
	}

	return &PluginLoader{
		pluginDir:     opts.PluginDir,
		loadedPlugins: make(map[string]any),
		pluginInfos:   make(map[string]PluginInfo),
		mu:            sync.RWMutex{},
	}
}

// PluginLoaderOptions 配置插件加载器的选项
type PluginLoaderOptions struct {
	PluginDir string
}

// DiscoverAndLoad 自动发现并加载 plugins 目录下的所有插件
func (pl *PluginLoader) DiscoverAndLoad() error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	// 读取插件目录
	entries, err := os.ReadDir(pl.pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录不存在，创建它
			os.MkdirAll(pl.pluginDir, 0755)
			fmt.Printf("Created plugin directory: %s\n", pl.pluginDir)
			return nil
		}
		return fmt.Errorf("failed to read plugin directory %s: %w", pl.pluginDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// 递归扫描子目录
			subLoader := NewPluginLoader(func(o *PluginLoaderOptions) {
				o.PluginDir = filepath.Join(pl.pluginDir, entry.Name())
			})
			if err := subLoader.DiscoverAndLoad(); err != nil {
				fmt.Printf("Warning: failed to load subdirectory %s: %v\n", entry.Name(), err)
			} else {
				for k, v := range subLoader.loadedPlugins {
					pl.loadedPlugins[k] = v
				}
				for k, v := range subLoader.pluginInfos {
					pl.pluginInfos[k] = v
				}
			}
			continue
		}

		// 只处理 .go 文件
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		filePath := filepath.Join(pl.pluginDir, entry.Name())
		if err := pl.loadGoFile(filePath, entry.Name()); err != nil {
			fmt.Printf("Warning: failed to load %s: %v\n", entry.Name(), err)
		}
	}

	fmt.Printf("Discovered %d plugin configurations, loaded %d plugins\n",
		len(pl.pluginInfos), len(pl.loadedPlugins))

	return nil
}

// loadGoFile 加载单个 Go 文件并查找 plugin 注解
func (pl *PluginLoader) loadGoFile(filePath string, entryName string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	lines := strings.Split(string(content), "\n")

	var pluginName string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 查找 //plugin:export 注解
		if strings.HasPrefix(trimmed, "//plugin:export") {
			// 解析注解参数
			annotContent := strings.TrimSpace(trimmed[len("//plugin:export"):])

			// 解析 key=value 对
			for _, part := range strings.Split(annotContent, " ") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "name=") {
					pluginName = strings.TrimPrefix(part, "name=")
					pluginName = strings.Trim(pluginName, "\"")
				}
			}

			// 如果没有显式名称，使用文件名 (去除 .go 后缀)
			if pluginName == "" {
				pluginName = strings.TrimSuffix(entryName, ".go")
			}

			continue
		}
	}

	if pluginName == "" {
		// 无法确定插件名称，跳过
		return nil
	}

	// 检查是否已加载
	if _, exists := pl.loadedPlugins[pluginName]; exists {
		fmt.Printf("Plugin %s already loaded, skipping\n", pluginName)
		return nil
	}

	// 简单的插件实现 - 实际使用时会从文件中动态加载
	pl.loadedPlugins[pluginName] = map[string]any{
		"name": pluginName,
	}

	// 收集插件信息
	info := PluginInfo{
		Name:        pluginName,
		Description: fmt.Sprintf("Auto-discovered plugin from %s", filePath),
		Enabled:     true,
	}

	pl.pluginInfos[pluginName] = info

	fmt.Printf("Successfully loaded plugin: %s\n", pluginName)

	return nil
}

// GetLoadedPlugins 获取已加载的插件列表
func (pl *PluginLoader) GetLoadedPlugins() map[string]any {
	pl.mu.RLock()
	defer pl.mu.RUnlock()

	result := make(map[string]any)
	for k, v := range pl.loadedPlugins {
		result[k] = v
	}
	return result
}

// GetPluginInfos 获取已加载插件的信息
func (pl *PluginLoader) GetPluginInfos() map[string]PluginInfo {
	pl.mu.RLock()
	defer pl.mu.RUnlock()

	result := make(map[string]PluginInfo)
	for k, v := range pl.pluginInfos {
		result[k] = v
	}
	return result
}

// ReloadPlugin 重新加载指定插件
func (pl *PluginLoader) ReloadPlugin(name string) error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	// 卸载旧插件 - 直接使用 name，不需要 plugin 变量
	fmt.Printf("Unloaded plugin: %s\n", name)
	delete(pl.loadedPlugins, name)
	delete(pl.pluginInfos, name)

	// 重新加载
	return pl.loadGoFile(filepath.Join(pl.pluginDir, name+".go"), name)
}

// UnloadPlugin 卸载指定插件
func (pl *PluginLoader) UnloadPlugin(name string) error {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if _, exists := pl.loadedPlugins[name]; exists {
		fmt.Printf("Unloaded plugin: %s\n", name)
		delete(pl.loadedPlugins, name)
		delete(pl.pluginInfos, name)
		return nil
	}

	return fmt.Errorf("plugin %s not found", name)
}

// ListPlugins 列出所有已知插件 (已加载 + 配置信息)
func (pl *PluginLoader) ListPlugins() []PluginInfo {
	pl.mu.RLock()
	defer pl.mu.RUnlock()

	// 合并已加载和配置信息
	var result []PluginInfo

	// 添加已加载的插件信息
	for _, info := range pl.pluginInfos {
		infoCopy := info
		result = append(result, infoCopy)
	}

	// 按名称排序
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].Name > result[j].Name {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result
}
