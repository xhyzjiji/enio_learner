#!/usr/bin/env bash
#
# 用 launchd 管理 Homebrew 安装的 Ollama 后台服务（macOS）。
# Manage the Homebrew-installed Ollama background service through launchd (macOS).
#
# 用法 / Usage:
#   ./deploy/ollama/ollama.sh start     # 启动并等待 API 就绪 / start and wait for the API
#   ./deploy/ollama/ollama.sh stop      # 停止服务，释放内存 / stop the service and free memory
#   ./deploy/ollama/ollama.sh restart   # 重启进程 / restart the process
#   ./deploy/ollama/ollama.sh status    # 查看状态 / show status
#   ./deploy/ollama/ollama.sh logs      # 跟踪日志 / follow the log
#
# 不要用 brew services restart：它会卸载再重新加载服务，重新加载后 launchd 可能一直不拉起进程
# （state = not running、runs = 0，brew services list 显示 other）。这里统一用 kickstart 强制启动。
# Avoid `brew services restart`: it unloads and reloads the job, after which launchd may never spawn
# the process (state = not running, runs = 0, listed as "other"). kickstart forces a run instead.

set -euo pipefail

OLLAMA_URL="${OLLAMA_URL:-http://127.0.0.1:11434}"
WAIT_SECONDS="${WAIT_SECONDS:-20}"
DOMAIN="gui/$(id -u)"
AGENTS_DIR="$HOME/Library/LaunchAgents"

PLIST=""
LABEL=""
TARGET=""

log()  { printf '\033[1;34m[ollama]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[ollama]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[ollama]\033[0m %s\n' "$*" >&2; exit 1; }

# 新版 Homebrew 的服务名是 sh.brew.ollama，旧版是 homebrew.mxcl.ollama。
# Newer Homebrew names the job sh.brew.ollama; older versions use homebrew.mxcl.ollama.
resolve_job() {
  local f
  for f in "$AGENTS_DIR/sh.brew.ollama.plist" "$AGENTS_DIR/homebrew.mxcl.ollama.plist"; do
    if [[ -f "$f" ]]; then
      PLIST="$f"
      LABEL="$(plutil -extract Label raw -o - "$PLIST")"
      TARGET="$DOMAIN/$LABEL"
      return 0
    fi
  done
  return 1
}

# plist 不存在时让 brew 生成；即使 brew 自己加载失败，文件也会写出来，后面由本脚本加载。
# Let brew generate the plist when missing; even if brew fails to load it, the file is written
# and this script loads it afterwards.
ensure_job() {
  resolve_job && return 0
  command -v brew >/dev/null || die "未找到 brew / brew not found"
  brew list ollama >/dev/null 2>&1 || die "未安装 ollama，先执行 / ollama not installed, run: brew install ollama"
  log "未找到 LaunchAgent plist，交给 brew 生成 / generating LaunchAgent plist via brew ..."
  brew services start ollama || true
  resolve_job || die "brew 没有生成 plist / brew did not create a plist in $AGENTS_DIR"
}

api_up()    { curl -fsS --max-time 2 "$OLLAMA_URL/api/version" >/dev/null 2>&1; }
is_loaded() { launchctl print "$TARGET" >/dev/null 2>&1; }

wait_api_up() {
  local i
  for ((i = 0; i < WAIT_SECONDS; i++)); do
    api_up && return 0
    sleep 1
  done
  return 1
}

wait_api_down() {
  local i
  for ((i = 0; i < WAIT_SECONDS; i++)); do
    api_up || return 0
    sleep 1
  done
  return 1
}

job_state() {
  launchctl print "$TARGET" 2>/dev/null | grep -E $'^\t(state|pid|runs|last exit code) =' || true
}

diagnose() {
  warn "API 仍不可达 / API still unreachable: $OLLAMA_URL"
  job_state >&2
  cat >&2 <<EOF

排查 / Troubleshooting:
  1. 系统设置 → 通用 → 登录项与扩展 → 允许在后台，确认 ollama 已打开
     System Settings → General → Login Items & Extensions → Allow in the Background: enable ollama
  2. sudo sfltool dumpbtm | grep -i -B4 -A12 ollama   # 看 Disposition 是否 disabled / check for "disabled"
  3. lsof -nP -iTCP:11434 -sTCP:LISTEN                # 端口是否被别的进程占用 / port taken by another process?
  4. $0 logs                                          # 查看服务日志 / read the service log
  5. ollama serve                                     # 前台运行看报错 / run in foreground to see errors
EOF
}

