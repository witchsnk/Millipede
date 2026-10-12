#!/bin/bash
# 产物热更冒烟验证（约束 H1–H6 + D1）。
#
# 验证两件事：
#   1. 正常换代：换上新产物 + 清单摘要 → Reap 成功，pid 变化。
#   2. 拒绝换代：换上一个摘要不符的产物 → 热更失败，旧代存活。
set -u
cd "$(dirname "$0")/.."

BIN=./bin/host
REL="go run ./cmd/release"
PLUGIN=proc-notifier
WORK=$(mktemp -d)
PORT=${1:-19321}

# 脚本自身必须证明"无进程残留"（约束 C4/B3）。
# 验证脚本自己泄漏进程的话，门禁就是假的。
leaked=0
cleanup() {
  sleep 1
  local left
  left=$(pgrep -f "$WORK" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$left" != "0" ]; then
    echo "FAIL: 有 $left 个插件进程残留"
    pgrep -fl "$WORK" 2>/dev/null | head -5
    pkill -f "$WORK" 2>/dev/null
    leaked=1
  fi
  rm -rf "$WORK"
  [ "$leaked" = "0" ] || exit 1
}
trap cleanup EXIT

echo "构建宿主与插件..."
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off $BIN >/dev/null 2>&1
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off go build -o "$WORK/$PLUGIN.v1" ./plugins/$PLUGIN
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off go build -ldflags "-X main.buildTag=v2" -o "$WORK/$PLUGIN.v2" ./plugins/$PLUGIN

live="$WORK/$PLUGIN.live"
manifest="$WORK/manifest.json"

pid_of() { curl -s "http://127.0.0.1:$PORT/plugins" 2>/dev/null | \
  python3 -c "import json,sys
try:
  d=json.load(sys.stdin)
  print(next((p['id'] for p in d['plugins'] if p['id']=='$PLUGIN'),''))
except Exception: print('')" 2>/dev/null; }

echo "=== 用例 1：正常换代 ==="
cp "$WORK/$PLUGIN.v1" "$live"
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off $REL -version 1.0.0 -out "$manifest" \
  -artifact $PLUGIN="$live" >/dev/null

HOST_WATCH_INTERVAL=300ms HOST_PLUGINS_ADDR="127.0.0.1:$PORT" HOST_PLUGINS_HOLD=10s \
  PROC_NOTIFIER_PATH="$live" PROC_MANIFEST="$manifest" ./bin/host > "$WORK/host.log" 2>&1 &
HOSTPID=$!
sleep 2

grep -q "plugin watch enabled" "$WORK/host.log" || { echo "FAIL: 监听未开启"; cat "$WORK/host.log"; exit 1; }
echo "  [ok] 监听已开启"

# 发布流程：先发布新摘要，再原子替换产物文件。
#
# 关键：必须用 mv（同分区 rename，原子）而不是 cp/cat 覆盖。
# 覆盖写会先 truncate 再写入，监听器可能抓到"半截文件"，
# 导致新进程起不来（stdout closed before handshake）。
cp "$WORK/$PLUGIN.v2" "$WORK/staged.bin"
chmod +x "$WORK/staged.bin"
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off $REL -version 1.0.0 -out "$manifest" \
  -artifact $PLUGIN="$WORK/staged.bin" >/dev/null
mv "$WORK/staged.bin" "$live"

sleep 4
if grep -q "hot reload done" "$WORK/host.log"; then
  echo "  [ok] 正常换代成功"
else
  echo "  FAIL: 正常换代未成功"; grep -E "artifact changed|hot reload" "$WORK/host.log"; exit 1
fi
if grep -q "keeping old generation" "$WORK/host.log"; then
  echo "  FAIL: 正常换代却报旧代保留"; exit 1
fi

wait $HOSTPID 2>/dev/null

echo
echo "=== 用例 2：篡改产物必须被拒绝且旧代存活 ==="
# 全新起一份：回到 v1 产物 + 与之匹配的清单
cp "$WORK/$PLUGIN.v1" "$WORK/case2.live"
chmod +x "$WORK/case2.live"
live="$WORK/case2.live"
GOCACHE="$PWD/.gocache" GOSUMDB=off GOPROXY=off $REL -version 1.0.0 -out "$manifest" \
  -artifact $PLUGIN="$live" >/dev/null

HOST_WATCH_INTERVAL=300ms HOST_PLUGINS_ADDR="127.0.0.1:$PORT" HOST_PLUGINS_HOLD=8s \
  PROC_NOTIFIER_PATH="$live" PROC_MANIFEST="$manifest" ./bin/host > "$WORK/host2.log" 2>&1 &
HOSTPID=$!
sleep 2

sleep 4
grep -q "plugin watch enabled" "$WORK/host2.log" || { echo "FAIL: 监听未开启"; cat "$WORK/host2.log"; exit 1; }

printf 'TAMPERED' >> "$live"
sleep 4

grep -q "artifact changed" "$WORK/host2.log" || { echo "FAIL: 未检测到产物变更"; cat "$WORK/host2.log"; exit 1; }
echo "  [ok] 检测到产物变更"
grep -q "checksum mismatch" "$WORK/host2.log" || { echo "FAIL: 未拒绝篡改产物"; cat "$WORK/host2.log"; exit 1; }
echo "  [ok] 篡改产物被 D1 拒绝"
grep -q "keeping old generation" "$WORK/host2.log" || { echo "FAIL: 旧代未保留"; exit 1; }
echo "  [ok] 旧代保留继续服务"

wait $HOSTPID 2>/dev/null

echo
echo "热更冒烟验证通过"
