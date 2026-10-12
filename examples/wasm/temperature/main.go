// +build ignore
//go:generate go run ./cmd/gen
//plugin:export name=temperature-plugin api=1.0 backend=wasm extension=temperature

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/millipede/millipede/kernel/loader"
	"github.com/millipede/millipede/sdk/contract"
)

// TemperaturePlugin 提供温度读数和气候分析功能
type TemperaturePlugin struct {
	loader.Base
	temperature     float64
	readingsHistory []float64
	mu              sync.Mutex
	pluginID        string
}

// NewTemperaturePlugin 创建新的温度插件实例
func NewTemperaturePlugin(id string) *TemperaturePlugin {
	return &TemperaturePlugin{
		temperature:     20.0,
		readingsHistory: make([]float64, 0, 100),
		pluginID:        id,
	}
}

// Init 在插件初始化时调用
func (p *TemperaturePlugin) Init(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	p.readingsHistory = make([]float64, 0, 100)
	fmt.Printf("temperature-plugin (ID: %s) initialized\n", p.pluginID)
	
	// 记录初始读数
	p.recordReading(p.temperature)
	
	return nil
}

// Invoke 处理插件调用
func (p *TemperaturePlugin) Invoke(ctx context.Context, method string, args any) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	switch method {
	case "readTemperature":
		return p.temperature, nil
	
	case "getHistory":
		if len(p.readingsHistory) == 0 {
			return []float64{}, nil
		}
		// 返回历史读数的拷贝
		history := make([]float64, len(p.readingsHistory))
		copy(history, p.readingsHistory)
		return history, nil
	
	case "setTemperature":
		if val, ok := args.(float64); ok {
			p.temperature = val
			p.recordReading(val)
			return fmt.Sprintf("temperature set to %.1f", val), nil
		}
		return nil, fmt.Errorf("invalid temperature value: %v", args)
	
	case "getStats":
		return p.calculateStats(), nil
	
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

// calculateStats 计算统计信息
func (p *TemperaturePlugin) calculateStats() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	if len(p.readingsHistory) == 0 {
		return map[string]interface{}{
			"min":      p.temperature,
			"max":      p.temperature,
			"average":  p.temperature,
			"count":    0,
		}
	}
	
	min, max := p.readingsHistory[0], p.readingsHistory[0]
	sum := 0.0
	count := float64(len(p.readingsHistory))
	
	for _, r := range p.readingsHistory {
		if r < min {
			min = r
		}
		if r > max {
			max = r
		}
		sum += r
	}
	
	average := sum / count
	
	return map[string]interface{}{
		"min":      min,
		"max":      max,
		"average":  average,
		"count":    int(count),
		"readings": len(p.readingsHistory),
	}
}

// recordReading 添加读数到历史 (线程安全)
func (p *TemperaturePlugin) recordReading(temp float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	p.readingsHistory = append(p.readingsHistory, temp)
	
	// 保持历史记录在合理范围内 (最多100条)
	if len(p.readingsHistory) > 100 {
		p.readingsHistory = p.readingsHistory[len(p.readingsHistory)-100:]
	}
}

// Close 在插件关闭时调用
func (p *TemperaturePlugin) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	p.readingsHistory = nil
	fmt.Printf("temperature-plugin (ID: %s) closed\n", p.pluginID)
	return nil
}

// Name 返回插件名称
func (p *TemperaturePlugin) Name() string { return "temperature-plugin" }

// APIVersion 返回插件 API 版本
func (p *TemperaturePlugin) APIVersion() contract.APIVersion {
	return contract.APIVersion{Major: 1, Minor: 0}
}

// Ensure interface compliance at compile time
var _ contract.Plugin = (*TemperaturePlugin)(nil)

func main() {
	// 示例入口
	fmt.Println("temperature-plugin: main entry (loaded by framework)")
}
