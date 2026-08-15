package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// rpcClient 封装一个常驻的 pi --mode rpc 进程。
// 每个会话一个实例：stdin 发 JSON 命令，stdout 读事件流。
type rpcClient struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	events chan map[string]any // 事件流（buffered，reader 常驻消费）

	mu       sync.Mutex
	pending  map[string]*pendingPrompt // id -> 进行中的 prompt
	closed   bool
	shutdown chan struct{}
	lastErr  string
}

type pendingPrompt struct {
	onChunk func(string)
	onDone  func(string, error)
	buf     strings.Builder
	timer   *time.Timer
}

// startRPC 启动常驻 pi RPC 进程。
func startRPC(ctx context.Context, cfg Config) (*rpcClient, error) {
	args := []string{
		"--mode", "rpc",
		"--no-skills",
		"-na",
		"--provider", cfg.Provider,
		"--model", cfg.Model,
	}
	if cfg.APIKey != "" {
		args = append(args, "--api-key", cfg.APIKey)
	}
	binary := cfg.Binary
	if binary == "" {
		binary = "pi"
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	c := &rpcClient{
		cmd:      cmd,
		stdin:    bufio.NewWriter(stdin),
		events:   make(chan map[string]any, 256),
		pending:  make(map[string]*pendingPrompt),
		shutdown: make(chan struct{}),
	}

	// stderr 日志（不阻塞）
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				log.Printf("[pi-rpc] stderr: %s", line)
			}
		}
	}()

	// stdout 事件读取循环（永不退出，直到进程结束）
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			select {
			case c.events <- ev:
			case <-c.shutdown:
				return
			}
		}
		// stdout EOF：进程退出了
		c.mu.Lock()
		c.closed = true
		errStr := c.lastErr
		if errStr == "" {
			errStr = "pi rpc process exited"
		}
		pend := c.pending
		c.pending = map[string]*pendingPrompt{}
		c.mu.Unlock()
		for _, p := range pend {
			p.onDone(strings.TrimSpace(p.buf.String()), fmt.Errorf("%s", errStr))
		}
	}()

	// 事件分发循环
	go c.dispatchLoop()

	// 进程退出兜底：cmd.Wait 完成后确保 closed
	go func() {
		_ = cmd.Wait()
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}()

	log.Printf("[pi-rpc] started pid=%d", cmd.Process.Pid)
	return c, nil
}

// dispatchLoop 消费事件流，路由到对应 pending prompt。
func (c *rpcClient) dispatchLoop() {
	for {
		select {
		case <-c.shutdown:
			return
		case ev := <-c.events:
			c.handleEvent(ev)
		}
	}
}

func (c *rpcClient) handleEvent(ev map[string]any) {
	typ, _ := ev["type"].(string)

	switch typ {
	case "message_update":
		ame, ok := ev["assistantMessageEvent"].(map[string]any)
		if !ok {
			return
		}
		dt, _ := ame["type"].(string)
		if dt == "text_delta" {
			delta, _ := ame["delta"].(string)
			c.mu.Lock()
			// 把 delta 分发给所有 pending（当前只有一个在跑，但保险起见全发）
			for _, p := range c.pending {
				p.buf.WriteString(delta)
				if p.onChunk != nil {
					p.onChunk(delta)
				}
			}
			c.mu.Unlock()
		}

	case "agent_settled":
		c.mu.Lock()
		pend := c.pending
		c.pending = map[string]*pendingPrompt{}
		c.mu.Unlock()
		for _, p := range pend {
			if p.timer != nil {
				p.timer.Stop()
			}
			final := strings.TrimSpace(p.buf.String())
			if final == "" {
				final = "（Pi 返回为空）"
			}
			p.onDone(final, nil)
		}

	case "response":
		// prompt 命令的 ack：{"id","type":"response","command":"prompt","success":true/false}
		id, _ := ev["id"].(string)
		okFlag, _ := ev["success"].(bool)
		if id != "" && !okFlag {
			errMsg, _ := ev["error"].(string)
			c.mu.Lock()
			if p, ok := c.pending[id]; ok {
				delete(c.pending, id)
				if p.timer != nil {
					p.timer.Stop()
				}
				p.onDone("", fmt.Errorf("pi rejected prompt: %s", errMsg))
			}
			c.mu.Unlock()
		}
	}
}

// prompt 向常驻进程发送一个 prompt，等待完成。
func (c *rpcClient) prompt(ctx context.Context, id, msg string, onChunk func(string), onDone func(string, error)) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		onDone("", fmt.Errorf("pi rpc process not running"))
		return
	}
	p := &pendingPrompt{onChunk: onChunk, onDone: onDone}
	c.pending[id] = p
	c.mu.Unlock()

	// 超时保护
	if dl, ok := ctx.Deadline(); ok {
		p.timer = time.AfterFunc(time.Until(dl), func() {
			c.mu.Lock()
			if p2, ok2 := c.pending[id]; ok2 {
				delete(c.pending, id)
				p2.onDone(strings.TrimSpace(p2.buf.String()), fmt.Errorf("timeout after %s", time.Until(dl).Round(time.Second)*-1))
			}
			c.mu.Unlock()
			// 超时后发 abort 让 pi 恢复
			_ = c.send(map[string]any{"type": "abort"})
		})
	}

	cmd := map[string]any{"id": id, "type": "prompt", "message": msg}
	if err := c.send(cmd); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		if p.timer != nil {
			p.timer.Stop()
		}
		onDone("", err)
	}
}

func (c *rpcClient) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("pi rpc process not running")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return c.stdin.Flush()
}

// close 关闭常驻进程。
func (c *rpcClient) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.shutdown)
	c.mu.Unlock()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
