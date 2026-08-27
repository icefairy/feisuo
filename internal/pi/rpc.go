package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"runtime"
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
	pendingOrder []string              // FIFO：插入顺序，agent_settled 只完成最早一个
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
	if cfg.SessionID != "" {
		args = append(args, "--session-id", cfg.SessionID)
	}
	if cfg.APIKey != "" {
		args = append(args, "--api-key", cfg.APIKey)
	}
	if cfg.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.AppendSystemPrompt)
	}
	binary := cfg.Binary
	if binary == "" {
		binary = "pi"
	}
	if runtime.GOOS == "windows" {
		// Windows npm 全局安装生成 pi.cmd（不是 pi），且 exec.Command 不能直接跑 .cmd。
		// 用 LookPath 解析完整路径（.cmd/.exe）；找不到则原样交给 Start 报错。
		if p, err := exec.LookPath(binary); err == nil {
			binary = p
		} else if p, err := exec.LookPath(binary + ".cmd"); err == nil {
			binary = p
		} else if p, err := exec.LookPath(binary + ".exe"); err == nil {
			binary = p
		}
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
		pendOrder := c.pendingOrder
		c.pending = map[string]*pendingPrompt{}
		c.pendingOrder = nil
		c.mu.Unlock()
		for _, id := range pendOrder {
			if p, ok := pend[id]; ok {
				p.onDone(strings.TrimSpace(p.buf.String()), fmt.Errorf("%s", errStr))
			}
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
			// 只发给最早 pending（当前正在跑的 prompt；followUp 排队的后序 prompt 还没开始）
			var cb func(string)
			if len(c.pendingOrder) > 0 {
				first := c.pendingOrder[0]
				if p, ok := c.pending[first]; ok {
					p.buf.WriteString(delta)
					cb = p.onChunk
				}
			}
			c.mu.Unlock()
			// 锁外回调：onChunk 可能进入 onProcessError→close 再次拿 c.mu，持锁调用会自锁死锁
			if cb != nil {
				cb(delta)
			}
		}

	case "agent_settled":
		// pi 的 agent_settled 不带 prompt id，且 followUp 队列 FIFO：
		// 每次 settled 只代表**最早**那个 prompt 完成（后续排队的还没跑）。
		// 只完成第一个，不能清空全部（否则排队的 prompt 会被错误地空结果 onDone）。
		c.mu.Lock()
		var doneID string
		var done *pendingPrompt
		if len(c.pendingOrder) > 0 {
			doneID = c.pendingOrder[0]
			done = c.pending[doneID]
			c.pendingOrder = c.pendingOrder[1:]
			delete(c.pending, doneID)
		}
		c.mu.Unlock()
		if done != nil {
			if done.timer != nil {
				done.timer.Stop()
			}
			final := strings.TrimSpace(done.buf.String())
			if final == "" {
				final = "（Pi 返回为空）"
			}
			log.Printf("[pi-rpc] settled id=%s len=%d queue=%d", doneID, len(final), len(c.pendingOrder))
			done.onDone(final, nil)
		}

	case "response":
		// prompt 命令的 ack：{"id","type":"response","command":"prompt","success":true/false}
		id, _ := ev["id"].(string)
		okFlag, _ := ev["success"].(bool)
		if id != "" && !okFlag {
			errMsg, _ := ev["error"].(string)
			c.mu.Lock()
			var p *pendingPrompt
			if pp, ok := c.pending[id]; ok {
				c.removePendingLocked(id)
				if pp.timer != nil {
					pp.timer.Stop()
				}
				p = pp
			}
			c.mu.Unlock()
			// 锁外回调：onDone 可能进入 onProcessError→close 再次拿 c.mu
			if p != nil {
				p.onDone("", fmt.Errorf("pi rejected prompt: %s", errMsg))
			}
		}
	}
}

// removePendingLocked 从 pending map 和 FIFO order 中移除 id。调用方必须持有 c.mu。
func (c *rpcClient) removePendingLocked(id string) {
	delete(c.pending, id)
	for i, v := range c.pendingOrder {
		if v == id {
			c.pendingOrder = append(c.pendingOrder[:i], c.pendingOrder[i+1:]...)
			break
		}
	}
}

