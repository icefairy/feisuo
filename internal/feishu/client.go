package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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
		"receive_id":  openID,
		"msg_type":    "text",
		"content":     map[string]string{"text": content},
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

// Start 启动 WebSocket 连接
func (c *Client) Start(ctx context.Context, handler func(Event)) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("websocket: token %w", err)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, _, err := dialer.Dial(c.cfg.WsURL, headers)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	log.Printf("[feishu] WebSocket connected to %s", c.cfg.WsURL)

	go c.wsLoop(ctx, conn, handler)

	// 启动时重连
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(5 * time.Second)
				if conn.RemoteAddr() == nil {
					log.Printf("[feishu] connection lost, reconnecting...")
					err := c.Start(ctx, handler)
					if err != nil {
						log.Printf("[feishu] reconnect failed: %v", err)
					}
				}
			}
		}
	}()

	return nil
}

func (c *Client) wsLoop(ctx context.Context, conn *websocket.Conn, handler func(Event)) {
	defer func() {
		log.Printf("[feishu] WebSocket disconnected")
		conn.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				log.Printf("[feishu] ws read error: %v", err)
				time.Sleep(2 * time.Second)
				continue
			}

			var evt struct {
				EventType string `json:"event_type"`
				Header    struct {
					EventID string `json:"event_id"`
				} `json:"header"`
				Event struct {
					SenderID string `json:"sender_id"`
					Message  struct {
						Content string `json:"content"`
						Type    string `json:"msg_type"`
						Text    string `json:"text"`
					} `json:"message"`
				} `json:"event"`
			}

			if err := json.Unmarshal(msg, &evt); err != nil {
				log.Printf("[feishu] event decode error: %v", err)
				continue
			}

			// 只处理用户消息事件
			if evt.EventType != "im.message.receive_v1" {
				continue
			}

			// 获取消息内容
			text := evt.Event.Message.Text
			if text == "" {
				// 尝试从 content JSON 中解析
				var contentMap map[string]interface{}
				json.Unmarshal([]byte(evt.Event.Message.Content), &contentMap)
				if t, ok := contentMap["text"].(string); ok {
					text = t
				}
			}
			if text == "" {
				text = evt.Event.Message.Content
			}

			// 去掉 @机器人 标记
			text = strings.TrimPrefix(text, "@")

			if text == "" {
				continue
			}

			log.Printf("[feishu] msg from=%s content=%q", evt.Event.SenderID, text)

			handler(Event{
				Type:     evt.EventType,
				SenderID: evt.Event.SenderID,
				Message:  Message{Content: text},
			})
		}
	}
}