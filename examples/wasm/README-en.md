# Large-Scale Pluggable Function Implementation - Millipede WASM Example Collection

## Goal
Demonstrate how to build **production-ready** pluggable WASM functionality using the Millipede framework, including:
- Multi-plugin management and lifecycle
- Method dispatch and scheduling
- Event bus integration
- Metrics collection and statistics
- Hot-swap support

## Directory Structure
```
examples/wasm/
├── README.md              # Main documentation (this file)
├── temperature/           # Temperature feature plugin directory
│   └── main.go            # Temperature plugin implementation
└── humidity/              # Humidity feature plugin directory
    └── main.go            # Humidity plugin implementation
```

## 1. Temperature Plugin

### Function Overview
Provides temperature readings, historical tracking, and statistical analysis.

### Available Methods
| Method | Description | Return Value |
|--------|-------------|------------|
| `readTemperature` | Current temperature reading | `float64` (default 20.0) |
| `setTemperature` | Set temperature value | `string` (confirmation) |
| `getHistory` | Get readings history | `[]float64` (max 100) |
| `getStats` | Get statistics | `map[string]interface{}` |

### Usage Example
```go
// Create plugin instance
temp := wasmexamples.NewTemperaturePlugin("sensor-001")

// Initialize
temp.Init(context.Background())

// Read current temperature
tempResult, _ := temp.Invoke(context.Background(), "readTemperature")
// Output: 20.0

// Set new temperature
setResult, _ := temp.Invoke(context.Background(), "setTemperature", 25.5)
// Output: "temperature set to 25.5"

// Get history
history, _ := temp.Invoke(context.Background(), "getHistory")
// Output: [20.0, 21.5, 22.0, ...]

// Get statistics
stats, _ := temp.Invoke(context.Background(), "getStats")
// Output: {"min":20.0,"max":25.5,"average":22.5,"count":5,"readings":5}

// Close plugin
temp.Close(context.Background())
```

### Metrics Collection
- Maintain last 100 readings of history
- Calculate min/max/average
- Auto-archive (automatically remove oldest readings when >100)

## 2. Humidity Plugin

### Function Overview
Provides humidity readings, historical tracking, and average calculation.

### Available Methods
| Method | Description | Return Value |
|--------|-------------|------------|
| `readHumidity` | Current humidity reading | `float64` (default 60.0) |
| `setHumidity` | Set humidity value | `string` (confirmation with %) |
| `getHistory` | Get readings history | `[]float64` (max 100) |
| `getAverage` | Get average humidity | `float64` |

### Usage Example
```go
// Create plugin instance
humid := wasmexamples.NewHumidityPlugin("sensor-002")

// Initialize
humid.Init(context.Background())

// Read current humidity
humidResult, _ := humid.Invoke(context.Background(), "readHumidity")
// Output: 60.0

// Set new humidity
setResult, _ := humid.Invoke(context.Background(), "setHumidity", 75.0)
// Output: "humidity set to 75.0%"

// Get history
history, _ := humid.Invoke(context.Background(), "getHistory")

// Get average
avg, _ := humid.Invoke(context.Background(), "getAverage")
// Output: 65.0 (if there are readings)

// Close plugin
humid.Close(context.Background())
```

### Metrics Collection
- Maintain last 100 readings of history
- Calculate real-time average
- Compare with current reading

## 3. How to Use These Plugins in Your Code

### Basic Usage Flow
```go
package main

import (
    "context"
    "fmt"
    "github.com/millipede/millipede/examples/wasm/temperature"
    "github.com/millipede/millipede/examples/wasm/humidity"
    "github.com/millipede/millipede/kernel/loader"
)

// 1. Create plugin instances
tempPlugin := temperature.NewTemperaturePlugin("temp-001")
humidPlugin := humidity.NewHumidityPlugin("humid-002")

// 2. Initialize plugins
tempPlugin.Init(context.Background())
humidPlugin.Init(context.Background())

// 3. Verify plugin status
fmt.Printf("Temperature plugin: %s\n", tempPlugin.Name())
fmt.Printf("Humidity plugin: %s\n", humidPlugin.Name())

// 4. Use plugin functionality
tempTemp, _ := tempPlugin.Invoke(context.Background(), "readTemperature")
fmt.Printf("Current temperature: %.1f°C\n", tempTemp)

// 5. Close plugins
tempPlugin.Close(context.Background())
humidPlugin.Close(context.Background())
```

### Advanced: Through Loader Management
```go
import (
    "context"
    "github.com/millipede/millipede/kernel/loader"
    "github.com/millipede/millipede/examples/wasm/temperature"
    "github.com/millipede/millipede/examples/wasm/humidity"
)

func main() {
    // Create loader
    ldr := loader.Base{}  // Actual usage should be specific loader subtype
    
    // Load plugins
    temp := temperature.NewTemperaturePlugin("t1")
    humid := humidity.NewHumidityPlugin("h1")
    
    // Initialize
    temp.Init(context.Background())
    humid.Init(context.Background())
    
    // Invoke methods (requires specific dispatch implementation)
    // result := ldr.Invoke(ctx, "temperature.readTemperature")
    
    // Close
    temp.Close(context.Background())
    humid.Close(context.Background())
}
```

## 4. Development Extension Guide

### Adding a New Plugin
1. Create a new .go file in `temperature/` or `humidity/` directory
2. Implement the `contract.Plugin` interface
3. Add `//plugin:export` annotation at the top of the file
4. Ensure correct use of `loader.Base` and `contract.Plugin` types

### Custom Functionality
Add new method in plugin's Invoke method:
```go
func (p *MyPlugin) Invoke(ctx context.Context, method string, args any) (any, error) {
    switch method {
    case "new-feature":
        // Handle new functionality
        return result, nil
    default:
        return nil, fmt.Errorf("unknown method: %s", method)
    }
}
```

### Running Tests
```bash
# Compile to verify syntax
cd examples/wasm/temperature && go build -o /tmp/temp-test .

# Check formatting
gofmt -w main.go

# Verify core packages still build
cd ../../.. && go build ./kernel/loader/wasm/...
```

## 5. Related Documentation
- [Millipede Usage Instructions](docs/07-使用说明.md)
- [Examples Summary](docs/08-例子汇总.md)
- [M6 Governance Configuration](config/go/governance.yaml)
- [WASM Loader Implementation](kernel/loader/wasm/loader.go)

## 6. Precautions

### Sandbox Environment Limitations
- `go build ./...` in sandbox environment may be restricted by cache directory permissions
- Core packages `go build ./kernel/loader/wasm/ ./kernel/loader/` work normally
- For full build, recommend running in local environment

### Plugin Specifications
- Each plugin file must include `//plugin:export` annotation at the top
- Must implement `contract.Plugin` interface
- Must import `github.com/millipede/millipede/kernel/loader` to get `Base` structure
- Recommended to use `go:generate go run ./cmd/gen` for automated code generation

### Performance Considerations
- Each plugin runs independently, mutually isolated
- History records max 100, preventing memory bloat
- Method calls through Invoke API, supporting cross-backend compatibility

## 7. License
Follow the Millipede project license.
