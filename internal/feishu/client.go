package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type Config struct {
	AppID     string
	AppSecret string
	BotName   string
	WsURL     string
}

type Event struct {
	Type     string
	SenderID string
	Message  Message
}

type Message struct {
	Content string
}

type Client struct {
	cfg       Config
	httpCli   *http.Client
	token     string
	tokenMu   sync.RWMutex
	streams   map[string]*streamState // openID|session -> 流式状态
	streamMu  sync.Mutex
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, httpCli: &http.Client{Timeout: 30 * time.Second}, streams: make(map[string]*streamState)}
}

// getAccessToken 获取 tenant_access_token
func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.RLock()
	if c.token != "" {
		tok := c.token
		c.tokenMu.RUnlock()
		return tok, nil
	}
	c.tokenMu.RUnlock()

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" {
		return c.token, nil
	}

	body := map[string]string{
		"app_id":     c.cfg.AppID,
		"app_secret": c.cfg.AppSecret,
	}
	data, _ := json.Marshal(body)
	resp, err := c.httpCli.Post("https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		"application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		// 注意：token API 响应是顶层字段（无 data 包装）
		TenantToken string `json:"tenant_access_token"`
		Expire      int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("token decode: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("token API: code=%d msg=%s", result.Code, result.Msg)
	}
	c.token = result.TenantToken
	log.Printf("[feishu] token obtained, expire=%ds", result.Expire)
	go c.refreshToken(ctx)
	return c.token, nil
}

func (c *Client) refreshToken(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := c.getAccessToken(ctx)
			if err != nil {
				log.Printf("[feishu] token refresh failed: %v", err)
			}
		}
	}
}

// Send 发送飞书文本消息（自动分片，每块 ≤ 3800 字符，避免内容超限被截断）
func (c *Client) Send(ctx context.Context, openID string, content string) error {
	for _, part := range splitParts(content, 3800) {
		if err := c.sendOne(ctx, openID, part); err != nil {
			return err
		}
	}
	return nil
}

// splitParts 按 n 字符切分（按 rune 切，避免拆坏中文）
func splitParts(s string, n int) []string {
	runes := []rune(s)
	var parts []string
	for i := 0; i < len(runes); i += n {
		end := i + n
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[i:end]))
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

// sendOne 发送单条（≤3800 字符）
func (c *Client) sendOne(ctx context.Context, openID string, content string) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	// 注意：飞书要求 content 是字符串（JSON 序列化后的字符串），不是对象
	contentJSON, _ := json.Marshal(map[string]string{"text": content})
	payload := map[string]interface{}{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    string(contentJSON),
	}
	data, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id",
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		body, _ := io.ReadAll(resp.Body)
		json.Unmarshal(body, &result)
		// 详细错误：飞书返回 code + msg；日志带完整请求摘要便于排障
		return fmt.Errorf("send: status=%d code=%d msg=%q resp=%s openid=%s",
			resp.StatusCode, result.Code, result.Msg, truncate(string(body), 300), truncate(openID, 40))
	}
	return nil
}

// truncate 截断长字符串（日志用）
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Reply 发送带前缀的消息
func (c *Client) Reply(openID, content string) {
	if openID == "local" {
		fmt.Printf("%s\n", content)
		return
	}
	content = fmt.Sprintf("🤖 [%s] %s", c.cfg.BotName, content)
	if err := c.Send(context.Background(), openID, content); err != nil {
		log.Printf("[feishu] reply failed: %v", err)
	}
}

// SendChunk 发送流式分块（用于 Pi 执行中的进度）
func (c *Client) SendChunk(openID, session, chunk string) {
	if openID == "local" {
		fmt.Printf("⚡ [%s] %s\n", session, chunk)
		return
	}
	prefix := fmt.Sprintf("⚡ [%s]", session)
	content := prefix + " " + chunk
	if len(content) > 1500 {
		content = content[:1497] + "..."
	}
	if err := c.Send(context.Background(), openID, content); err != nil {
		log.Printf("[feishu] chunk send failed: %v", err)
	}
}

// ---- 流式编辑模式（一个气泡内滚动更新）----
// 策略参考 Hermes stream_consumer：
//   - 编辑间隔 1.5s（飞书单条消息编辑上限 20 次，1.5s 间隔可覆盖 ~30s 生成）
//   - 每条消息编辑预算 15 次，到上限 finalize 当前消息、开新消息继续
//   - 编辑失败自适应退避（翻倍，最多 10s）；连续失败 3 次进 fallback
//   - 完成时编辑最终完整答案

