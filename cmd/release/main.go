// Command release 生成插件发布清单（约束 D1）。
//
// 用法：
//
//	go run ./cmd/release -version 1.0.0 -out bin/manifest.json \
//	  -key-file release.key proc-notifier=bin/proc-notifier
//
// 清单记录每个产物的 sha256 与 HMAC 签名；宿主加载时据此校验。
// 签名密钥属于发布侧，宿主侧只持有同一密钥用于验证。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/millipede/millipede/kernel/integrity"
)

type artifactList []string

func (a *artifactList) String() string { return strings.Join(*a, ",") }
func (a *artifactList) Set(v string) error {
	*a = append(*a, v)
	return nil
}

func main() {
	var arts artifactList
	version := flag.String("version", "", "插件版本（对应清单键 pluginID@version）")
	out := flag.String("out", "manifest.json", "清单输出路径")
	keyFile := flag.String("key-file", "", "签名密钥文件；为空则只生成校验和，不签名")
	flag.Var(&arts, "artifact", "产物，格式 pluginID=path（可重复）")
	flag.Parse()

	if *version == "" {
		fmt.Fprintln(os.Stderr, "release: -version is required")
		os.Exit(2)
	}
	if len(arts) == 0 {
		fmt.Fprintln(os.Stderr, "release: at least one -artifact is required")
		os.Exit(2)
	}

	var key []byte
	if *keyFile != "" {
		k, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "release: read key: %v\n", err)
			os.Exit(1)
		}
		key = k
	}

	artifacts := map[string]string{}
	for _, a := range arts {
		id, path, ok := strings.Cut(a, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "release: bad -artifact %q, want pluginID=path\n", a)
			os.Exit(2)
		}
		artifacts[id] = path
	}

	if err := integrity.SignAll(*out, key, *version, artifacts); err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("release: manifest written to %s (version=%s, artifacts=%d)\n", *out, *version, len(artifacts))
}
