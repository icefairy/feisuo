# 飞梭 (feisuo)

飞书机器人网关：在飞书里直接跟 pi Agent 对话，支持**多会话并行**、**流式回传**、**持久会话**、**会话间协作**、**用户认证**。

```
飞书消息 ──► feisuo ──► pi CLI ──► LLM
     ▲                    │
     └────── 流式回传 ◄────┘
```

## 特性

- **多会话并行**：每个会话独立队列 + **独立常驻 pi 进程**（会话之间上下文完全隔离）
- **持久会话**：会话状态 + pi 对话历史落盘，**飞梭异常终止/重启后自动恢复**（未显式关闭的会话全部复活，`/KILL` 过的保持关闭）
- **流式回传**：pi 的文本增量逐块发回飞书，实时可见；1.5s 间隔 + 15 次编辑预算 + 自适应退避 + 自动降级
- **意图路由**：三层路由——精确命令 > 显式前缀（`main: xxx` / `@ice xxx`）> LLM 语义解析（glm4:9b），普通消息也能管理会话
- **会话间协作（bridge）**：pi 可输出 `<bridge to="会话">消息</bridge>` 把任务转发给其他会话，支持链式 A→B→C，深度 5 防环
- **会话角色**：每个会话可自定义系统提示词（人设/职责），`/NEW fanyi --role "资深翻译"` 或语义创建
- **用户认证**：飞书用户白名单制，未授权用户拒绝一切操作并提示授权方式，管理员 CLI 一键批准
- **本地交互**：终端 `feisuo>` 提示符可直接发任务，命令语法与飞书一致
- **会话介入（attach）**：`feisuo attach <会话>` 把终端挂到任意会话，实时看收发、可手动输入（tmux/多终端并行介入多会话）
- **会话状态**：idle / busy / done / error / stopped 五态，`/STATUS` 一览
- **跨平台**：Linux / Windows 双平台二进制（Windows 用本地 TCP 替代 Unix socket）
- **零依赖部署**：单二进制 + yaml 配置，supervisor 托管

## 快速开始

### 1. 准备飞书应用