const (
	maxEditsPerMessage = 15 // 单条消息编辑预算（留余量给 20 次上限）
	streamEditInterval = 1500 * time.Millisecond
	maxEditBackoff     = 10 * time.Second
	maxFloodStrikes    = 3
)

type streamState struct {
	msgID        string    // 已发送的消息 ID，流式期间反复编辑它
	openID       string    // 接收者 open_id（roll 新消息用，不能拿 session 名当 open_id！）
	session      string
	full         string    // 累积的完整文本（每次编辑发全文，保证前面的内容不丢）
	buf          string    // 待 flush 的增量（防抖用）
	dirty        bool
	timer        *time.Timer
	editCount    int       // 本条消息已编辑次数
	editInterval time.Duration // 当前编辑间隔（自适应退避）
	floodStrikes int       // 连续编辑失败次数
	lastEdit     time.Time // 上次编辑时间
	fallback     bool      // fallback 模式：停止编辑，等最终结果直接发
	local        bool      // 命令行介入模式：输出打印到 stdout，不调飞书 API
}

// StreamBegin 开始流式输出：发送"处理中"消息并记录 message_id。
// openID=="local"（命令行介入）时直接打印到 stdout，不调飞书 API。
func (c *Client) StreamBegin(openID, session string) {
	if openID == "local" {
		fmt.Printf("\n⚡ [%s] 处理中...\n", session)
		c.streamMu.Lock()
		defer c.streamMu.Unlock()
		c.streams[openID+"|"+session] = &streamState{msgID: "", openID: openID, session: session, lastEdit: time.Now(), local: true}
		return
	}
	key := openID + "|" + session
	content := fmt.Sprintf("⚡ [%s] 处理中...", session)
	msgID, err := c.sendAndGetID(openID, content)
	if err != nil {
		log.Printf("[feishu] stream begin failed: %v", err)
		return
	}
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	c.streams[key] = &streamState{msgID: msgID, openID: openID, session: session, lastEdit: time.Now()}
}

// StreamChunk 累积增量，按编辑间隔/字符阈值编辑同一消息；
// 本条消息编辑预算耗尽时 finalize 并开新消息继续
func (c *Client) StreamChunk(openID, session, chunk string) {
	key := openID + "|" + session
	c.streamMu.Lock()
	st := c.streams[key]
	if st != nil && st.local {
		// 命令行介入：增量直接打印（无编辑机制）
		fmt.Print(chunk)
		c.streamMu.Unlock()
		return
	}
	if st == nil {
		// 没有活动流：退化到普通分块发送
		c.streamMu.Unlock()
		c.SendChunk(openID, session, chunk)
		return
	}
	if st.fallback {
		// fallback 模式：只累积不编辑，等 StreamEnd 发完整结果
		st.buf += chunk
		st.full += chunk
		c.streamMu.Unlock()
		return
	}
	st.buf += chunk
	st.full += chunk
	st.dirty = true
	now := time.Now()
	// 编辑预算耗尽：finalize 当前消息（编辑为当前全文），开新消息继续
	if st.editCount >= maxEditsPerMessage {
		c.rollToNewMessageLocked(st, key)
		c.streamMu.Unlock()
		return
	}
	// 距上次编辑 ≥ editInterval（自适应）或 累积 ≥ 1200 字符：立即 flush
	if st.timer == nil {
		interval := st.editInterval
		if interval <= 0 {
			interval = streamEditInterval
		}
		st.timer = time.AfterFunc(interval, func() {
			c.flushStream(key)
		})
	}
	if now.Sub(st.lastEdit) >= st.effectiveInterval() || len([]rune(st.buf)) >= 1200 {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		c.streamMu.Unlock()
		c.flushStream(key)
		return
	}
	c.streamMu.Unlock()
}

// effectiveInterval 返回当前生效的编辑间隔（自适应退避后的）
func (st *streamState) effectiveInterval() time.Duration {
	if st.editInterval <= 0 {
		return streamEditInterval
	}
	return st.editInterval
}

