#!/usr/bin/env bash
#
# 构建并以 remote 模式启动 mcp-server-mysql。
# Build and start mcp-server-mysql in remote mode.
#
# 用法 / Usage:
#   ./deploy/mcp-mysql/run-server.sh              # 重建演示库 + 按需构建 + 启动
#   ./deploy/mcp-mysql/run-server.sh --rebuild    # 强制重新构建
#   ./deploy/mcp-mysql/run-server.sh --no-seed    # 跳过重建演示库
#
# 源码目录可用 MCP_MYSQL_SRC 覆盖。
# The source directory can be overridden with MCP_MYSQL_SRC.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="${MCP_MYSQL_SRC:-/Users/bytedance/Documents/typescript_projects/github/mcp-server-mysql}"
ENV_FILE="$SCRIPT_DIR/.env"
SEED_FILE="$SCRIPT_DIR/travel_demo.sql"

REBUILD=false
SEED=true
for arg in "$@"; do
  case "$arg" in
    --rebuild) REBUILD=true ;;
    --no-seed) SEED=false ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

log() { printf '\033[1;34m[mcp]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[mcp]\033[0m %s\n' "$*" >&2; exit 1; }

# ---- 前置检查 / Preflight ----
[[ -d "$SRC_DIR" ]] || die "源码目录不存在: $SRC_DIR (可用 MCP_MYSQL_SRC 覆盖)"
command -v node >/dev/null || die "未找到 node"
command -v pnpm >/dev/null || die "未找到 pnpm"

if [[ ! -f "$ENV_FILE" ]]; then
  die "缺少 $ENV_FILE，请先执行：cp $SCRIPT_DIR/.env.example $ENV_FILE 并填入 REMOTE_SECRET_KEY"
fi

# ---- 造数据 / Seed ----
# 每次启动都重建：出行日期是 INSERT 时按 CURDATE() 求值后存成绝对日期的，
# 种下去超过一周，“未来 7 天”的查询就会静默返回空集。重建只要几百毫秒。
# Reseed on every start: depart dates are absolute values evaluated from CURDATE() at INSERT time,
# so data older than a week makes "next 7 days" queries silently return nothing. Reseeding costs ~200ms.
if [[ "$SEED" == true ]]; then
  command -v mysql >/dev/null || die "未找到 mysql 客户端"
  log "重建演示库 travel_demo ..."
  mysql -uroot < "$SEED_FILE"
  log "演示库就绪（数据日期已对齐到今天）"
fi

# ---- 构建 / Build ----
# dist 比源码旧时才重建，避免每次启动都等一遍 tsc。
# Rebuild only when dist is older than the sources, so startup isn't blocked by tsc every time.
cd "$SRC_DIR"
if [[ ! -d node_modules ]]; then
  log "安装依赖 (pnpm install) ..."
  pnpm install
fi

needs_build=false
if [[ "$REBUILD" == true || ! -f dist/index.js ]]; then
  needs_build=true
elif [[ -n "$(find index.ts src -newer dist/index.js -name '*.ts' -print -quit 2>/dev/null)" ]]; then
  needs_build=true
fi

if [[ "$needs_build" == true ]]; then
  log "构建 (pnpm run build) ..."
  pnpm run build
else
  log "dist 已是最新，跳过构建"
fi

# ---- 加载配置 / Load config ----
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

[[ "${IS_REMOTE_MCP:-}" == "true" ]] || die "IS_REMOTE_MCP 必须为 true，否则不会监听 HTTP 端口"
[[ -n "${REMOTE_SECRET_KEY:-}" ]] || die "REMOTE_SECRET_KEY 不能为空，否则 server 会退回 stdio 模式"

# ---- 启动 / Start ----
log "MySQL: ${MYSQL_USER}@${MYSQL_HOST}:${MYSQL_PORT}/${MYSQL_DB}"
log "监听:  http://127.0.0.1:${PORT:-3000}/mcp  (Bearer 鉴权)"
log "退出:  Ctrl-C"
echo
exec node dist/index.js
