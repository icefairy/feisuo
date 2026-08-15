package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	cfg     Config
	token   string
	tokenMu sync.RWMutex
	httpCli *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, httpCli: &http.Client{Timeout: 30 * time.Second}}
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
		Data struct {
			TenantToken string `json:"tenant_access_token"`
			Expire      int    `json:"expire"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("token decode: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("token API: code=%d msg=%s", result.Code, result.Msg)
	}
	c.token = result.Data.TenantToken
	log.Printf("[feishu] token obtained, expire=%ds", result.Data.Expire)
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

// Send 发送飞书文本消息
func (c *Client) Send(ctx context.Context, openID string, content string) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	payload := map[string]interface{}{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    map[string]string{"text": content},
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
		var result struct{ Code int `json:"code"` }
		json.NewDecoder(resp.Body).Decode(&result)
		return fmt.Errorf("send: status=%d code=%d", resp.StatusCode, result.Code)
	}
	return nil
}

// Reply 发送带前缀的消息
func (c *Client) Reply(openID, content string) {
	content = fmt.Sprintf("🤖 [%s] %s", c.cfg.BotName, content)
	if err := c.Send(context.Background(), openID, content); err != nil {
		log.Printf("[feishu] reply failed: %v", err)
	}
}

// SendChunk 发送流式分块（用于 Pi 执行中的进度）
func (c *Client) SendChunk(openID, session, chunk string) {
	prefix := fmt.Sprintf("⚡ [%s]", session)
	content := prefix + " " + chunk
	// 前缀截断避免飞书消息过长
	if len(content) > 1500 {
		content = content[:1497] + "..."
	}
	if err := c.Send(context.Background(), openID, content); err != nil {
		log.Printf("[feishu] chunk send failed: %v", err)
	}
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
