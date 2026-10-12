# Millipede - Go插件框架

## 项目介绍

Millipede 是一个 Go 插件化框架，旨在复刻 Java 插件体系的核心能力：
- SPI 发现
- 依赖注入
- 生命周期管理
- 热插拔
- 故障隔离

**核心设计**：Go 为 AOT 静态链接，单进程仅有一套全局类型表，不存在运行时装载新类型入口。隔离能力必须由进程/WASM/解释器换取。

主路径 = `builtin`（编译期注册） + `proc`（子进程 + RPC）；`wasm` 作为不可信代码/需可卸载场景的增强路径。

## 插件系统

### 动态插件加载

Millipede 通过 `plugins` 文件夹支持动态插件加载。框架会自动发现并加载包含 `//plugin:export` 注解的 Go 插件文件。

**它是如何工作的：**
- 扫描 `plugins` 目录（或自定义路径）中的 `.go` 文件
- 解析 `//plugin:export name="myplugin"` 注解
- 如果没有显式名称，使用不带 `.go` 后缀的文件名作为插件名
- 支持递归子目录扫描

**用法：**
```go
// 自动发现（在 init 中调用）
_ = NewPluginLoader().DiscoverAndLoad()

// 或自定义插件目录
NewPluginLoader(func(o *plugin.PluginLoaderOptions) {
    o.PluginDir = "my-custom-plugins"
})
```

### 插件注解格式

```go
// plugin:export name="myplugin" description="your description"
// 或不带 name，使用文件名作为插件名

**核心路径：**
- `builtin`：编译期注册
- `proc`：子进程 + RPC
- `wasm`：动态加载（通过 plugins 文件夹）

## 快速开始

### 编译验证

```bash
# 验证核心包构建
go build ./kernel/loader/wasm/...
go build ./kernel/loader/...
```

### 运行插件示例

```bash
# 启动 host 程序
go run ./cmd/host

# 或直接运行温度插件示例
cd examples/wasm/temperature && go run main.go
```

### Go 插件方法调用演示

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
