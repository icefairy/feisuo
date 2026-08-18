package pi

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Binary   string
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Timeout  int
	// SessionID 指定 pi 会话 id（持久化对话历史，重启后恢复上下文）。
	// 为空则不传（每次全新会话）。
	SessionID string
	// AppendSystemPrompt 追加到 system prompt（会话协作协议等），可空
	AppendSystemPrompt string
}

type Message struct {
	Content string
	Sender  string
}

// Engine 封装 pi RPC 常驻进程。
// 每个会话持有一个常驻 pi 进程：首次收到该会话消息时启动，
// 之后所有消息通过 stdin 发 JSON 命令，流式事件从 stdout 读，
// agent_settled 事件触发完成通知。会话之间上下文完全隔离。
type Engine struct {
	cfg Config

	// SessionList 返回当前会话名列表（用于动态生成 bridge 协作协议注入 prompt）。
	// 由 main 注入；nil 则不注入 bridge 协议。
	SessionList func() []string
	// SystemPromptOf 返回指定会话的自定义系统提示词（角色），用于启动 pi 时注入。
	// 可为 nil（不注入角色）。
	SystemPromptOf func(session string) string

	mu      sync.Mutex
	clients map[string]*rpcClient // session name -> 常驻进程
	seq     int
}

func New(cfg Config) *Engine {
	return &Engine{cfg: cfg, clients: make(map[string]*rpcClient)}
}

// Execute 向指定会话的常驻 pi 进程发送 prompt。
// 会话首次使用时自动启动进程；onChunk 收到流式文本 delta，
// onDone 收到完整回答或错误。
func (e *Engine) Execute(ctx context.Context, session, prompt string,
	onChunk func(string), onDone func(string, error)) {

	timeout := e.cfg.Timeout
	if timeout == 0 {
		timeout = 1800 // 默认 30 分钟（与 config 默认一致，应对开发任务）
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	c := e.getClient(session)
	if c == nil {
		onDone("", fmt.Errorf("start pi rpc for session %q failed", session))
		return
	}

	e.seq++
	id := fmt.Sprintf("f%d", e.seq)
	log.Printf("[pi/%s] prompt id=%s: %s", session, id, prompt)
	c.prompt(ctx, id, prompt, onChunk, func(final string, err error) {
		if err != nil {
			log.Printf("[pi/%s] id=%s error: %v", session, id, err)
			// 进程挂了？尝试重启（下次消息自动重连）
			e.onProcessError(session, c)
		} else {
			log.Printf("[pi/%s] id=%s done len=%d", session, id, len(final))
		}
		onDone(final, err)
	})
}

// getClient 获取会话的常驻进程，不存在则启动。
// 每个会话固定使用 session-id "feisuo-<name>"（sanitize 后），
// 重启飞梭后用相同 id 拉起 pi，自动恢复该会话的对话历史。
func (e *Engine) getClient(session string) *rpcClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.clients[session]; ok && c != nil {
		return c
	}
	cfg := e.cfg
	cfg.SessionID = "feisuo-" + sanitizeSessionID(session)
	var prompts []string
	if e.SystemPromptOf != nil {
		if role := strings.TrimSpace(e.SystemPromptOf(session)); role != "" {
			prompts = append(prompts, role)
		}
	}
	if e.SessionList != nil {
		prompts = append(prompts, bridgePrompt(e.SessionList()))
	}
	cfg.AppendSystemPrompt = strings.Join(prompts, "\n\n")
	c, err := startRPC(context.Background(), cfg)
	if err != nil {
		log.Printf("[pi/%s] start rpc failed: %v", session, err)
		return nil
	}
	e.clients[session] = c
	log.Printf("[pi/%s] rpc process started (session-id=%s)", session, cfg.SessionID)
	return c
}

// bridgePrompt 生成注入 pi system prompt 的会话协作协议说明。
// 与 internal/bridge 的 BridgePrompt 一致，但避免包循环依赖。
func bridgePrompt(sessions []string) string {
	var b strings.Builder
	b.WriteString("\n\n[飞梭会话协作协议]\n")
	b.WriteString("你可以请其他会话的助手帮你处理任务。方法：在回复中输出标记\n")
	b.WriteString("<bridge to=\"会话名\">要发给对方的完整消息</bridge>\n")
	b.WriteString("飞梭会把标记内部的消息转发给目标会话，对方回复会直接发给用户。\n")
	b.WriteString("可用会话：" + strings.Join(sessions, ", ") + "\n")
	b.WriteString("规则：\n")
	b.WriteString("1. to 必须是上面列出的会话名之一；\n")
	b.WriteString("2. 标记内部消息要完整自包含（对方看不到本会话对话历史）；\n")
	b.WriteString("3. 一次最多输出 2 个标记；\n")
	b.WriteString("4. 普通回复不要输出该标记（否则会被误转发）；\n")
	b.WriteString("5. 若目标会话不存在，标记会被忽略。\n")
	return b.String()
}

// sanitizeSessionID 将会话名转成安全的 pi session id（仅 [a-zA-Z0-9-_]，且首尾必须是字母/数字）。
func sanitizeSessionID(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-_")
	if s == "" {
		return "dev"
	}
	return s
}

// onProcessError 当会话进程异常时移除引用（下次消息重新启动）。
func (e *Engine) onProcessError(session string, c *rpcClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.clients[session] == c {
		delete(e.clients, session)
	}
	c.close()
}

// Close 关闭所有常驻进程。
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, c := range e.clients {
		log.Printf("[pi/%s] closing rpc process", name)
		c.close()
	}
	e.clients = map[string]*rpcClient{}
}

// Kill 关闭指定会话的常驻 pi 进程（真正释放资源）。
// 会话不存在或未启动时静默返回。之后该会话再次收到消息会自动重启进程。
func (e *Engine) Kill(session string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.clients[session]; ok && c != nil {
		log.Printf("[pi/%s] killing rpc process", session)
		c.close()
		delete(e.clients, session)
	}
}

// Reset 透传 pi 原生 new_session（等价 pi 交互模式 /new）：
// 在常驻进程内切换到全新 session，清空该会话的对话上下文。
// 若会话进程不存在，下次消息会重新启动（空上下文，效果相同）。
// 若 agent 正在跑，清空 pending 防止等待者卡死。
func (e *Engine) Reset(session string) error {
	e.mu.Lock()
	c, ok := e.clients[session]
	e.mu.Unlock()
	if !ok || c == nil {
		log.Printf("[pi/%s] reset: no rpc process (will start fresh on next message)", session)
		return nil
	}
	log.Printf("[pi/%s] reset: sending new_session", session)
	c.clearPending("session reset")
	return c.newSession()
}

// SessionCount 返回活跃常驻进程数（调试用）。
func (e *Engine) SessionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.clients)
}
