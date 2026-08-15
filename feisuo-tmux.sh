#!/bin/bash
# 飞梭 tmux 启动脚本
# 用法: ./feisuo-tmux.sh [feisuo.yaml 路径]

CONFIG="${1:-feisuo.yaml}"
LOG_DIR="$HOME/.feisuo"
LOG_FILE="$LOG_DIR/feisuo.log"
PID_FILE="$LOG_DIR/feisuo.pid"

mkdir -p "$LOG_DIR"

# 终止旧进程
if [ -f "$PID_FILE" ]; then
    kill $(cat "$PID_FILE") 2>/dev/null
    sleep 1
fi

# 启动 feisuo 后台（日志写文件而不是终端）
cd "$(dirname "$0")"
nohup ./feisuo "$CONFIG" > "$LOG_FILE" 2>&1 &
echo $! > "$PID_FILE"
echo "feisuo PID: $(cat $PID_FILE)"

# 清理旧 tmux 会话
tmux kill-session -t feisuo 2>/dev/null

# 创建新 tmux 会话
tmux new-session -d -s feisuo -n "feisuo" -x 160 -y 48

# 窗口1: 实时日志
tmux send-keys -t feisuo:1 "clear && echo '=== 飞梭 feisuo 实时日志 ===' && echo 'Ctrl+B 1 → 日志 | Ctrl+B 2 → 会话 | Ctrl+B 3 → 输入' && echo '============================' && tail -f $LOG_FILE" Enter

# 窗口2: 会话状态
tmux new-window -t feisuo:2 -n "sessions"
tmux send-keys -t feisuo:2 "watch -n 2 'ls -la $LOG_DIR/sessions/ 2>/dev/null && echo --- && tail -5 $LOG_FILE | grep -E \"\\[session\\]|\\[pi/\"'" Enter

# 窗口3: 本地交互
tmux new-window -t feisuo:3 -n "input"
tmux send-keys -t feisuo:3 "echo '飞梭本地交互模式' && echo '输入命令: /TO <会话> <消息> | /NEW | /LIST | /KILL | /HELP' && echo 'Ctrl+C 退出交互模式' && echo '---' && echo 'main' | ./feisuo" Enter

# 附加到 tmux
tmux select-window -t feisuo:1
tmux attach-session -t feisuo

# 退出时清理
echo "正在停止 feisuo..."
kill $(cat "$PID_FILE") 2>/dev/null
rm -f "$PID_FILE"