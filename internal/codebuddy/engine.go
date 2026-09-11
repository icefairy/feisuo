// Package codebuddy implements the engine.Engine interface using Tencent CodeBuddy CLI.
//
// Protocol: resident process launched as
//
//	codebuddy -p --input-format stream-json --output-format stream-json
//	          --include-partial-messages --session-id <id> -y [--model <m>]
//
// Stdin 发送 JSON 行，stdout 接收 NDJSON 事件流。
// 完成信号：type=result 的事件（无 prompt id，按 FIFO 顺序匹配 pending prompt）。
// 流式增量：type=stream_event 事件中的 text_delta，用于 onChunk 回调。
package codebuddy

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

// Config 是 codebuddy 引擎的配置。
type Config struct {
	Binary   string // "codebuddy"
	Model    string // 如 "deepseek-v4.1-flash"
	APIKey   string // 非空时设置为 CODEBUDDY_API_KEY 环境变量
	Timeout  int    // 单任务超时（秒）；0=不超时；nil 配置时默认 1800
	SessionID func(session string) string
	// AppendSystemPrompt 追加到 system prompt（角色 + bridge 协议），可空
	AppendSystemPrompt string
}

// Engine 封装 codebuddy RPC 常驻进程。每个会话持有一个常驻进程。
type Engine struct {
	cfg Config

	mu      sync.Mutex
	clients map[string]*rpcClient
	seq     int
}

func New(cfg Config) *Engine {
	if cfg.Binary == "" {
		cfg.Binary = "codebuddy"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 1800
	}
	return &Engine{cfg: cfg, clients: make(map[string]*rpcClient)}
}

// Execute 向指定会话的常驻 codebuddy 进程发送 prompt。
func (e *Engine) Execute(ctx context.Context, session, prompt string,
	onChunk func(string), onDone func(string, error)) {

	e.runPrompt(ctx, session, prompt, false, onChunk, onDone)
}

// ExecuteSteer 立即打断当前任务并注入新 prompt（等价 pi 的 steer 模式）。
func (e *Engine) ExecuteSteer(ctx context.Context, session, prompt string,
	onChunk func(string), onDone func(string, error)) {

	e.runPrompt(ctx, session, prompt, true, onChunk, onDone)
}

// Steer 向指定会话发送 steer 消息（fire-and-forget），用于 busy 时的纠正注入。
// 内部实现：先发 interrupt 控制请求，再立即发新消息。
func (e *Engine) Steer(session, msg string) error {
	e.mu.Lock()
	c, ok := e.clients[session]
	e.mu.Unlock()
	if !ok || c == nil {
		return fmt.Errorf("session %q: no codebuddy process", session)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("session %q: codebuddy process not running", session)
	}
	// 先发 interrupt，再发新消息（顺序关键）。
	// 不等待 interrupt 的 result——让新消息自然排队处理。
	if err := c.sendControl("interrupt"); err != nil {
		return err
	}
	// 短暂等待确保 interrupt 被处理后再发新消息
	time.Sleep(500 * time.Millisecond)
	return c.sendPrompt(msg, false)
}

// Abort 中断指定会话当前正在跑的 codebuddy 任务。
func (e *Engine) Abort(session string) error {
	e.mu.Lock()
	c, ok := e.clients[session]
	e.mu.Unlock()
	if !ok || c == nil {
		return fmt.Errorf("session %q: no codebuddy process", session)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("session %q: codebuddy process not running", session)
	}
	log.Printf("[cb/%s] abort", session)
	return c.sendControl("interrupt")
}

// Reset 清空会话上下文：关闭旧进程，下次 Execute 用新 session-id 重启。
func (e *Engine) Reset(session string) error {
	e.mu.Lock()
	c, ok := e.clients[session]
	e.mu.Unlock()
	if !ok || c == nil {
		log.Printf("[cb/%s] reset: no process (will start fresh on next message)", session)
		return nil
	}
	// 生成新 session-id，避免重启后恢复旧上下文
	newID := fmt.Sprintf("feisuo-%s-%d", sanitizeSessionID(session), time.Now().UnixNano())
	c.mu.Lock()
	c.sessionID = newID
	c.mu.Unlock()
	// 清空中断 pending，关闭进程（下次 Execute 会自动重启）
	c.clearPending("session reset")
	c.close()
	e.mu.Lock()
	delete(e.clients, session)
	e.mu.Unlock()
	log.Printf("[cb/%s] reset: new session-id=%s", session, newID)
	return nil
}

