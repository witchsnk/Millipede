// +build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// contractMatrix 为 Millipede 生成契约兼容性矩阵
func main() {
	// 扫描 plugins 目录下的所有插件
	pluginsDir := "./plugins"
	
	// 读取 plugins_gen.go 中的注册信息
	// 或者直接扫描 //plugin:export 注解
	
	fmt.Println("Millipede Contract Compatibility Matrix")
	fmt.Println("========================================")
	fmt.Println()
	
	// 扫描所有 .go 文件查找 plugin 注解
	// 查找 //plugin:export 行
	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		
		if strings.HasSuffix(path, ".go") {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			
			lines := strings.Split(string(content), "\n")
			for _, line := range lines {
				if strings.Contains(line, "//plugin:export") {
					fmt.Printf("Found plugin: %s\n", filepath.Base(path))
					break
				}
			}
		}
		return nil
	})
	
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning plugins: %v\n", err)
		os.Exit(1)
	}
	
	fmt.Println()
	fmt.Println("Matrix generation complete.")
}