cmd_start() {
  if api_up; then
    log "已在运行 / already running: $(curl -fsS "$OLLAMA_URL/api/version")"
    return 0
  fi
  ensure_job

  if ! is_loaded; then
    log "加载服务 / loading $LABEL ..."
    launchctl enable "$TARGET"
    launchctl bootstrap "$DOMAIN" "$PLIST" || true
    is_loaded || die "launchctl bootstrap 失败 / failed for $PLIST，参考 / see deploy/ollama/README.md"
  fi

  # 服务加载后 launchd 可能只把启动挂起而不执行，必须 kickstart。
  # launchd may leave a freshly loaded job pending without spawning it, so kickstart is required.
  log "启动 / kickstart $TARGET ..."
  launchctl kickstart -k "$TARGET"

  if wait_api_up; then
    log "就绪 / ready: $OLLAMA_URL $(curl -fsS "$OLLAMA_URL/api/version")"
  else
    diagnose
    exit 1
  fi
}

cmd_stop() {
  # bootout 只卸载当前会话里的服务，plist 保留，下次登录仍会自动启动。
  # bootout only unloads the job from this session; the plist stays, so it autostarts at next login.
  if resolve_job && is_loaded; then
    log "卸载服务 / unloading $TARGET ..."
    launchctl bootout "$TARGET" || warn "bootout 失败 / bootout failed"
  fi

  if pgrep -f 'ollama serve' >/dev/null; then
    log "结束残留的 ollama serve 进程 / killing leftover ollama serve processes ..."
    pkill -f 'ollama serve' || true
  fi

  if wait_api_down; then
    log "已停止 / stopped"
  else
    warn "11434 仍有服务在响应，可能是菜单栏 Ollama.app，请从菜单退出 / still answering, quit Ollama.app from the menu bar if running"
    exit 1
  fi
}

cmd_restart() {
  if resolve_job && is_loaded; then
    log "重启 / kickstart -k $TARGET ..."
    launchctl kickstart -k "$TARGET"
    if wait_api_up; then
      log "就绪 / ready: $(curl -fsS "$OLLAMA_URL/api/version")"
    else
      diagnose
      exit 1
    fi
  else
    cmd_start
  fi
}

cmd_status() {
  if api_up; then
    log "API: $OLLAMA_URL $(curl -fsS "$OLLAMA_URL/api/version")"
  else
    warn "API 不可达 / unreachable: $OLLAMA_URL"
  fi

  if resolve_job; then
    if is_loaded; then
      log "launchd: $TARGET"
      job_state
    else
      warn "launchd: $LABEL 未加载 / not loaded（执行 / run: $0 start）"
    fi
  else
    warn "未找到 LaunchAgent plist / no LaunchAgent plist in $AGENTS_DIR"
  fi

  log "端口 / port 11434:"
  lsof -nP -iTCP:11434 -sTCP:LISTEN 2>/dev/null || echo "  (无进程监听 / nothing listening)"

  if api_up; then
    log "已加载的模型 / loaded models (ollama ps):"
    ollama ps
  fi
}

cmd_logs() {
  local log_file="/opt/homebrew/var/log/ollama.log"
  if resolve_job; then
    log_file="$(plutil -extract StandardOutPath raw -o - "$PLIST" 2>/dev/null || echo "$log_file")"
  fi
  [[ -f "$log_file" ]] || die "日志不存在 / log file not found: $log_file"
  log "$log_file (Ctrl-C 退出 / to exit)"
  tail -n 50 -f "$log_file"
}

case "${1:-}" in
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  restart) cmd_restart ;;
  status)  cmd_status ;;
  logs)    cmd_logs ;;
  *)
    echo "用法 / usage: $0 {start|stop|restart|status|logs}" >&2
    exit 2
    ;;
esac
