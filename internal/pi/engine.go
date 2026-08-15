package pi

import (
	"context"
	"fmt"
	"log"
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
		timeout = 300
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
func (e *Engine) getClient(session string) *rpcClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.clients[session]; ok && c != nil {
		return c
	}
	c, err := startRPC(context.Background(), e.cfg)
	if err != nil {
		log.Printf("[pi/%s] start rpc failed: %v", session, err)
		return nil
	}
	e.clients[session] = c
	log.Printf("[pi/%s] rpc process started", session)
	return c
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

// SessionCount 返回活跃常驻进程数（调试用）。
func (e *Engine) SessionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.clients)
}