// Kill 关闭指定会话的常驻进程。
func (e *Engine) Kill(session string) {
	e.mu.Lock()
	c, ok := e.clients[session]
	e.mu.Unlock()
	if !ok || c == nil {
		return
	}
	log.Printf("[cb/%s] killing process", session)
	c.clearPending("session killed")
	c.close()
	e.mu.Lock()
	delete(e.clients, session)
	e.mu.Unlock()
}

// Close 关闭所有常驻进程。
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, c := range e.clients {
		log.Printf("[cb/%s] closing", name)
		c.clearPending("engine closed")
		c.close()
	}
	e.clients = map[string]*rpcClient{}
}

// SessionCount 返回活跃常驻进程数。
func (e *Engine) SessionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.clients)
}

// SetSessionList 实现 engine.Engine 接口。
func (e *Engine) SetSessionList(fn func() []string) {
	e.cfg.AppendSystemPrompt = appendBridgeProtocol(e.cfg.AppendSystemPrompt, strings.Join(fn(), ", "))
}

// SetSystemPromptOf 实现 engine.Engine 接口。
// 已启动的会话不受影响（仅在下次启动新会话时通过 cfg.AppendSystemPrompt 生效）。
func (e *Engine) SetSystemPromptOf(fn func(string) string) {
	_ = fn
}

// runPrompt 内部方法：启动（或复用）进程，发送 prompt，等待 result。
func (e *Engine) runPrompt(ctx context.Context, session, prompt string, steer bool,
	onChunk func(string), onDone func(string, error)) {

	timeout := e.cfg.Timeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	c := e.getClient(session)
	if c == nil {
		onDone("", fmt.Errorf("start codebuddy rpc for session %q failed", session))
		return
	}

	e.seq++
	id := fmt.Sprintf("cb%d", e.seq)
	log.Printf("[cb/%s] prompt id=%s: %s", session, id, truncate(prompt, 80))
	c.prompt(ctx, id, prompt, steer, onChunk, func(final string, err error) {
		if err != nil {
			log.Printf("[cb/%s] id=%s error: %v", session, id, err)
			e.onProcessError(session, c)
		} else {
			log.Printf("[cb/%s] id=%s done len=%d", session, id, len(final))
		}
		onDone(final, err)
	})
}

// getClient 获取或启动会话的常驻进程。
func (e *Engine) getClient(session string) *rpcClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.clients[session]; ok && c != nil {
		return c
	}
	cfg := e.cfg
	sessionID := cfg.SessionID(session)
	binary := cfg.Binary
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--session-id", sessionID,
		"-y", // 非交互：跳过权限确认
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.AppendSystemPrompt)
	}

	cmd := exec.CommandContext(context.Background(), binary, args...)
	// 注入 API key（可选）
	if cfg.APIKey != "" {
		cmd.Env = append(cmd.Env, "CODEBUDDY_API_KEY="+cfg.APIKey)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("[cb/%s] stdin pipe: %v", session, err)
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[cb/%s] stdout pipe: %v", session, err)
		return nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[cb/%s] stderr pipe: %v", session, err)
		return nil
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[cb/%s] start: %v", session, err)
		return nil
	}

	c := &rpcClient{
		cmd:      cmd,
		stdin:    bufio.NewWriter(stdin),
		events:   make(chan map[string]any, 256),
		pending:  make(map[string]*pendingPrompt),
		shutdown: make(chan struct{}),
		sessionID: sessionID,
	}

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				log.Printf("[cb-rpc] stderr: %s", line)
			}
		}
	}()

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
		c.mu.Lock()
		c.closed = true
		errStr := c.lastErr
		if errStr == "" {
			errStr = "codebuddy rpc process exited"
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

	go c.dispatchLoop()

	go func() {
		_ = cmd.Wait()
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}()

	log.Printf("[cb/%s] started pid=%d session-id=%s", session, cmd.Process.Pid, sessionID)
	e.clients[session] = c
	return c
}

