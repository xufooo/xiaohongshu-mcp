#!/usr/bin/env bash
# pi-calibrate.sh —— 目标机（树莓派 3B）上量化优化的实际效果。
#
# 只依赖 bash + curl + grep + sed + /proc，不需要 node/python。
# 量的是「代码侧那些优化到底有没有生效」，而不是靠推断：
#   1) 服务起到 MCP 可用的时间
#   2) 生效配置自述：低资源档 / 浏览器 profile 是否持久 / 拦截模式条数
#   3) start_page 墙钟耗时与结果（feed_card_count / 风险/未登录提示）
#   4) 浏览器进程树的 RSS / PSS（按 profile 目录匹配进程）
#   5) get_page_state.browser：pages_created / warm_page_reused /
#      navigation_skipped / blocked_url_patterns / profile_persistent
#   6) 连续两次 start_page：navigation_skipped 是否增加
#
# 用法：
#   PORT=18160 BROWSER_BIN=/usr/bin/chromium \
#     ./scripts/pi-calibrate.sh /usr/local/bin/xiaohongshu-mcp
#
# 说明：脚本自己拉起服务、跑完就关；profile 等按服务自身默认（含默认持久 profile）。
# 需要登录态才能跑完第 5/6 步：未登录时会停在风险页并明确提示。
set -uo pipefail

BIN="${1:-}"
PORT="${PORT:-18160}"
BROWSER_BIN="${BROWSER_BIN:-}"
LOG="$(mktemp -t pi-calibrate.XXXXXX.log)"

if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "用法: $0 <xiaohongshu-mcp 可执行文件>   [PORT=18160 BROWSER_BIN=/usr/bin/chromium]" >&2
  exit 2
fi

note() { printf '%-44s %s\n' "$1" "$2"; }

# print_waits 打印 waits 块：每类等待的次数 / 累计耗时 / 最大耗时 / 页内探测次数。
# 这些数字是真机验收的关键证据（探测次数不随等待时长增长 = 机制与机器状态无关）。
# 只用 grep/sed/tr，保持本脚本"不依赖 node/python"的约束。
print_waits() {
  printf '%s' "$1" | tr -d ' \n\t' \
    | grep -oE '"waits":\{.*\}' \
    | grep -oE '"[^"]+":\{"count":[0-9]+,"total_ms":[0-9]+,"max_ms":[0-9]+(,"probes":[0-9]+)?\}' \
    | sed -E 's/^"([^"]+)":\{"count":([0-9]+),"total_ms":([0-9]+),"max_ms":([0-9]+)(,"probes":([0-9]+))?\}$/   \1 count=\2 total=\3ms max=\4ms probes=\6/' \
    | sed -E 's/probes=$/probes=0/'
}

# 从服务日志里取 profile 目录：三种日志形态都要覆盖，并剥掉尾部中文括注与引号
profile_dir_from_log() {
  grep -oE '(default persistent browser profile: *[^"]*|browser profile from XHS_BROWSER_PROFILE_DIR: *[^"]*|浏览器 profile 退到 *[^ ]*)' "$1" \
    | head -1 | sed 's/^.*: *//; s/^.*退到 *//; s/（.*$//; s/"$//'
}

# 内层 data 是被转义的 JSON 字符串，先还原再取字段
unescape() { printf '%s' "$1" | sed 's/\\"/"/g'; }
field() { # field <json> <key>
  printf '%s' "$1" | grep -oE "\"$2\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[^,}]*)" \
    | head -1 | sed 's/^[^:]*:[[:space:]]*//; s/^"//; s/"$//'
}

mcp_post() { # mcp_post <session_id|-> <body>
  local sid="$1" body="$2"
  local args=(-s -X POST "http://127.0.0.1:${PORT}/mcp"
    -H 'Content-Type: application/json'
    -H 'Accept: application/json, text/event-stream'
    --max-time 300 -d "$body")
  [ "$sid" != "-" ] && args+=(-H "Mcp-Session-Id: ${sid}")
  curl "${args[@]}" | sed 's/^data: //' | tr -d '\r'
}

run_tool() { # run_tool <name> <args-json>
  mcp_post "$SID" "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"${1}\",\"arguments\":${2}}}"
}

echo "== 启动服务（日志：$LOG）"
echo "   XHS_LOW_RESOURCE=${XHS_LOW_RESOURCE:-<未设置，按架构默认>}  PORT=$PORT"
START=$(date +%s.%N)
env ${BROWSER_BIN:+ROD_BROWSER_BIN="$BROWSER_BIN"} "$BIN" -port ":$PORT" ${BROWSER_BIN:+-bin "$BROWSER_BIN"} >"$LOG" 2>&1 &
SVC_PID=$!
trap 'kill $SVC_PID 2>/dev/null' EXIT

for _ in $(seq 1 600); do
  curl -s -o /dev/null --max-time 1 "http://127.0.0.1:${PORT}/mcp" && break
  sleep 0.2
done
note "1) 服务起到 MCP 可用" "$(echo "$(date +%s.%N) $START" | awk '{printf "%.2fs", $1-$2}')"

