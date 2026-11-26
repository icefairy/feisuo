# 飞梭 (feisuo)

飞书机器人网关：在飞书里直接跟 AI 编程 Agent 对话。支持 **pi / CodeBuddy 双引擎**、**多会话并行**、**流式回传**、**持久会话**、**会话间协作**、**用户认证**。

> 📖 项目介绍文章：[把 AI 编程 Agent 接进飞书：我造了一个多会话网关「飞梭」](https://zhuanlan.zhihu.com/p/2109240799430959667)（[Markdown 源稿](docs/zhihu-feisuo.md)）

```
飞书消息 ──► feisuo ──► 引擎 CLI（pi / codebuddy）──► LLM
     ▲                          │
     └──────── 流式回传 ◄────────┘
```

## 特性

- **双执行引擎**：`pi` 与 `codebuddy` 可选，yaml 指定默认引擎，`/SWITCH` 运行时切换、`/MODEL` 切换模型，均立即生效并持久化
- **多会话并行**：每个会话独立队列 + **独立常驻 CLI 进程**（会话之间上下文完全隔离）
- **持久会话**：会话状态 + 对话历史落盘，**飞梭异常终止/重启后自动恢复**（未显式关闭的会话全部复活，`/KILL` 过的保持关闭）
- **流式回传**：引擎的文本增量逐块发回飞书，实时可见；1.5s 间隔 + 15 次编辑预算 + 自适应退避 + 自动降级
- **当前会话**：`/TO <会话>` 切换当前对话的会话（可带消息直接发送），普通消息默认发往当前会话，无需每次指定
- **会话间协作（bridge）**：Agent 可输出 `<bridge to="会话">消息</bridge>` 把任务转发给其他会话，支持链式 A→B→C，深度 5 防环
- **会话角色**：每个会话可自定义系统提示词（人设/职责），`/NEW fanyi --role "资深翻译"` 或语义创建
- **按会话工作目录**：每个会话可绑定一个项目目录，Agent 进程在该目录内读写文件/执行命令（`/NEW repo --dir /path` 或 `/CD repo /path`），`pi` 与 `codebuddy` 引擎均支持
- **忙碌打断**：会话执行长任务时，直接发的新消息会以 steer 方式打断并注入（`pi` 透传 steer，`codebuddy` 先 interrupt 再发），不用等任务跑完
- **任务控制**：`/STOP` 打断当前任务（不销毁会话）、`/RESET` 清空上下文、`/COMPACT` 压缩上下文（pi 引擎）
- **用户认证**：飞书用户白名单制，未授权用户拒绝一切操作并提示授权方式，管理员 CLI 一键批准
- **本地交互**：终端 `feisuo>` 提示符可直接发任务，命令语法与飞书一致
- **会话介入（attach）**：`feisuo attach <会话>` 把终端挂到任意会话，实时看收发、可手动输入（tmux/多终端并行介入多会话）
- **会话状态**：idle / busy / done / error / stopped 五态，`/STATUS` 一览
- **闲置回收**：会话闲置超过 `sessions.idle_timeout`（默认 1 小时）自动停掉其常驻 CLI 进程释放资源，下次消息自动重启且上下文不丢
- **WS 自愈**：应用层 WS 健康检查，连接僵死且超过阈值未恢复时自动退出进程，由 supervisor 重启重建（有任务在跑时自动避让，不误杀长任务）
- **跨平台**：Linux / Windows 双平台二进制（Windows 用本地 TCP 替代 Unix socket）
- **零依赖部署**：单二进制 + yaml 配置，supervisor / NSSM 托管

## 环境准备

### 1. Go

需要 Go 1.23+（仅编译时需要）。

### 2. 执行引擎（二选一或都装）

**pi**

```bash
npm i -g @earendil-works/pi-coding-agent   # 需要 Node.js ≥ 20
pi --version
pi auth                                    # 登录 / 配置 provider 与凭据
```

**CodeBuddy CLI**

安装 CodeBuddy CLI 并完成本机登录即可。飞梭不保存 codebuddy 的凭据与模型，直接复用本机登录状态；以 headless 权限全通过模式运行（`-y --permission-mode bypassPermissions` + `CODEBUDDY_IS_SANDBOX=1`），无人值守下不会弹审批。

> **模型与凭据不在飞梭配置**：pi 引擎复用 `pi auth` / models.json 的配置，codebuddy 引擎复用本机登录凭据。yaml 中的 `model` / `api_key` / `provider` 均为可选项，留空即用引擎自身配置。

### 3. 准备飞书应用

1. 在 [飞书开放平台](https://open.feishu.cn/app) 创建企业自建应用
2. 获取 `App ID` 和 `App Secret`
3. 添加机器人能力，获取 WebSocket 长连接端点（`wss://open.feishu.cn/ws/v1/ws/...`）
4. 发布应用版本并开启可用性

## 快速开始

### 1. 配置

```bash
cp feisuo.yaml.example feisuo.yaml
vim feisuo.yaml   # 填入飞书凭证；引擎与路径按需调整
```

```yaml
engine: "codebuddy"   # 默认引擎：pi | codebuddy（默认 pi）

feishu:
  app_id: "cli_xxxxxxxxxx"      # 飞书应用 App ID
  app_secret: "xxxxxxxxxxxxxxx" # 飞书应用 App Secret
  bot_name: "飞梭"
  ws_url: "wss://open.feishu.cn/ws/v1/ws/"
  ws_health_timeout: 600        # WS 僵死自愈阈值（秒），默认 600；0=禁用

pi:                    # engine: pi 时使用
  binary: "pi"         # pi CLI 路径；provider/model/api_key 可选，空则用 pi 自身配置
  timeout: 1800        # 单任务超时（秒）；0=不设超时

codebuddy:             # engine: codebuddy 时使用；本机登录凭据，无需 api_key
  binary: "codebuddy"
  model: ""            # 可选，空=codebuddy 默认模型
  timeout: 1800

sessions:
  data_dir: "~/.feisuo/sessions" # 会话状态 + 授权列表存储
  default_name: "main"           # 默认会话名
  idle_timeout: 3600             # 闲置回收（秒）：无活动超时则停掉该会话常驻 CLI 进程，下次消息自动重启；0=关闭
```

### 2. 编译 & 运行

```bash
go build -o feisuo .
./feisuo feisuo.yaml
```

首次运行后按提示授权自己：`./feisuo approve <你的用户 ID>`。

## 执行引擎

飞梭把引擎抽象成统一接口（Execute / Steer / Abort / Reset / Kill），`pi` 与 `codebuddy` 行为一致：

| 能力 | pi | codebuddy |
|---|---|---|
| 常驻进程 + 流式事件 | ✅ RPC / NDJSON | ✅ stream-json |
| 按会话隔离上下文 | ✅ `--session-id feisuo-<会话>` | ✅ `--session-id feisuo-<会话>` |
| 运行中打断（steer） | ✅ | ✅ interrupt + 新消息 |
| 任务中止 / 重置 | ✅ | ✅ |
| 上下文压缩 | ✅ `/COMPACT` | — |
| 工作目录 | ✅ `cmd.Dir` | ✅ `cmd.Dir` |
| 权限模式 | 沿用 pi 配置 | headless 全通过，不弹审批 |

**引擎选择优先级**：`/SWITCH` 持久化的选择 > yaml 的 `engine` > 默认 `pi`。
**模型选择优先级**：`/MODEL` 持久化的选择 > 当前引擎 yaml 的 `model` > 引擎自身默认。
切换引擎或模型会重建引擎进程，正在执行的任务会中断，但会话上下文按固定的 `session-id` 恢复。

## 按会话工作目录

默认会话（以及未设置的会话）的 Agent 进程运行在 feisuo 的启动目录。要让某个会话在指定仓库里做开发，给它绑定工作目录即可：

```
/NEW repo --dir /data/codes/myproject     # 创建时绑定
/CD  repo /data/codes/myproject           # 对已有会话设置/修改
/CD  repo                                  # 查看
```

路径支持 `~/...` 与相对路径（解析为绝对路径），目录必须已存在。绑定后该会话的 Agent 进程以该目录为 `cmd.Dir`，文件读写、git、shell 命令都在目标仓库内执行。修改工作目录会结束该会话当前进程，下次消息自动用新目录重建（与 `/ROLE` 行为一致）。工作目录随会话状态落盘，飞梭重启后自动恢复。`/STATUS` 中用 📁 标记已绑定目录的会话。

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
| `你好` | 发给当前会话（默认 `main`，`/TO` 切换后发往新会话） |
| `/TO 分析` | 切换到会话 `分析`（后续普通消息发往这里） |
| `/TO 分析 看看这个项目` | 切换到会话 `分析` 并发送消息 |
| `/NEW repo --dir /data/codes/foo` | 创建会话并绑定项目目录（可同时带 `--role`） |
| `/CD repo /data/codes/foo` | 设置会话工作目录（下次消息生效，`~` 与相对路径均可） |
| `/CD repo` | 查看会话当前工作目录 |
| `/ROLE 排障 --role "改成负责代码审查"` | 修改会话角色（下次消息生效） |
| `/ROLE 排障` | 查看会话角色 |
| `/LIST` | 列出所有会话（含角色） |
| `/STATUS` | 查看各会话状态 |
| `/KILL 分析` | 终止会话（真正杀 CLI 进程；下次消息自动复活） |
| `/STOP 分析` | 打断当前任务（不销毁会话，上下文保留） |
| `/RESET 分析` | 重置会话（清空对话上下文，进程按需重建） |
| `/COMPACT 分析` | 压缩会话上下文（pi 引擎：保留关键信息、降低 token 占用） |
| `/NAME 旧名 新名` | 重命名会话 |
| `/SWITCH pi` | 运行时切换执行引擎为 pi（立即生效，重启后保留） |
| `/SWITCH codebuddy` 或 `/SWITCH cb` | 运行时切换执行引擎为 CodeBuddy（立即生效，重启后保留） |
| `/MODEL` | 查看当前引擎与模型 |
| `/MODEL deepseek-v4.1-flash` | 设置全局模型（立即生效，重启后保留） |
| `/MODEL reset` | 恢复配置文件默认模型 |
| `/HELP` | 查看帮助 |

会话执行中时直接发新消息会**打断当前任务**并立即处理（steer），适合纠正方向或补充信息；`/STOP` 则是单纯打断，不追加内容。

### 会话间协作（bridge）

Agent 在回复中可输出协作标记，feisuo 自动转发给目标会话，目标会话的回复流式回传给同一用户：

```
<bridge to="ice">请帮我查一下这个 API 文档</bridge>
```

- 支持链式：A → B → C
- 防环：最大转发深度 5，自转发忽略，目标不存在报错
- 引擎启动时自动注入协议说明 + 当前会话列表

### 在终端

```bash
$ ./feisuo feisuo.yaml
飞梭 feisuo 启动
  飞书: 飞梭 | 默认会话: main
  引擎: CodeBuddy（codebuddy 默认模型）
  命令: /TO <会话> <消息> | /NEW <名> | /LIST | /STATUS | /KILL <名> | /RESET <名>
  Ctrl+C 退出
feisuo> /NEW 排障
feisuo> /TO 排障 看看这个项目的日志报错
```

### 介入会话（attach）

`feisuo attach <会话>` 把当前终端挂到指定会话：**实时看到该会话的收发，直接输入即可发送**（输入走会话队列，会话忙时自动排队不打断）。飞书与 attach 操作的是同一个会话，两边实时可见。

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

> **attach 只是监视+输入终端**：退出它只断开连接，CLI 进程、消息队列、上下文全部保留。会话只在 `/KILL`（杀进程，上下文保留）或 `/RESET`（清空上下文）时真正终止。

## 架构

```
┌──────────┐    ┌──────────────────────────── feisuo ────────────────────────────┐
│  飞书    │    │  ┌──────────┐  ┌─────────┐  ┌─────────┐                        │
│ 用户消息  │───►│  │ 认证门卫  │─►│ 命令解析  │─►│ 当前会话  │──► 默认会话 main      │
└──────────┘    │  └──────────┘  └─────────┘  └─────────┘   │                     │
                │        │            │            │        │  ┌────────────────┐ │
                │        ▼            ▼            ▼        ├─►│ 会话 ice       │ │
                │  authorized.json  /NEW 等命令  /TO 切换    │  │ 会话 work      │ │
                │        │            │            │        │  └────────────────┘ │
                │        └──── 会话间协作 (bridge) ◄────────┘    每会话一个常驻 CLI │
                │                                                     ▲           │
                │                              引擎层 (pi / codebuddy)│流式回传    │
                └─────────────────────────────────────────────────────┴───────────┘
```

- **认证门卫**：检查飞书用户 ID 是否在 authorized.json 白名单，未授权拒绝一切操作
- **会话**：每个会话 = 队列 + 常驻引擎进程；状态/角色/工作目录 JSON 落盘，重启自动恢复
- **引擎层**：统一接口屏蔽 pi / codebuddy 差异，支持运行时切换；进程用固定 `--session-id feisuo-<会话名>` 启动，对话历史跨重启保留
- **生命周期**：闲置超过 `idle_timeout` 自动停掉空闲进程；进程异常时下次消息自动重连
- **watchdog**：基于连接状态的 WS 健康检查，断连超阈值未恢复则自愈退出；只按连接状态判活，长任务期间不误判
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

> 飞梭内置 WS 僵死自愈：疑似僵死时进程自行退出，supervisor 的 `autorestart` 会立即拉起重建。

## Windows 部署

飞梭支持 Windows 原生运行（Go 跨平台）。与 Linux 的唯一架构差异：**attach 通道用本地 TCP（127.0.0.1:52821）替代 Unix socket**，其他功能完全一致。

### 1. 编译 Windows 二进制

```bash
# Linux 上交叉编译
GOOS=windows GOARCH=amd64 go build -o feisuo.exe .
# 或 Windows 本机 go build -o feisuo.exe .
```

### 2. 安装引擎

```powershell
# pi：需要 Node.js ≥ 20
npm i -g @earendil-works/pi-coding-agent
pi --version

# 或安装 CodeBuddy CLI 并登录，engine: codebuddy 即可
```

### 3. 配置

`feisuo.yaml` 同 Linux，注意 `binary` 填 Windows 命令（如 `pi.cmd`、`codebuddy.cmd`，代码会自动 LookPath 解析，也可直接写完整路径）：

```yaml
pi:
  binary: "pi.cmd"   # 或 C:\Users\你\AppData\Roaming\npm\pi.cmd
sessions:
  data_dir: "C:\Users\你\.feisuo\sessions"   # ~/.feisuo/sessions 也支持
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
- **/KILL 杀进程树**：Windows 上用 `taskkill /T /F` 杀掉 CLI 及其子进程，避免僵尸残留
- **工具链**：pi / codebuddy 执行任务依赖 git/bash 等命令，请安装 [Git for Windows](https://git-scm.com/download/win)（自带 bash）
- **路径分隔符**：data_dir 用 Windows 格式（`C:\...`）或 `~/...` 均可，代码自动适配

## 常见问题

**进程占内存怎么办？**
默认闲置 1 小时自动回收（`sessions.idle_timeout`）。回收后再发消息自动重启，上下文不丢。也可手动 `/KILL <会话>`。

**换模型后上下文还在吗？**
在。`/MODEL`、`/SWITCH` 会重建引擎进程，但会话使用固定的 `session-id`，重启后按原会话恢复历史。

**需要给 codebuddy 配 api_key 吗？**
不需要。飞梭复用本机 `codebuddy` 登录凭据；仅当你想临时覆盖时才在 yaml 写 `codebuddy.api_key`。

**飞书后台显示机器人离线 / 消息不响应？**
飞梭内置 WS 健康检查：疑似僵死会自动退出并由 supervisor 重启。请确保用 supervisor/NSSM 托管而不是裸跑。