1. 在 [飞书开放平台](https://open.feishu.cn/app) 创建企业自建应用
2. 获取 `App ID` 和 `App Secret`
3. 添加机器人能力，获取 WebSocket 长连接端点（`wss://open.feishu.cn/ws/v1/ws/...`）
4. 发布应用版本并开启可用性

### 2. 配置

```bash
cp feisuo.yaml.example feisuo.yaml
vim feisuo.yaml   # 填入飞书凭证 + pi 参数
```

```yaml
feishu:
  app_id: "cli_xxxxxxxxxx"      # 飞书应用 App ID
  app_secret: "xxxxxxxxxxxxxxx" # 飞书应用 App Secret
  bot_name: "飞梭"
  ws_url: "wss://open.feishu.cn/ws/v1/ws/"

pi:
  binary: "pi"                  # pi CLI 路径
  provider: "agentrium"   # 与 pi auth/models.json 中配置的 provider 名一致
  model: "deepseek-v4-flash"
  base_url: "http://127.0.0.1:3000/v1"   # OpenAI 兼容端点
  api_key: "your-api-key-here"
  timeout: 300                  # 单任务超时（秒）

sessions:
  data_dir: "~/.feisuo/sessions" # 会话状态 + 授权列表存储
  default_name: "main"           # 默认会话名

intent:
  enabled: true                  # 意图路由开关
  model: "glm4:9b"               # 语义路由模型
  base_url: "http://127.0.0.1:3002/v1"
  api_key: "your-intent-key"     # 语义路由模型 API key
  timeout: 8
```

### 3. 编译 & 运行

```bash
go build -o feisuo .
./feisuo feisuo.yaml
```

## 用户认证

飞梭默认**只对授权用户开放**。授权列表存于 `~/.feisuo/sessions/authorized.json`，命令行即时生效。

### 未授权用户流程

1. 新用户给机器人发消息 → 收到拒绝回复，提示其用户 ID 与授权命令
2. 用户把 ID 告诉管理员
3. 管理员执行 `feisuo approve <用户ID>` → **立即生效**，无需重启

### 授权管理命令

```bash
feisuo approve <用户ID>    # 授权用户（立即生效）
feisuo revoke <用户ID>     # 撤销授权
feisuo list-auth           # 查看已授权 + 待授权申请列表
```

未授权用户每次发消息都会记录到 `pending` 列表（`feisuo list-auth` 可查看申请者）。

> 首次部署时请先 `feisuo approve <自己的用户ID>` 预授权，否则所有消息都会被拦截。

## 使用方式

### 在飞书里

| 输入 | 效果 |
|---|---|
| `你好` | 发给默认会话 `main` |
| `/TO 分析 看看这个项目` | 发给指定会话 `分析` |
| `/NEW 排障 --role "负责排查线上问题"` | 创建会话（可带角色） |
| `/ROLE 排障 --role "改成负责代码审查"` | 修改会话角色（下次消息生效） |
| `/ROLE 排障` | 查看会话角色 |
| `/LIST` | 列出所有会话（含角色） |
| `/STATUS` | 查看各会话状态 |
| `/KILL 分析` | 终止会话（真正杀 pi 进程；下次消息自动复活） |
| `/RESET 分析` | 重置会话（透传 pi `/new`：清空对话上下文，进程不重启） |
| `/NAME 旧名 新名` | 重命名会话 |
| `/HELP` | 查看帮助 |
| `main: 帮我查资料` | 显式前缀，强制路由到指定会话 |
| `有哪些会话` | 语义管理：列出会话 |
| `新建一个会话叫 ops，负责运维` | 语义管理：创建会话（带角色） |
| `把 ice 关了吧` | 语义管理：关闭会话 |

### 会话间协作（bridge）

pi 在回复中可输出协作标记，feisuo 自动转发给目标会话，目标会话的回复流式回传给同一用户：

```
<bridge to="ice">请帮我查一下这个 API 文档</bridge>
```

- 支持链式：A → B → C
- 防环：最大转发深度 5，自转发忽略，目标不存在报错
- pi 启动时自动注入协议说明 + 当前会话列表

### 在终端

```bash
$ ./feisuo feisuo.yaml
飞梭 feisuo 启动
  飞书: 飞梭 | 默认会话: main
  Pi: deepseek-v4-flash @ http://127.0.0.1:3000/v1
  命令: /TO <会话> <消息> | /NEW <名> | /LIST | /STATUS | /KILL <名> | /RESET <名>
  Ctrl+C 退出
feisuo> /NEW 排障
feisuo> /TO 排障 看看这个项目的日志报错
```

### 介入会话（attach）

`feisuo attach <会话>` 把当前终端挂到指定会话：**实时看到该会话的收发，直接输入即可发送**（输入走 pi 原生 followUp 队列，会话忙时自动排队不打断）。飞书与 attach 操作的是同一个会话，两边实时可见。

```bash
# 挂到会话 ice
$ feisuo attach ice
👤 [ice] 帮我查一下这个 API
🤖 [ice] 好的，正在查看…
✅ [ice] 已查完，结论如下：…

# 双通道模式：回复同时发飞书指定用户 + 本地显示
$ feisuo attach ice --as ou_xxxxxxx

# 退出 attach（不影响会话本体，随时可重新挂）
$ exit   # 或 Ctrl+C
```

用 tmux 并行介入多个会话：

```bash
tmux new -s feisuo
tmux split-window -h    # 分窗
tmux split-window -v
# 每个 pane 里 attach 一个会话
feisuo attach ice
feisuo attach work
feisuo attach main
```

> **attach 只是监视+输入终端**：退出它只断开连接，pi 进程、消息队列、上下文全部保留。会话只在 `/KILL`（杀进程，上下文保留）或 `/RESET`（清空上下文）时真正终止。

## 架构

```
┌──────────┐    ┌──────────────────────────── feisuo ────────────────────────────┐
│  飞书    │    │  ┌──────────┐  ┌─────────┐  ┌─────────┐                        │
│ 用户消息  │───►│  │ 认证门卫  │─►│ 命令解析  │─►│ 意图路由  │──► 默认会话 main      │
└──────────┘    │  └──────────┘  └─────────┘  └─────────┘   │                     │
                │        │            │            │        │  ┌──────────────┐  │
                │        ▼            ▼            ▼        ├─►│ 会话 ice (pi) │  │
                │  authorized.json  /NEW 等命令  语义路由    │  │ 会话 work(pi) │  │
                │        │            │            │        │  └──────────────┘  │
                │        └──── 会话间协作 (bridge) ◄────────┘         ▲           │
                │                                                     │流式回传   │
                └─────────────────────────────────────────────────────┴───────────┘
```

- **认证门卫**：检查飞书用户 ID 是否在 authorized.json 白名单，未授权拒绝一切操作
- **会话**：每个会话 = 队列 + 常驻 pi RPC 进程；状态/角色 JSON 落盘，重启自动恢复
- **持久化**：pi 进程用固定 `--session-id feisuo-<会话名>` 启动，对话历史跨重启保留
- **bridge**：流式输出中解析 `<bridge to="x">msg</bridge>`，剔除标记后转发，深度 5 防环

## 部署（supervisor）

```ini
[program:feisuo]
command=/root/.local/bin/feisuo feisuo.yaml
directory=/etc/feisuo
autostart=true
autorestart=true
startsecs=3
startretries=5
redirect_stderr=true
stdout_logfile=/var/log/feisuo.log
```

更新版本：

```bash
go build -o feisuo .
supervisorctl stop feisuo
cp feisuo /root/.local/bin/feisuo
supervisorctl start feisuo
```

## Windows 部署

飞梭支持 Windows 原生运行（Go 跨平台 + pi 是 Node CLI）。与 Linux 的唯一架构差异：**attach 通道用本地 TCP（127.0.0.1:52821）替代 Unix socket**，其他功能完全一致。

### 1. 编译 Windows 二进制

```bash
# Linux 上交叉编译
GOOS=windows GOARCH=amd64 go build -o feisuo.exe .
# 或 Windows 本机 go build -o feisuo.exe .
```

### 2. 安装 pi

```powershell
# 需要 Node.js ≥ 20
npm i -g @earendil-works/pi-coding-agent
# 验证：pi --version
```

### 3. 配置

`feisuo.yaml` 同 Linux，注意 `pi.binary` 填 `pi.cmd`（Windows npm 全局安装生成的命令，代码会自动 LookPath 解析，也可直接写完整路径）：

```yaml
pi:
  binary: "pi.cmd"   # 或 C:\\Users\\你\\AppData\\Roaming\\npm\\pi.cmd
sessions:
  data_dir: "C:\\Users\\你\\.feisuo\\sessions"   # ~/.feisuo/sessions 也支持
```

### 4. 运行

```powershell
# 前台（Ctrl+C 优雅退出）
feisuo.exe feisuo.yaml

# 介入会话（Windows Terminal 多标签页替代 tmux）
feisuo.exe attach ice --as ou_xxxxxxx
```

### 5. 服务托管（NSSM）

把 feisuo 注册为 Windows 服务，开机自启 + 崩溃自动重启：

```powershell
nssm install feisuo "C:\feisuo\feisuo.exe" "C:\feisuo\feisuo.yaml"
nssm set feisuo AppDirectory "C:\feisuo"
nssm set feisuo AppStdout "C:\feisuo\feisuo.log"
nssm set feisuo AppStderr "C:\feisuo\feisuo.err.log"
nssm set feisuo Start SERVICE_AUTO_START
nssm start feisuo
```

### Windows 注意事项

- **attach 端口固定 52821**：一台机器只跑一个 feisuo 实例；要改端口，编辑 `internal/socket/socket.go` 的 `winTCPAddr` 常量后重新编译
- **/KILL 杀进程树**：Windows 上用 `taskkill /T /F` 杀掉 pi.cmd 及其 node 子进程，避免僵尸残留
- **pi 工具链**：pi 执行任务依赖 git/bash 等命令，请安装 [Git for Windows](https://git-scm.com/download/win)（自带 bash）
- **路径分隔符**：data_dir 用 Windows 格式（`C:\...`）或 `~/...` 均可，代码自动适配
