//go:build ignore
// +build ignore

//go:generate go run github.com/witchsnk/millipede/cmd/gen
//plugin:export name=humidity-plugin api=1.0 backend=wasm extension=humidity

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
	"github.com/witchsnk/millipede/sdk/contract"
)

// HumidityPlugin 提供湿度读数和环境监测功能
type HumidityPlugin struct {
	loader.Base
	humidity float64
	history  []float64
	mu       sync.Mutex
	pluginID string
}

// NewHumidityPlugin 创建新的湿度插件实例
func NewHumidityPlugin(id string) *HumidityPlugin {
	return &HumidityPlugin{
		humidity: 60.0,
		history:  make([]float64, 0, 100),
		pluginID: id,
	}
}

// Init 在插件初始化时调用
func (p *HumidityPlugin) Init(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.history = make([]float64, 0, 100)
	fmt.Printf("humidity-plugin (ID: %s) initialized\n", p.pluginID)

	// 记录初始读数
	p.recordReading(p.humidity)

	return nil
}

// Invoke 处理插件调用
func (p *HumidityPlugin) Invoke(ctx context.Context, method string, args any) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch method {
	case "readHumidity":
		return p.humidity, nil

	case "setHumidity":
		if val, ok := args.(float64); ok {
			p.humidity = val
			p.recordReading(val)
			return fmt.Sprintf("humidity set to %.1f%%", val), nil
		}
		return nil, fmt.Errorf("invalid humidity value: %v", args)

	case "getHistory":
		if len(p.history) == 0 {
			return []float64{}, nil
		}
		history := make([]float64, len(p.history))
		copy(history, p.history)
		return history, nil

	case "getAverage":
		return p.calculateAverage(), nil

	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

// calculateAverage 计算平均湿度
func (p *HumidityPlugin) calculateAverage() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.history) == 0 {
		return p.humidity
	}

	sum := 0.0
	for _, h := range p.history {
		sum += h
	}
	return sum / float64(len(p.history))
}

// recordReading 添加读数到历史
func (p *HumidityPlugin) recordReading(humid float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.history = append(p.history, humid)
	if len(p.history) > 100 {
		p.history = p.history[len(p.history)-100:]
	}
}

// Close 在插件关闭时调用
func (p *HumidityPlugin) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.history = nil
	fmt.Printf("humidity-plugin (ID: %s) closed\n", p.pluginID)
	return nil
}

// Name 返回插件名称
func (p *HumidityPlugin) Name() string { return "humidity-plugin" }

// APIVersion 返回插件 API 版本
func (p *HumidityPlugin) APIVersion() contract.APIVersion {
	return contract.APIVersion{Major: 1, Minor: 0}
}

// Ensure interface compliance at compile time
var _ contract.Plugin = (*HumidityPlugin)(nil)

func main() {
	fmt.Println("humidity-plugin: main entry (loaded by framework)")
}
