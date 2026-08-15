# 飞梭 (feisuo)

飞书机器人网关：在飞书里直接跟 pi Agent 对话，支持**多会话并行**、**流式回传**、**本地交互**。

```
飞书消息 ──► feisuo ──► pi CLI ──► LLM
     ▲                    │
     └────── 流式回传 ◄────┘
```

## 特性

- **多会话并行**：每个会话独立队列 + **独立常驻 pi 进程**（会话之间上下文完全隔离）
- **上下文延续**：同一会话的消息都发给同一个 `pi --mode rpc` 进程，agent 记得之前的对话（进程内记忆）
- **流式回传**：pi 的文本增量逐块发回飞书，实时可见
- **本地交互**：终端 `feisuo>` 提示符可直接发任务，命令语法与飞书一致
- **会话状态**：idle / busy / done / error / stopped 五态，`/STATUS` 一览
- **持久化**：会话状态 JSON 落盘 `~/.feisuo/sessions/`，重启可查
- **零依赖部署**：单二进制 + yaml 配置，附带 tmux 一键启动脚本

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
  data_dir: "~/.feisuo/sessions" # 会话状态存储
  default_name: "main"           # 默认会话名
```

### 3. 编译 & 运行

```bash
go build -o feisuo .
./feisuo feisuo.yaml
```

或使用 tmux 脚本一键启动（日志 / 会话状态 / 交互三窗口）：

```bash
./feisuo-tmux.sh feisuo.yaml
```

## 使用方式

### 在飞书里

| 输入 | 效果 |
|---|---|
| `你好` | 发给默认会话 `main` |
| `/TO 分析 看看这个项目` | 发给指定会话 `分析` |
| `/NEW 排障` | 创建新会话 |
| `/LIST` | 列出所有会话 |
| `/STATUS` | 查看各会话状态 |
| `/KILL 分析` | 终止会话 |
| `/NAME 旧名 新名` | 重命名会话 |
| `/HELP` | 查看帮助 |

> 发给不存在的会话时，feisuo 会自动创建并立即处理。

### 在终端

```bash
$ ./feisuo feisuo.yaml
飞梭 feisuo 启动
  飞书: 飞梭 | 默认会话: main
  Pi: deepseek-v4-flash @ http://127.0.0.1:3000/v1
  命令: /TO <会话> <消息> | /NEW <名> | /LIST | /STATUS | /KILL <名>
  Ctrl+C 退出
feisuo> /NEW 排障
feisuo> /TO 排障 看看这个项目的日志报错
```

## 架构

```
main.go            装配：config → store → session → pi → feishu
internal/
  config/           yaml 配置加载 + 默认值 + ~ 展开
  feishu/           飞书 WebSocket 长连接、tenant_access_token 管理、消息收发
  pi/               pi CLI 子进程封装（流式 stdout / 超时控制）
  protocol/         命令解析（/TO /NEW /LIST /STATUS /KILL /NAME /HELP）
  session/          会话管理器（队列 + 状态机 + 并发安全）
  store/            会话状态 JSON 持久化
```

## 命令速查

```
/TO <会话> <消息>   发给指定会话（会话不存在则自动创建）
/NEW <名称>         创建新会话
/LIST / /STATUS     列出/查看会话状态
/KILL <会话>        终止会话（状态置 stopped，队列清空）
/NAME <旧> <新>     重命名会话
/HELP               帮助
```

## 注意事项

- **token 管理**：tenant_access_token 自动获取并在过期前（2h）后台刷新
- **消息长度**：飞书单条消息超 1500 字符会被截断（流式分块已处理）
- **pi 依赖**：需要预先安装 [pi Agent](https://github.com/icefairy/pi-agent) 并配置好上游 LLM
- **超时**：单个任务默认 300 秒超时，可在 yaml 调整
