// +build ignore
//go:generate go run ./cmd/gen
//plugin:export name=wasm-plugin-loader api=1.0 backend=wasm extension=wasm-loader

package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/sdk/contract"
	"github.com/millipede/millipede/sdk/logger"
)

// wasmPluginLoader 演示如何加载和管理多个 WASM 插件
type wasmPluginLoader struct {
	loader.Base
	plugins      map[string]contract.Plugin
	pluginConfigs map[string]map[string]any
	mu              sync.RWMutex
	eventBus        logger.EventBus
}

// NewWasmPluginLoader 创建新的插件加载器
func NewWasmPluginLoader() *wasmPluginLoader {
	return &wasmPluginLoader{
		plugins:       make(map[string]contract.Plugin),
		pluginConfigs: make(map[string]map[string]any),
	}
}

// Init 初始化插件加载器
func (p *wasmPluginLoader) Init(ctx context.Context) error {
	// 从 context 提取 eventbus
	if eb, ok := ctx.Value(logger.EventBusKey).(logger.EventBus); ok {
		p.eventBus = eb
	}
	
	if p.eventBus != nil {
		p.eventBus.Publish("loader.initialized", map[string]interface{}{
			"loader_type": "wasm",
		})
	}
	
	fmt.Println("wasm-plugin-loader: initialized")
	return nil
}

// LoadPlugin 加载指定名称的插件
func (p *wasmPluginLoader) LoadPlugin(name string, plugin contract.Plugin) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	if _, exists := p.plugins[name]; exists {
		return fmt.Errorf("plugin %s already loaded", name)
	}
	
	// 初始化插件
	if err := plugin.Init(context.Background()); err != nil {
		return fmt.Errorf("failed to init plugin %s: %w", name, err)
	}
	
	p.plugins[name] = plugin
	
	if p.eventBus != nil {
		p.eventBus.Publish("plugin.loaded", map[string]interface{}{
			"plugin_name": name,
			"plugin_type": fmt.Sprintf("%T", plugin),
		})
	}
	
	fmt.Printf("wasm-plugin-loader: plugin %s loaded successfully\n", name)
	return nil
}

// UnloadPlugin 卸载指定名称的插件
func (p *wasmPluginLoader) UnloadPlugin(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	plugin, exists := p.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %s not found", name)
	}
	
	// 调用插件关闭
	if err := plugin.Close(context.Background()); err != nil {
		return fmt.Errorf("failed to close plugin %s: %w", name, err)
	}
	
	delete(p.plugins, name)
	
	if p.eventBus != nil {
		p.eventBus.Publish("plugin.unloaded", map[string]interface{}{
			"plugin_name": name,
		})
	}
	
	fmt.Printf("wasm-plugin-loader: plugin %s unloaded successfully\n", name)
	return nil
}

// Invoke 处理插件调用 - 分发到对应插件
func (p *wasmPluginLoader) Invoke(ctx context.Context, method string, args any) (any, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	// 解析方法: "plugin_name.method_name"
	if len(method) < 1 {
		return nil, fmt.Errorf("method name required")
	}
	
	// 查找点号分隔
	dotIdx := method.Index('.')
	if dotIdx < 0 {
		// 如果没有点号，尝试作为广播方法处理
		return p.invokeBroadcast(method, args)
	}
	
	pluginName := method[:dotIdx]
	methodName := method[dotIdx+1:]
	
	plugin, exists := p.plugins[pluginName]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", pluginName)
	}
	
	result, err := plugin.Invoke(ctx, methodName, args)
	if err != nil {
		return nil, fmt.Errorf("plugin %s invoke %s failed: %w", pluginName, methodName, err)
	}
	
	return result, nil
}

// invokeBroadcast 广播调用所有插件
func (p *wasmPluginLoader) invokeBroadcast(method string, args any) (any, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	results := make(map[string]any)
	var errors []string
	
	for name, plugin := range p.plugins {
		result, err := plugin.Invoke(ctx, method, args)
		if err != nil {
			errors = append(errors, fmt.Sprintf("plugin %s: %v", name, err))
			continue
		}
		results[name] = result
	}
	
	if len(results) > 0 {
		return results, nil
	}
	
	if len(errors) > 0 {
		return nil, fmt.Errorf("all plugins failed: %v", errors)
	}
	
	return nil, fmt.Errorf("no plugins available")
}

// GetPluginInfo 获取插件信息
func (p *wasmPluginLoader) GetPluginInfo(name string) (map[string]interface{}, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	plugin, exists := p.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", name)
	}
	
	info := map[string]interface{}{
		"name":        plugin.Name(),
		"api_version": fmt.Sprintf("%d.%d", plugin.APIVersion().Major, plugin.APIVersion().Minor),
		"running":     true,
	}
	
	return info, nil
}

// GetAllPluginsInfo 获取所有插件信息
func (p *wasmPluginLoader) GetAllPluginsInfo() (map[string]map[string]interface{}, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	result := make(map[string]map[string]interface{})
	
	for name, plugin := range p.plugins {
		info := map[string]interface{}{
			"name":        plugin.Name(),
			"api_version": fmt.Sprintf("%d.%d", plugin.APIVersion().Major, plugin.APIVersion().Minor),
			"running":     true,
		}
		result[name] = info
	}
	
	return result, nil
}

// Close 关闭所有插件
func (p *wasmPluginLoader) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	// 按照逆序关闭插件
	for name, plugin := range p.plugins {
		if err := plugin.Close(ctx); err != nil {
			fmt.Printf("warning: failed to close plugin %s: %v\n", name, err)
		}
	}
	
	p.plugins = make(map[string]contract.Plugin)
	
	if p.eventBus != nil {
		p.eventBus.Publish("loader.closed", map[string]interface{}{
			"loader_type": "wasm",
		})
	}
	
	fmt.Println("wasm-plugin-loader: all plugins closed")
	return nil
}

// Name 返回加载器名称
func (p *wasmPluginLoader) Name() string { return "wasm-plugin-loader" }

// APIVersion 返回加载器 API 版本
func (p *wasmPluginLoader) APIVersion() contract.APIVersion {
	return contract.APIVersion{Major: 1, Minor: 0}
}

// Ensure interface compliance
var _ contract.Plugin = (*wasmPluginLoader)(nil)

func main() {
	// 示例入口
	fmt.Println("wasm-plugin-loader: main entry (loaded by framework)")
	
	// 这里通常由 Millipede 框架自动加载插件
	// 示例：loader.LoadPlugin("temperature", tempPlugin)
	// 示例：loader.LoadPlugin("humidity", humidityPlugin)
}
