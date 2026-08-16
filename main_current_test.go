package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"feisuo/internal/feishu"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/store"
)

// mockFeishu 返回 token 和消息接口都 200 的 mock 飞书
func mockFeishu() (*httptest.Server, *feishu.Client) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0, "tenant_access_token": "mock-token", "expire": 7200,
			})
		default: // 发消息/编辑
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0, "data": map[string]interface{}{"message_id": "mock-msg"},
			})
		}
	}))
	fc := feishu.New(feishu.Config{AppID: "app", AppSecret: "sec", BotName: "飞梭"})
	fc.SetBaseURL(srv.URL) // 测试注入 mock 端点
	return srv, fc
}

// TestToSwitchThenPlainGoesToSwitchedSession：
// /TO 切换会话（无消息）→ 后续普通消息发往新会话
func TestToSwitchThenPlainGoesToSwitchedSession(t *testing.T) {
	srv, fc := mockFeishu()
	defer srv.Close()

	sm := session.New()
	sm.EnsureDefault("main")
	sm.Create("work")

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	startLoop := func(string) {}

	// 1. 普通消息默认发 main
	handleFeishuEvent(ctx, feishu.Event{Type: "message", SenderID: "ou_u", Message: feishu.Message{Content: "你好"}},
		fc, protocol.New(), sm, nil, st, startLoop, "main")
	if got := peekQueueTarget(sm, "main"); !got {
		t.Fatal("切换前普通消息应进 main 会话队列")
	}

	// 2. /TO work（无消息）= 纯切换
	cmd := protocol.New().Parse(`/TO work`)
	if cmd.Type != protocol.CmdCommand || cmd.Session != "work" || cmd.Arg != "" {
		t.Fatalf("解析 /TO work 失败: %+v", cmd)
	}
	handleCommand(ctx, cmd, fc, sm, "ou_u", st, startLoop, nil, "main")
	if got := sm.Current("ou_u", "main"); got != "work" {
		t.Fatalf("/TO work 后当前会话应为 work, got %q", got)
	}

	// 3. 普通消息 → 应进 work 会话
	handleFeishuEvent(ctx, feishu.Event{Type: "message", SenderID: "ou_u", Message: feishu.Message{Content: "在 work 里聊聊"}},
		fc, protocol.New(), sm, nil, st, startLoop, "main")
	if got := peekQueueTarget(sm, "work"); !got {
		t.Fatal("切换后普通消息应进 work 会话队列")
	}
}

// TestToWithMessageSwitchesAndSends：/TO 带消息 = 切换 + 发送
func TestToWithMessageSwitchesAndSends(t *testing.T) {
	srv, fc := mockFeishu()
	defer srv.Close()

	sm := session.New()
	sm.EnsureDefault("main")
	sm.Create("work")

	cmd := protocol.New().Parse(`/TO work 发条消息`)
	handleCommand(context.Background(), cmd, fc, sm, "ou_u", mustOpenStore(t), func(string) {}, nil, "main")
	if got := sm.Current("ou_u", "main"); got != "work" {
		t.Fatalf("/TO work 带消息后当前会话应为 work, got %q", got)
	}
	if got := peekQueueTarget(sm, "work"); !got {
		t.Fatal("/TO work 带消息应入 work 队列")
	}
}

// TestBridgeDoesNotChangeCurrent：bridge 转发不改变当前会话
func TestBridgeDoesNotChangeCurrent(t *testing.T) {
	sm := session.New()
	sm.EnsureDefault("main")
	sm.Create("a")
	sm.Create("b")
	sm.SetCurrent("ou_u", "a")

	// 模拟 bridge 转发：直接 Enqueue 到 b（不经过 current 逻辑）
	sm.Enqueue("b", session.Message{Content: "转发", Sender: "ou_u", Origin: "a"})
	if got := sm.Current("ou_u", "main"); got != "a" {
		t.Fatalf("bridge 转发后当前会话应保持 a, got %q", got)
	}
}

// peekQueueTarget 检查指定会话队列里是否有消息（非阻塞）
func peekQueueTarget(sm *session.Manager, name string) bool {
	se, ok := sm.Get(name)
	if !ok || se.Queue == nil {
		return false
	}
	select {
	case <-se.Queue:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func mustOpenStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}
