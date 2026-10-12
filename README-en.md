# Millipede - Go Plugin Framework

## Project Overview

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
// Or without a name, use the filename as the plugin name

**Core Paths:**
- `builtin`：compile-time registration
- `proc`：subprocess + RPC
- `wasm`：dynamic loading (through plugins folder)

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