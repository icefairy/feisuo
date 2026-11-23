#!/usr/bin/env bash
# 飞梭编译 + 部署 + 重启脚本
#
# 用法:
#   ./deploy.sh                 编译、跑测试、安装并重启
#   ./deploy.sh -s              跳过测试
#   ./deploy.sh -d              延迟重启（在飞梭会话内部执行时用，避免自己把自己杀掉）
#   ./deploy.sh --restart-only  只重启（内部使用）
#
# 可用环境变量:
#   FEISUO_BIN   安装目标，默认 /root/.local/bin/feisuo
#   FEISUO_PROG  supervisor 程序名，默认 feisuo
#   FEISUO_DELAY 延迟重启等待秒数，默认 12

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${FEISUO_BIN:-/root/.local/bin/feisuo}"
PROG="${FEISUO_PROG:-feisuo}"
DELAY="${FEISUO_DELAY:-12}"
TMP_BIN="/tmp/feisuo-build.$$"
LOG="/tmp/feisuo-restart.log"

SKIP_TEST=0
DETACH=0
RESTART_ONLY=0

usage() { sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    -s|--skip-test) SKIP_TEST=1 ;;
    -d|--detach)    DETACH=1 ;;
    --restart-only) RESTART_ONLY=1 ;;
    -h|--help)      usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage; exit 1 ;;
  esac
  shift
done

info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m✔\033[0m %s\n' "$*"; }
die()  { printf '\033[31m✘\033[0m %s\n' "$*" >&2; exit 1; }

latest_backup() { ls -1t "${BIN}".bak-* 2>/dev/null | head -1 || true; }

wait_running() {
  # 最多等 15 秒，确认服务进入 RUNNING
  for _ in $(seq 1 15); do
    if supervisorctl status "$PROG" 2>/dev/null | grep -q RUNNING; then return 0; fi
    sleep 1
  done
  return 1
}

do_restart() {
  info "重启 $PROG ..."
  supervisorctl restart "$PROG" || true
  if wait_running; then
    ok "服务已 RUNNING"
    supervisorctl status "$PROG"
    return 0
  fi

  # 启动失败 → 回滚到上一个二进制
  local bak; bak="$(latest_backup)"
  if [ -n "$bak" ]; then
    printf '\033[31m✘\033[0m 启动失败，回滚到 %s\n' "$bak" >&2
    supervisorctl stop "$PROG" >/dev/null 2>&1 || true
    cp -a "$bak" "$BIN"
    supervisorctl start "$PROG" >/dev/null 2>&1 || true
    wait_running && ok "已回滚并恢复运行" || printf '\033[31m✘\033[0m 回滚后仍未启动，请查看 /var/log/feisuo.log\n' >&2
  else
    printf '\033[31m✘\033[0m 启动失败且无可回滚备份，请查看 /var/log/feisuo.log\n' >&2
  fi
  exit 1
}

# 只重启（供延迟重启子进程调用）
if [ "$RESTART_ONLY" = "1" ]; then
  do_restart >>"$LOG" 2>&1
  exit $?
fi

command -v go >/dev/null || die "找不到 go"
command -v supervisorctl >/dev/null || die "找不到 supervisorctl"

cd "$SRC_DIR"
info "工作目录: $SRC_DIR"

if [ -d .git ]; then
  info "当前提交: $(git log --oneline -1)"
  if [ -n "$(git status --porcelain)" ]; then
    printf '\033[33m!\033[0m 存在未提交改动，将按工作区现状编译\n'
  fi
fi

trap 'rm -f "$TMP_BIN"' EXIT

info "编译 ..."
go build -o "$TMP_BIN" . || die "编译失败"
ok "编译完成 ($(du -h "$TMP_BIN" | cut -f1))"

if [ "$SKIP_TEST" = "1" ]; then
  printf '\033[33m!\033[0m 已跳过测试\n'
else
  info "运行测试 ..."
  if go test ./... 2>&1 | tail -20; then
    ok "测试通过"
  else
    die "测试失败，未部署"
  fi
fi

info "安装到 $BIN ..."
if [ -f "$BIN" ]; then
  BAK="${BIN}.bak-$(date +%Y%m%d-%H%M%S)"
  cp -a "$BIN" "$BAK"
  ok "旧版本已备份: $BAK"
fi
install -m 755 "$TMP_BIN" "$BIN"
ok "新版本已安装"

if [ "$DETACH" = "1" ]; then
  info "${DELAY} 秒后重启（日志: $LOG）"
  setsid -f sh -c "sleep $DELAY; '$SRC_DIR/deploy.sh' --restart-only" </dev/null >/dev/null 2>&1
  ok "已安排重启"
else
  do_restart
fi
