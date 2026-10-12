# Millipede - Go Plugin Framework

## 项目介绍

Millipede 是一个 Go 插件化框架，旨在复刻 Java 插件体系的核心能力：
- SPI 发现
- 依赖注入
- 生命周期管理
- 热插拔
- 故障隔离

**核心设计**：Go 为 AOT 静态链接，单进程仅有一套全局类型表，不存在运行时装载新类型入口。隔离能力必须由进程/WASM/解释器换取。

主路径 = `builtin`（编译期注册） + `proc`（子进程 + RPC）；`wasm` 作为不可信代码/需可卸载场景的增强路径。

## Plugin System

### Dynamic Plugin Loading
Millipede supports dynamic plugin loading through the `plugins` folder. The framework automatically discovers and loads Go plugin files that contain `//plugin:export` annotations.

**How it works:**
- Scans `plugins` directory (or custom path) for `.go` files
- Parses `//plugin:export name="myplugin"` annotations
- If no explicit name, uses filename (without `.go` suffix) as plugin name
- Supports recursive subdirectory scanning

**Usage:**
```go
// Auto-discovery (called in init)
_ = NewPluginLoader().DiscoverAndLoad()

// Or customize plugin directory
NewPluginLoader(func(o *plugin.PluginLoaderOptions) {
    o.PluginDir = "my-custom-plugins"
})
```

### Plugin Annotation Format
```go
// plugin:export name="myplugin" description="your description"
// 或不带 name，使用文件名作为插件名
```

**Core Paths:**
- `builtin`：编译期注册
- `proc`：子进程 + RPC
- `wasm`：动态加载（通过 plugins 文件夹）

## Quick Start

### Compile Verification

```bash
# 验证核心包构建
go build ./kernel/loader/wasm/...
go build ./kernel/loader/...
```

### Run Plugin Examples

```bash
# 启动 host 程序
go run ./cmd/host

# 或者直接运行温度插件示例
cd examples/wasm/temperature && go run main.go
```

### Plugin Method Invocation Demo (Go)

```go
// 创建插件实例
plugin := wasmexamples.NewTemperaturePlugin("sensor-001")

// 初始化
plugin.Init(context.Background())

// 调用方法
temp, _ := plugin.Invoke(context.Background(), "readTemperature")
// 输出: 20.0

// 设置温度
plugin.Invoke(context.Background(), "setTemperature", 25.5)

// 关闭插件
plugin.Close(context.Background())
```

---

# Millipede - Go 插件框架

## Project Introduction

Millipede is a Go plugin framework designed to replicate the core capabilities of Java plugin systems:
- SPI discovery
- Dependency injection
- Lifecycle management
- Hot swapping
- Fault isolation

**Core Design**: Go is AOT statically linked, a single process has only one global type table, and there is no runtime entry for loading new types. Isolation capability must be obtained through process/WASM/interpreter.

Main path = `builtin` (compile-time registration) + `proc` (subprocess + RPC); `wasm` as an enhanced path for untrusted code / scenarios requiring unloading.

## Plugin System

### Dynamic Plugin Loading
Millipede supports dynamic plugin loading through the `plugins` folder. The framework automatically discovers and loads Go plugin files that contain `//plugin:export` annotations.

**How it works:**
- Scans `plugins` directory (or custom path) for `.go` files
- Parses `//plugin:export name="myplugin"` annotations
- If no explicit name, uses filename (without `.go` suffix) as plugin name
- Supports recursive subdirectory scanning

**Usage:**
```go
// Auto-discovery (called in init)
_ = NewPluginLoader().DiscoverAndLoad()

// Or customize plugin directory
NewPluginLoader(func(o *plugin.PluginLoaderOptions) {
    o.PluginDir = "my-custom-plugins"
})
```

### Plugin Annotation Format
```go
// plugin:export name="myplugin" description="your description"
// 或不带 name，使用文件名作为插件名
```

**Core Paths:**
- `builtin`：编译期注册
- `proc`：子进程 + RPC
- `wasm`：动态加载（通过 plugins 文件夹）

## Quick Start

### Compile Verification

```bash
# Verify core package build
go build ./kernel/loader/wasm/...
go build ./kernel/loader/...
```

### Run Plugin Examples

```bash
# Start the host program
go run ./cmd/host

# Or run the temperature plugin example directly
cd examples/wasm/temperature && go run main.go
```

### Plugin Method Invocation Demo (Go)

```go
// Create plugin instance
plugin := wasmexamples.NewTemperaturePlugin("sensor-001")

// Initialize
plugin.Init(context.Background())

// Call methods
temp, _ := plugin.Invoke(context.Background(), "readTemperature")
// Output: 20.0

// Set temperature
plugin.Invoke(context.Background(), "setTemperature", 25.5)

// Close plugin
plugin.Close(context.Background())
```