// prompt 发送 prompt 给 pi。
// steer=false：streamingBehavior=followUp 让 pi 原生排队，agent 忙时自动排队，完成后依次处理
// （不用 feisuo 自己串行）。
// steer=true：streamingBehavior=steer 立即打断当前 agent 正在跑的任务，优先处理这条消息
// （用于纠正/补充等需要立即生效的消息）。
func (c *rpcClient) prompt(ctx context.Context, id, msg string, steer bool, onChunk func(string), onDone func(string, error)) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		onDone("", fmt.Errorf("pi rpc process not running"))
		return
	}
	p := &pendingPrompt{onChunk: onChunk, onDone: onDone}
	c.pending[id] = p
	c.pendingOrder = append(c.pendingOrder, id)
	c.mu.Unlock()

	// 超时保护
	if dl, ok := ctx.Deadline(); ok {
		// 缓存剩余时间：timer 触发时 time.Until(dl)≈0，直接取会导致错误信息误导显示 "0s"
		dur := time.Until(dl)
		if dur <= 0 {
			// deadline 已过（排队太久）：给最小保护期，避免 AfterFunc(0) 立即触发
			dur = 30 * time.Second
		}
		p.timer = time.AfterFunc(dur, func() {
			// 先取回 + 移除 pending，再锁外调 onDone：
			// onDone → onProcessError → close() 会再次拿 c.mu，持锁调用会自锁死锁（2026-08 事故根因）
			c.mu.Lock()
			var p2 *pendingPrompt
			if pp, ok2 := c.pending[id]; ok2 {
				c.removePendingLocked(id)
				p2 = pp
			}
			c.mu.Unlock()
			if p2 != nil {
				p2.onDone(strings.TrimSpace(p2.buf.String()), fmt.Errorf("timeout after %s", dur.Round(time.Second)))
			}
			// 超时后发 abort 让 pi 恢复
			_ = c.send(map[string]any{"type": "abort"})
		})
	}

	// streamingBehavior：followUp(FIFO 排队) 或 steer(打断当前任务立即处理)。
	streamingBehavior := "followUp"
	if steer {
		streamingBehavior = "steer"
	}
	cmd := map[string]any{
		"id":   id,
		"type": "prompt",
		"message": msg,
		// 使用 pi 原生队列：当前 agent 忙时 followUp 排队，完成后依次处理；
		// steer 则打断当前任务立即处理这条消息。不自己串行（doneCh），避免与 pi 队列冲突/重复实现。
		"streamingBehavior": streamingBehavior,
	}
	if err := c.send(cmd); err != nil {
		c.mu.Lock()
		c.removePendingLocked(id)
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

// newSession 透传 pi 原生 reset：切换到全新 session（清空上下文）。
// 等价 pi 交互模式的 /new。旧 session 文件留磁盘（可 switch_session 找回）。
func (c *rpcClient) newSession() error {
	return c.send(map[string]any{"type": "new_session"})
}

// abort 透传 pi 原生 abort：打断当前正在跑的 prompt（不销毁会话，上下文保留；
// agent 空闲时发送是 no-op）。
func (c *rpcClient) abort() error {
	return c.send(map[string]any{"type": "abort"})
}

// steer 透传 pi 原生 steer：打断当前正在跑的 agent 并立即注入这条消息处理。
// 与 abort 的区别：steer 会继续对话（打断当前 turn 后处理这条新输入），
// abort 是停止。用于"用户发现问题立即追加纠正/补充信息"的场景。
func (c *rpcClient) steer(msg string) error {
	return c.send(map[string]any{"type": "steer", "message": msg})
}

// clearPending 清空该进程所有 pending prompt（reset 时若 agent 正在跑，
// 需要立即结束所有等待者，否则他们会永远等 agent_settled 而卡死）。
func (c *rpcClient) clearPending(errMsg string) {
	c.mu.Lock()
	pend := c.pending
	pendOrder := c.pendingOrder
	c.pending = map[string]*pendingPrompt{}
	c.pendingOrder = nil
	c.mu.Unlock()
	for _, id := range pendOrder {
		if p, ok := pend[id]; ok {
			if p.timer != nil {
				p.timer.Stop()
			}
			p.onDone(strings.TrimSpace(p.buf.String()), fmt.Errorf("%s", errMsg))
		}
	}
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
		if runtime.GOOS == "windows" {
			// Windows 上 Process.Kill() 只杀 pi.cmd（cmd.exe 壳），node 子进程会残留成僵尸。
			// taskkill /T 杀整个进程树。
			_ = exec.Command("taskkill", "/PID", fmt.Sprint(c.cmd.Process.Pid), "/T", "/F").Run()
		} else {
			_ = c.cmd.Process.Kill()
		}
	}
}