// rollToNewMessageLocked 编辑预算耗尽：当前消息 finalize（不再编辑），
// 开一条新消息记录后续增量，后续编辑指向新消息。调用方必须持有 streamMu。
func (c *Client) rollToNewMessageLocked(st *streamState, key string) {
	msgID := st.msgID
	session := st.session
	prefix := fmt.Sprintf("⚡ [%s] ", session)
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	// 当前消息 finalize：编辑为当前全文（预算未到 20，还能编辑一次）
	cur := prefix + st.full
	go func() {
		if err := c.EditMessage(msgID, cur); err != nil {
			log.Printf("[feishu] roll finalize edit failed: %v", err)
		}
	}()
	// 开新消息：从当前全文继续（新消息获得新的 20 次编辑预算）
	newID, err := c.sendAndGetID(st.openID, cur)
	if err != nil {
		// 新消息失败：进入 fallback，等最终结果直接发
		log.Printf("[feishu] roll new message failed, fallback: %v", err)
		st.fallback = true
		st.buf = ""
		st.dirty = false
		return
	}
	st.msgID = newID
	st.editCount = 0
	st.floodStrikes = 0
	st.editInterval = 0
	st.lastEdit = time.Now()
}

// StreamEnd 结束流式：编辑为最终结果（成功/失败），并清理状态
func (c *Client) StreamEnd(openID, session, final string, success bool) {
	key := openID + "|" + session
	c.streamMu.Lock()
	st := c.streams[key]
	if st != nil && st.local {
		// 命令行介入：打印最终结果并清理
		delete(c.streams, key)
		c.streamMu.Unlock()
		prefix := "✅"
		if !success {
			prefix = "❌"
		}
		fmt.Printf("\n%s [%s] %s\n", prefix, session, final)
		return
	}
	if st == nil {
		c.streamMu.Unlock()
		// 没有活动流：直接发一条结果
		prefix := "✅"
		if !success {
			prefix = "❌"
		}
		c.SendChunk(openID, session, fmt.Sprintf("%s %s", prefix, final))
		return
	}
	wasFallback := st.fallback
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	msgID := st.msgID
	full := st.full
	delete(c.streams, key)
	c.streamMu.Unlock()

	prefix := "✅"
	if !success {
		prefix = "❌"
	}
	content := fmt.Sprintf("%s [%s] %s", prefix, session, final)
	if wasFallback {
		// fallback 模式：直接发新消息（编辑旧消息大概率失败）
		c.Send(context.Background(), openID, content)
		return
	}
	if err := c.EditMessage(msgID, content); err != nil {
		log.Printf("[feishu] stream end edit failed: %v (fallback send)", err)
		c.Send(context.Background(), openID, content)
	}
	// 如果流式期间编辑过（full 非空），确保最终内容完整（含之前展示的全文）
	_ = full
}

// flushStream 把累积全文编辑到已发送消息（不是增量——保证前面的内容不丢）。
// 编辑失败：限流类错误 → 自适应退避（间隔翻倍）；连续失败超阈值 → fallback。
func (c *Client) flushStream(key string) {
	c.streamMu.Lock()
	st := c.streams[key]
	if st == nil || !st.dirty || st.fallback {
		c.streamMu.Unlock()
		return
	}
	st.dirty = false
	msgID := st.msgID
	session := st.session
	full := st.full
	st.buf = ""
	st.lastEdit = time.Now()
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	c.streamMu.Unlock()

	content := fmt.Sprintf("⚡ [%s] %s", session, full)
	if err := c.EditMessage(msgID, content); err != nil {
		log.Printf("[feishu] stream edit failed: %v", err)
		c.handleEditFailure(key, err)
		return
	}
	// 成功：重置失败计数
	c.streamMu.Lock()
	if st := c.streams[key]; st != nil {
		st.floodStrikes = 0
		st.editCount++
	}
	c.streamMu.Unlock()
}

