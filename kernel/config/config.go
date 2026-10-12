// Package config 提供配置注入与变更监听（约束 E3）。
//
// 插件配置一律由框架统一注入，禁止插件绕过框架直接读环境变量/自建配置文件。
// 配置按 plugin_id 隔离命名空间，支持变更监听与回滚。
package config

import (
	"strconv"
	"sync"
)

// Source 是配置的底层来源（可由文件/远程/内存实现）。
type Source interface {
	Get(key string) (any, bool)
}

// Store 保存全量配置并支持按命名空间视图与变更订阅。
type Store struct {
	mu      sync.RWMutex
	data    map[string]any
	watches map[string][]func(key string, old, new any) // ns -> listeners
}

// New 构造配置中心。
func New() *Store {
	return &Store{data: make(map[string]any), watches: make(map[string][]func(string, any, any))}
}

// Set 写入一个命名空间下的配置项，并触发该命名空间的监听。
// 返回旧值，供回滚使用。
func (s *Store) Set(ns, key string, val any) any {
	full := ns + "." + key
	s.mu.Lock()
	old, hadOld := s.data[full]
	s.data[full] = val
	listeners := append([]func(string, any, any){}, s.watches[ns]...)
	s.mu.Unlock()

	for _, fn := range listeners {
		// 回调异步触发，避免在持锁时阻塞（约束 B6 思想）。
		go fn(full, valueOrNil(old, hadOld), val)
	}
	return old
}

// Delete 删除配置项并触发监听。
func (s *Store) Delete(ns, key string) {
	full := ns + "." + key
	s.mu.Lock()
	old, hadOld := s.data[full]
	delete(s.data, full)
	listeners := append([]func(string, any, any){}, s.watches[ns]...)
	s.mu.Unlock()
	if hadOld {
		for _, fn := range listeners {
			go fn(full, old, nil)
		}
	}
}

// View 返回某插件的隔离配置视图（实现 sdk/host.Config 语义）。
func (s *Store) View(ns string) *View { return &View{store: s, ns: ns} }

// Watch 订阅某命名空间的变更。
func (s *Store) Watch(ns string, fn func(key string, old, new any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watches[ns] = append(s.watches[ns], fn)
}

// View 是插件侧只读配置视图，键不需要带命名空间前缀。
type View struct {
	store *Store
	ns    string
}

// Get 读取配置。
func (v *View) Get(key string) (any, bool) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	val, ok := v.store.data[v.ns+"."+key]
	return val, ok
}

// GetString 读取字符串配置，缺省返回 def。
func (v *View) GetString(key, def string) string {
	if val, ok := v.Get(key); ok {
		switch t := val.(type) {
		case string:
			return t
		case []byte:
			return string(t)
		}
	}
	return def
}

// GetInt 读取整型配置，缺省返回 def。
func (v *View) GetInt(key string, def int) int {
	if val, ok := v.Get(key); ok {
		switch t := val.(type) {
		case int:
			return t
		case int64:
			return int(t)
		case float64:
			return int(t)
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	return def
}

func valueOrNil(v any, ok bool) any {
	if !ok {
		return nil
	}
	return v
}
