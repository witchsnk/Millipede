package main

import (
	"fmt"
	"github.com/millipede/millipede/kernel/plugin"
)

func main() {
	// 创建插件加载器
	ldr := plugin.NewPluginLoader(
		func(o *plugin.PluginLoaderOptions) {
			o.PluginDir = "./plugins"
		},
	)

	fmt.Println("=== DiscoverAndLoad ===")
	// 自动发现并加载插件
	ldr.DiscoverAndLoad()

	fmt.Println("\n=== ListPlugins ===")
	// 列出所有已知插件
	plugins := ldr.ListPlugins()
	for _, p := range plugins {
		fmt.Printf("  - %s: %s\n", p.Name, p.Description)
	}

	fmt.Println("\n=== GetPluginInfos ===")
	// 获取插件信息
	infos := ldr.GetPluginInfos()
	for name, info := range infos {
		fmt.Printf("  %s: %s\n", name, info.Description)
	}

	fmt.Println("\n=== GetLoadedPlugins ===")
	// 获取已加载的插件
	loaded := ldr.GetLoadedPlugins()
	for name, plugin := range loaded {
		m := plugin.(map[string]any)
		fmt.Printf("  %s: %v\n", name, m)
	}

	fmt.Println("\n=== UnloadPlugin ===")
	// 卸载一个插件
	ldr.UnloadPlugin("temperature")

	fmt.Println("\n=== After Unload ===")
	infos = ldr.GetPluginInfos()
	for name, info := range infos {
		fmt.Printf("  %s: %s\n", name, info.Description)
	}

	fmt.Println("\n=== Demo Complete ===")
}