// onProcessError 进程异常时移除引用（下次消息自动重连）。
func (e *Engine) onProcessError(session string, c *rpcClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.clients[session] == c {
		delete(e.clients, session)
	}
	c.close()
}

// ---------- rpcClient ----------

type rpcClient struct {
	cmd       *exec.Cmd
	stdin     *bufio.Writer
	events    chan map[string]any
	sessionID string

	mu           sync.Mutex
	pending      map[string]*pendingPrompt
	pendingOrder []string
	closed       bool
	shutdown     chan struct{}
	lastErr      string
}

type pendingPrompt struct {
	onChunk    func(string)
	onDone     func(string, error)
	buf        strings.Builder
	timer      *time.Timer
	hasText    bool // 启动竞态检测：是否收到过 assistant text
	retries    int  // 启动重试次数（最多 1 次）
	promptText string // 原始 prompt，用于启动竞态自动重试
	hasDelta   bool // 是否收到过 stream_event text_delta（避免 assistant 完整块与 delta 重复追加）
}

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
	case "stream_event":
		// --include-partial-messages 时产生：提取 text_delta 增量
		evData, ok := ev["event"].(map[string]any)
		if !ok {
			return
		}
	eventType, _ := evData["type"].(string)
		if eventType != "content_block_delta" {
			return
		}
		delta, ok := evData["delta"].(map[string]any)
		if !ok {
			return
		}
		deltaType, _ := delta["type"].(string)
		if deltaType != "text_delta" {
			return
		}
		text, _ := delta["text"].(string)
		c.mu.Lock()
		if len(c.pendingOrder) > 0 {
			firstID := c.pendingOrder[0]
			if p, ok := c.pending[firstID]; ok {
				p.buf.WriteString(text)
				p.hasText = true
				p.hasDelta = true
				cb := p.onChunk
				c.mu.Unlock()
				if cb != nil {
					cb(text)
				}
				return
			}
		}
		c.mu.Unlock()

	case "assistant":
		// 完整 assistant 消息（fallback，当没有 stream_event 时作为最终文本来源）
		msg, ok := ev["message"].(map[string]any)
		if !ok {
			return
		}
		content, ok := msg["content"].([]any)
		if !ok {
			return
		}
		var fullText strings.Builder
		for _, item := range content {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "text" {
				fullText.WriteString(block["text"].(string))
			}
		}
		c.mu.Lock()
		if len(c.pendingOrder) > 0 {
			firstID := c.pendingOrder[0]
			if p, ok := c.pending[firstID]; ok {
				// 仅当未收到过 stream_event delta 时才追加完整 assistant 文本，避免重复
				if !p.hasDelta {
					p.buf.WriteString(fullText.String())
				}
				p.hasText = true
			}
		}
		c.mu.Unlock()

	case "result":
		subtype, _ := ev["subtype"].(string)
		resultRaw, _ := ev["result"].(string)
		isError, _ := ev["is_error"].(bool)

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

		if done == nil {
			return
		}
		if done.timer != nil {
			done.timer.Stop()
		}

		// 启动竞态检测：第一条消息被进程丢弃时，result 为空且 hasText=false。
		// 自动重发一次（最多 1 次），等待进程初始化完成后再发。
		if subtype == "success" && resultRaw == "" && !done.hasText && done.retries < 1 {
			done.retries++
			promptText := done.promptText
			log.Printf("[cb-rpc] id=%s: empty result+no text (startup race?), resending in 2s", doneID)
			go func() {
				time.Sleep(2 * time.Second)
				c.mu.Lock()
				c.pending[doneID] = done
				c.pendingOrder = append(c.pendingOrder, doneID)
				c.mu.Unlock()
				if err := c.sendPrompt(promptText, false); err != nil {
					c.mu.Lock()
					var p2 *pendingPrompt
					if pp, ok2 := c.pending[doneID]; ok2 {
						c.removePendingLocked(doneID)
						p2 = pp
					}
					c.mu.Unlock()
					if p2.timer != nil {
						p2.timer.Stop()
					}
					if p2.onDone != nil {
						p2.onDone("", fmt.Errorf("resend failed: %w", err))
					}
				}
			}()
			return
		}

		final := strings.TrimSpace(done.buf.String())
		if final == "" {
			final = resultRaw
		}
		if final == "" {
			final = "（CodeBuddy 返回为空）"
		}
		var err error
		if isError || subtype == "error_during_execution" {
			err = fmt.Errorf("codebuddy error: %s", resultRaw)
		}
		log.Printf("[cb-rpc] settled id=%s len=%d subtype=%s", doneID, len(final), subtype)
		done.onDone(final, err)

	case "control_response":
		// interrupt 等控制请求的响应，无需特殊处理（异步）
		resp, ok := ev["response"].(map[string]any)
		if ok && resp["subtype"] == "error" {
			log.Printf("[cb-rpc] control response error: %v", resp)
		}
	}
}