// handleEditFailure 编辑失败处理：限流→退避；连续失败→fallback（等最终结果）
func (c *Client) handleEditFailure(key string, err error) {
	msg := err.Error()
	isFlood := strings.Contains(msg, "flood") || strings.Contains(msg, "rate") ||
		strings.Contains(msg, "retry") || strings.Contains(msg, "230072") ||
		strings.Contains(msg, "99991661") || strings.Contains(msg, "99991663")
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	st := c.streams[key]
	if st == nil {
		return
	}
	if isFlood {
		st.floodStrikes++
		// 退避：间隔翻倍（最多 10s）
		if st.editInterval <= 0 {
			st.editInterval = streamEditInterval
		}
		st.editInterval = minDur(st.editInterval*2, maxEditBackoff)
		if st.floodStrikes >= maxFloodStrikes {
			log.Printf("[feishu] stream edit flood strikes=%d, entering fallback", st.floodStrikes)
			st.fallback = true
		}
		return
	}
	// 非限流错误（如 230072 编辑次数超限）：直接 fallback
	log.Printf("[feishu] stream edit non-flood error, entering fallback: %v", err)
	st.fallback = true
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// sendAndGetID 发送消息并返回 message_id
func (c *Client) sendAndGetID(openID, content string) (string, error) {
	token, err := c.getAccessToken(context.Background())
	if err != nil {
		return "", err
	}
	contentJSON, _ := json.Marshal(map[string]string{"text": content})
	payload := map[string]interface{}{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    string(contentJSON),
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.Background(), "POST",
		"https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id",
		bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &result)
	if resp.StatusCode != 200 || result.Code != 0 {
		return "", fmt.Errorf("create: status=%d code=%d resp=%s", resp.StatusCode, result.Code, truncate(string(body), 300))
	}
	return result.Data.MessageID, nil
}

// EditMessage 编辑已发送消息（PUT /im/v1/messages/:message_id）
func (c *Client) EditMessage(messageID, content string) error {
	token, err := c.getAccessToken(context.Background())
	if err != nil {
		return err
	}
	contentJSON, _ := json.Marshal(map[string]string{"text": content})
	payload := map[string]interface{}{
		"msg_type": "text",
		"content":  string(contentJSON),
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(context.Background(), "PUT",
		"https://open.feishu.cn/open-apis/im/v1/messages/"+messageID,
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	json.Unmarshal(body, &result)
	// 注意：HTTP 200 时 body 的 code 仍可能非 0（如 230072 编辑次数超限/99991663 消息不可编辑），
	// 必须同时检查 code，否则会静默吞掉失败（用户看到"处理中"不更新）。
	if resp.StatusCode != 200 || result.Code != 0 {
		return fmt.Errorf("edit: status=%d code=%d msg=%q resp=%s", resp.StatusCode, result.Code, result.Msg, truncate(string(body), 300))
	}
	return nil
}

// extractText 从飞书消息 Content（JSON 序列化字符串）中提取纯文本。
// 文本消息格式: {"text":"你好"}；其他类型（卡片/图片等）返回空串。
func extractText(content string) string {
	var v struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &v); err == nil && v.Text != "" {
		return v.Text
	}
	return ""
}

// Start 启动飞书长连接（官方 SDK WS 客户端：自动端点发现/心跳/重连）。
// handler 收到 P2MessageReceiveV1 事件（含文本消息）。
func (c *Client) Start(ctx context.Context, handler func(Event)) error {
	dispatcher := larkevent.NewEventDispatcher("", "")
	dispatcher.OnP2MessageReceiveV1(func(evCtx context.Context, event *larkim.P2MessageReceiveV1) error {
		msg := event.Event.Message
		content := ""
		if msg != nil && msg.Content != nil {
			// Content 是 JSON 序列化字符串，如 {"text":"你好"}；解析出 text 字段
			content = extractText(*msg.Content)
		}
		senderID := ""
		if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
			if oid := event.Event.Sender.SenderId.OpenId; oid != nil {
				senderID = *oid
			}
		}
		log.Printf("[feishu] msg received, sender=%s content=%s", senderID, content)
		handler(Event{Type: "message", SenderID: senderID, Message: Message{Content: content}})
		return nil
	})

	cli := larkws.NewClient(c.cfg.AppID, c.cfg.AppSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithLogLevel(larkcore.LogLevelError),
	)
	cli.SetOnReady(func() {
		log.Printf("[feishu] WebSocket connected (long connection)")
	})
	cli.SetOnDisconnected(func() {
		log.Printf("[feishu] WebSocket disconnected, will reconnect")
	})
	cli.SetOnError(func(err error) {
		log.Printf("[feishu] ws error: %v", err)
	})

	go func() {
		if err := cli.Start(ctx); err != nil {
			log.Printf("[feishu] ws start failed: %v", err)
		}
	}()

	// 简单探测：等 3 秒看是否连接成功（OnReady 会打印日志）
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(3 * time.Second):
	}
	return nil
}