echo
echo "== 2) 生效配置自述"
if grep -q 'low resource profile enabled' "$LOG"; then
  note "   低资源档" "$(grep -o 'low resource profile enabled: .*' "$LOG" | head -1)"
else
  note "   低资源档" "未启用（x86 上属正常；arm64 应默认启用）"
fi
note "   profile" "$(profile_dir_from_log "$LOG")"
if grep -q 'cache dir 不可写' "$LOG"; then
  note "   警告" "缓存目录不可写 → profile 退到临时目录，冷启动缓存不持久"
fi
if grep -q 'blocking [0-9]* URL patterns per page' "$LOG"; then
  note "   拦截模式" "$(grep -o 'blocking [0-9]* URL patterns per page' "$LOG" | head -1)"
else
  note "   拦截模式" "无（非低资源档；arm64 上应默认拦截图片/媒体）"
fi

SID=$(curl -s -D - -o /dev/null -X POST "http://127.0.0.1:${PORT}/mcp" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"pi-calibrate","version":"1"}}}' \
  | grep -i '^mcp-session-id:' | head -1 | sed 's/.*: //; s/\r//')
if [ -z "$SID" ]; then echo "!! MCP 初始化失败，看 $LOG" >&2; exit 1; fi
mcp_post "$SID" '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null

echo
echo "== 3) start_page（冷启动：新建浏览器 + 首屏）"
T1=$(date +%s.%N)
RAW1=$(run_tool start_page '{}')
T2=$(date +%s.%N)
OUT1=$(unescape "$RAW1")
note "   耗时" "$(echo "$T2 $T1" | awk '{printf "%.1fs", $1-$2}')"
note "   isError" "$(field "$OUT1" isError)"
note "   feed_card_count" "$(field "$OUT1" feed_card_count)"
note "   风险/登录提示" "$(echo "$OUT1" | grep -o '风险信号\|未登录\|get_login_qrcode\|页面不存在' | head -1)"
SESS=$(field "$OUT1" id)

echo
echo "== 4) 浏览器进程树内存（按 profile 目录匹配）"
PROFILE_DIR=$(profile_dir_from_log "$LOG")
if [ -n "$PROFILE_DIR" ]; then
  RSS=0; PSS=0; N=0
  for pid in $(ls /proc | grep -E '^[0-9]+$'); do
    if tr '\0' ' ' 2>/dev/null < "/proc/$pid/cmdline" | grep -qF "$PROFILE_DIR"; then
      r=$(awk '/^VmRSS:/{print $2}' "/proc/$pid/status" 2>/dev/null); r=${r:-0}
      p=$(awk '/^Pss:/{print $2}' "/proc/$pid/smaps_rollup" 2>/dev/null); p=${p:-0}
      RSS=$((RSS + r)); PSS=$((PSS + p)); N=$((N + 1))
    fi
  done
  note "   进程数 / RSS 合计" "${N} / $(echo "$RSS" | awk '{printf "%.0f MB", $1/1024}')"
  note "   PSS 合计（比例分摊，推荐口径）" "$(echo "$PSS" | awk '{printf "%.0f MB", $1/1024}')"
else
  note "   未能从日志解析 profile 目录" "跳过"
fi

if [ -z "$SESS" ] || [ "$(field "$OUT1" isError)" = "true" ]; then
  echo
  echo "== 5/6 跳过：start_page 未成功（多半是未登录/命中了风险页）"
  echo "   先登录（get_login_qrcode → 手机扫码 → check_login_status）后重跑本脚本，"
  echo "   才能读到 get_page_state.browser 与 navigation_skipped 的计数。"
  echo
  echo "完成（未登录路径）。完整日志：$LOG"
  exit 0
fi

echo
echo "== 5) get_page_state.browser（优化是否真的命中）"
STATE=$(unescape "$(run_tool get_page_state "{\"session_id\":\"${SESS}\"}")")
for key in pages_created warm_page_reused warm_page_cached navigation_skipped blocked_url_patterns profile_persistent idle_timeout_seconds; do
  note "   $key" "$(field "$STATE" "$key")"
done

echo
echo "== 5b) waits（各类等待：次数 / 累计 / 最大 / 页内探测数）"
print_waits "$STATE"
note "   读法" "探测次数不随时长增长 = 机制与机器状态无关；Pi 上应比 x86 更慢但探测次数不涨"

echo
echo "== 6) 再调一次 start_page（会话/热页面复用，看 navigation_skipped 是否增加）"
BEFORE=$(field "$STATE" navigation_skipped)
run_tool start_page '{}' >/dev/null
STATE2=$(unescape "$(run_tool get_page_state "{\"session_id\":\"${SESS}\"}")")
note "   navigation_skipped 前后" "${BEFORE} → $(field "$STATE2" navigation_skipped)"

echo
echo "== 7) 浏览器空闲关闭汇总（XHS_BROWSER_IDLE_TIMEOUT 调小可快点看到）"
grep -o 'browser idle close: .*' "$LOG" | tail -1 || true
echo
echo "完成。完整日志：$LOG"