// prompt 发送 prompt 给 codebuddy。
// steer=true 时先发送 interrupt 再发 prompt（用于 ExecuteSteer）。
func (c *rpcClient) prompt(ctx context.Context, id, msg string, steer bool,
	onChunk func(string), onDone func(string, error)) {

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		onDone("", fmt.Errorf("codebuddy rpc process not running"))
		return
	}
	p := &pendingPrompt{onChunk: onChunk, onDone: onDone, promptText: msg}
	c.pending[id] = p
	c.pendingOrder = append(c.pendingOrder, id)
	c.mu.Unlock()

	// 超时保护
	if dl, ok := ctx.Deadline(); ok {
		dur := time.Until(dl)
		if dur <= 0 {
			dur = 30 * time.Second
		}
		p.timer = time.AfterFunc(dur, func() {
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
			_ = c.sendControl("interrupt")
		})
	}

	// steer 模式：先发 interrupt 再发 prompt
	if steer {
		if err := c.sendControl("interrupt"); err != nil {
			c.mu.Lock()
			c.removePendingLocked(id)
			c.mu.Unlock()
			if p.timer != nil {
				p.timer.Stop()
			}
			onDone("", err)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}

	if err := c.sendPrompt(msg, steer); err != nil {
		c.mu.Lock()
		c.removePendingLocked(id)
		c.mu.Unlock()
		if p.timer != nil {
			p.timer.Stop()
		}
		onDone("", err)
	}
}

func (c *rpcClient) sendPrompt(msg string, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("codebuddy rpc process not running")
	}
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": msg},
			},
		},
	})
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return c.stdin.Flush()
}

func (c *rpcClient) sendControl(reqType string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("codebuddy rpc process not running")
	}
	b, err := json.Marshal(map[string]any{
		"type": "control_request",
		"request_id": fmt.Sprintf("cb-%d", time.Now().UnixNano()),
		"request": map[string]any{
			"subtype": reqType,
		},
	})
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return c.stdin.Flush()
}

func (c *rpcClient) removePendingLocked(id string) {
	delete(c.pending, id)
	for i, v := range c.pendingOrder {
		if v == id {
			c.pendingOrder = append(c.pendingOrder[:i], c.pendingOrder[i+1:]...)
			break
		}
	}
}

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

// ---------- helpers ----------

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// appendBridgeProtocol 在 system prompt 末尾追加 bridge 协作协议说明。
func appendBridgeProtocol(base, sessionsStr string) string {
	if sessionsStr == "" {
		return base
	}
	var b strings.Builder
	if base != "" {
		b.WriteString(base)
		b.WriteString("\n\n")
	}
	b.WriteString("\n\n[飞梭会话协作协议]\n")
	b.WriteString("你可以请其他会话的助手帮你处理任务。方法：在回复中输出标记\n")
	b.WriteString("<bridge to=\"会话名\">要发给对方的完整消息</bridge>\n")
	b.WriteString("飞梭会把标记内部的消息转发给目标会话，对方回复会直接发给用户。\n")
	b.WriteString("可用会话：" + sessionsStr + "\n")
	b.WriteString("规则：\n")
	b.WriteString("1. to 必须是上面列出的会话名之一；\n")
	b.WriteString("2. 标记内部消息要完整自包含（对方看不到本会话对话历史）；\n")
	b.WriteString("3. 一次最多输出 2 个标记；\n")
	b.WriteString("4. 普通回复不要输出该标记（否则会被误转发）；\n")
	b.WriteString("5. 若目标会话不存在，标记会被忽略。\n")
	return b.String()
}
