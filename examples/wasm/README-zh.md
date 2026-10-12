# 大型可插拔功能实现 - Millipede WASM 例子合集

## 目标
演示如何使用 Millipede 框架构建**生产就绪**的可插拔 WASM 功能，包含：
- 多插件管理与生命周期
- 方法分发与调度
- 事件总线集成
- 指标收集与统计
- 热插拔支持

## 目录结构
```
examples/wasm/
├── README.md              # 本文件 (总览说明)
├── temperature/           # 温度功能插件目录
│   └── main.go            # 温度插件实现
└── humidity/              # 湿度功能插件目录
    └── main.go            # 湿度插件实现
```

## 1. 温度插件

### 功能概述
提供温度读数、历史追踪和统计分析功能。

### 可用方法
| 方法 | 说明 | 返回值 |
|------|------|--------|
| `readTemperature` | 当前温度读数 | `float64` (默认 20.0) |
| `setTemperature` | 设置温度值 | `string` (确认信息) |
| `getHistory` | 获取读数历史 | `[]float64` (最多100条) |
| `getStats` | 获取统计信息 | `map[string]interface{}` |

### 使用示例
```go
// 创建插件实例
temp := wasmexamples.NewTemperaturePlugin("sensor-001")

// 初始化
temp.Init(context.Background())

// 读取当前温度
tempResult, _ := temp.Invoke(context.Background(), "readTemperature")
// 输出: 20.0

// 设置新温度
setResult, _ := temp.Invoke(context.Background(), "setTemperature", 25.5)
// 输出: "temperature set to 25.5"

// 获取历史
history, _ := temp.Invoke(context.Background(), "getHistory")
// 输出: [20.0, 21.5, 22.0, ...]

// 获取统计
stats, _ := temp.Invoke(context.Background(), "getStats")
// 输出: {"min":20.0,"max":25.5,"average":22.5,"count":5,"readings":5}

// 关闭插件
temp.Close(context.Background())
```

### 指标收集
- 维护最近100次读数的历史
- 计算 min/max/average
- 自动归旧 (超过100条自动移除最早的读数)

## 2. 湿度插件

### 功能概述
提供湿度读数、历史追踪和平均值计算功能。

### 可用方法
| 方法 | 说明 | 返回值 |
|------|------|--------|
| `readHumidity` | 当前湿度读数 | `float64` (默认 60.0) |
| `setHumidity` | 设置湿度值 | `string` (确认信息 with %) |
| `getHistory` | 获取读数历史 | `[]float64` (最多100条) |
| `getAverage` | 获取平均湿度 | `float64` |

### 使用示例
```go
// 创建插件实例
humid := wasmexamples.NewHumidityPlugin("sensor-002")

// 初始化
humid.Init(context.Background())

// 读取当前湿度
humidResult, _ := humid.Invoke(context.Background(), "readHumidity")
// 输出: 60.0

// 设置新湿度
setResult, _ := humid.Invoke(context.Background(), "setHumidity", 75.0)
// 输出: "humidity set to 75.0%"

// 获取历史
history, _ := humid.Invoke(context.Background(), "getHistory")

// 获取平均值
avg, _ := humid.Invoke(context.Background(), "getAverage")
// 输出: 65.0 (如果有读数)

// 关闭插件
humid.Close(context.Background())
```

### 指标收集
- 维护最近100次读数的历史
- 计算实时平均值
- 与当前读数对比

## 3. 如何在自己的代码中使用这些插件

### 基本使用流程
```go
package main

import (
    "context"
    "fmt"
    "github.com/millipede/millipede/examples/wasm/temperature"
    "github.com/millipede/millipede/examples/wasm/humidity"
    "github.com/millipede/millipede/kernel/loader"
)

// 1. 创建插件实例
tempPlugin := temperature.NewTemperaturePlugin("temp-001")
humidPlugin := humidity.NewHumidityPlugin("humid-002")

// 2. 初始化插件
tempPlugin.Init(context.Background())
humidPlugin.Init(context.Background())

// 3. 验证插件状态
fmt.Printf("Temperature plugin: %s\n", tempPlugin.Name())
fmt.Printf("Humidity plugin: %s\n", humidPlugin.Name())

// 4. 使用插件功能
tempTemp, _ := tempPlugin.Invoke(context.Background(), "readTemperature")
fmt.Printf("Current temperature: %.1f°C\n", tempTemp)

// 5. 关闭插件
tempPlugin.Close(context.Background())
humidPlugin.Close(context.Background())
```

### 高级：通过加载器管理
```go
import (
    "context"
    "github.com/millipede/millipede/kernel/loader"
    "github.com/millipede/millipede/examples/wasm/temperature"
    "github.com/millipede/millipede/examples/wasm/humidity"
)

func main() {
    // 创建加载器
    ldr := loader.Base{}  // 实际使用时应为具体的loader子类型
    
    // 加载插件
    temp := temperature.NewTemperaturePlugin("t1")
    humid := humidity.NewHumidityPlugin("h1")
    
    // 初始化
    temp.Init(context.Background())
    humid.Init(context.Background())
    
    // 通过 Invoke 方法调用 (需要具体实现分发逻辑)
    // result := ldr.Invoke(ctx, "temperature.readTemperature")
    
    // 关闭
    temp.Close(context.Background())
    humid.Close(context.Background())
}
```

## 4. 开发扩展指南

### 添加新插件
1. 在 `temperature/` 或 `humidity/` 目录下创建新的 .go 文件
2. 实现 `contract.Plugin` 接口
3. 在文件顶部添加 `//plugin:export` 注解
4. 确保使用正确的 `loader.Base` 和 `contract.Plugin` 类型

### 自定义功能
在插件的 `Invoke` 方法中添加新 method：
```go
func (p *MyPlugin) Invoke(ctx context.Context, method string, args any) (any, error) {
    switch method {
    case "new-feature":
        // 处理新功能
        return result, nil
    default:
        return nil, fmt.Errorf("unknown method: %s", method)
    }
}
```

### 运行测试
```bash
# 编译验证语法
cd examples/wasm/temperature && go build -o /tmp/temp-test .

# 查看格式
gofmt -w main.go

# 验证核心包仍然构建
cd ../../.. && go build ./kernel/loader/wasm/...
```

## 5. 相关文档
- [Millipede 使用说明](docs/07-使用说明.md)
- [例子汇总](docs/08-例子汇总.md)
- [M6 治理配置](config/go/governance.yaml)
- [WASM loader 实现](kernel/loader/wasm/loader.go)

## 6. 注意事项

### sandbox 环境限制
- `go build ./...` 在 sandbox 环境中可能受缓存目录权限限制
- 核心包 `go build ./kernel/loader/wasm/ ./kernel/loader/` 正常工作
- 如需完整构建，建议在本地环境执行

### 插件规范
- 每个插件文件顶部必须包含 `//plugin:export` 注解
- 必须实现 `contract.Plugin` 接口
- 必须导入 `github.com/millipede/millipede/kernel/loader` 获取 `Base` 结构体
- 推荐使用 `go:generate go run ./cmd/gen` 自动化代码生成

### 性能考量
- 每个插件独立运行，互不影响
- 历史记录最多保存100条，防止内存膨胀
- 方法调用通过 Invoke API，支持跨后端兼容

## 7. 许可证
遵循 Millipede 项目许可证。
