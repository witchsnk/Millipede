# Millipede 框架工程化入口。
# 所有命令自带本地缓存与离线构建参数（适配沙箱/受限网络环境）。

GO        ?= go
GOCACHE   := $(CURDIR)/.gocache
GOENV     := GOCACHE=$(GOCACHE) GOSUMDB=off GOPROXY=off
PKGS      := ./...

.PHONY: all build vet lint test race bench cover clean ci gen check-gen manifest demo status-endpoint hot-reload

all: build

build: ## 编译全部包与示例二进制
	$(GOENV) $(GO) build $(PKGS)
	@mkdir -p bin
	$(GOENV) $(GO) build -o bin/host ./cmd/host
	$(GOENV) $(GO) build -o bin/proc-notifier ./plugins/proc-notifier

manifest: build ## 生成插件发布清单（约束 D1：期望校验和的唯一可信来源）
	$(GOENV) $(GO) run ./cmd/release -version 1.0.0 -out bin/manifest.json \
		$(if $(KEYFILE),-key-file $(KEYFILE),$(if $(wildcard release.key),-key-file release.key,)) \
		-artifact proc-notifier=bin/proc-notifier

hot-reload: ## 产物热更冒烟：正常换代 + 篡改拒绝且旧代存活（H1–H6 / D1）
	@./hack/hotreload-check.sh

demo: manifest ## 端到端演示：清单校验 -> proc 调用 -> 卸载 -> 干净退出
	PROC_NOTIFIER_PATH=$(CURDIR)/bin/proc-notifier PROC_MANIFEST=$(CURDIR)/bin/manifest.json ./bin/host
	@! pgrep -f bin/proc-notifier >/dev/null || (echo "ERROR: 存在残留插件进程" && exit 1)
	@echo "端到端通过：无残留进程"

status-endpoint: manifest ## 验证 /plugins 状态端点（约束 E2）：能服务真实插件状态，且只读
	@HOST_PLUGINS_ADDR=127.0.0.1:19311 HOST_PLUGINS_HOLD=4s \
	 PROC_NOTIFIER_PATH=$(CURDIR)/bin/proc-notifier PROC_MANIFEST=$(CURDIR)/bin/manifest.json ./bin/host > /tmp/mp-status.log 2>&1 & \
	for i in $$(seq 1 80); do curl -s -m 1 http://127.0.0.1:19311/plugins > /dev/null 2>&1 && break; sleep 0.05; done; \
	curl -sf http://127.0.0.1:19311/plugins | grep -q '"id"' || (echo "ERROR: 端点未返回插件状态" && cat /tmp/mp-status.log && exit 1); \
	curl -sf http://127.0.0.1:19311/plugins | grep -q '"state": "Ready"' || (echo "ERROR: 插件状态非 Ready" && exit 1); \
	code=$$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:19311/plugins); \
	[ "$$code" = "405" ] || (echo "ERROR: 端点应只读，POST 返回 $$code" && exit 1); \
	wait; rm -f /tmp/mp-status.log; echo "状态端点验证通过：可服务真实状态、只读"

vet: ## go vet
	$(GOENV) $(GO) vet $(PKGS)

lint: ## golangci-lint（约束 A~G 组门禁）
	@golangci-lint run ./... || echo "提示: 未安装 golangci-lint，可用 'go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest'"

test: ## 单元测试
	$(GOENV) $(GO) test -timeout 180s $(PKGS)

race: ## 竞态检测（约束 B4/F2 门禁，必须 0 DATA RACE）
	$(GOENV) $(GO) test -race -timeout 180s $(PKGS)

bench: ## 运行效率基准（safecall 边界开销）
	$(GOENV) $(GO) test -run=NONE -bench=. -benchtime=1s ./kernel/safecall/

cover: ## 覆盖率
	$(GOENV) $(GO) test -coverprofile=cover.out $(PKGS)
	$(GOENV) $(GO) tool cover -func=cover.out | tail -1

gen: ## 代码生成（约束 F3：生成器幂等，CI 校验 diff 为空）
	$(GOENV) $(GO) generate $(PKGS)

check-gen: gen ## 校验生成物无 diff（CI 门禁 F3）
	@git diff --exit-code --stat || (echo "ERROR: 生成物与代码不一致，请先运行 make gen" && exit 1)

ci: vet test race ## CI 一键门禁：vet + 单测 + 竞态
	@echo "CI 门禁通过：vet/test/race 全绿"

clean: ## 清理构建产物与缓存
	rm -rf bin cover.out
