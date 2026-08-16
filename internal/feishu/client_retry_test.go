package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newMockServer 返回 mock 飞书服务器，可控制 token 获取和消息发送失败
func newMockServer(t *testing.T, sendFailures int) (*httptest.Server, *int, *int) {
	t.Helper()
	var mu sync.Mutex
	sendCalls := 0
	tokenCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Path, "/auth/") {
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tok-1", "expire": 7200})
			return
		}
		if strings.Contains(r.URL.Path, "/im/v1/messages") && r.Method == http.MethodPost {
			sendCalls++
			if sendCalls <= sendFailures {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "Invalid access token"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om-123"}})
			return
		}
		w.WriteHeader(404)
	}))
	return srv, &sendCalls, &tokenCalls
}

func newTestClient(baseURL string) *Client {
	c := New(Config{AppID: "test", AppSecret: "secret", BotName: "test"})
	c.httpCli = &http.Client{Timeout: 5 * time.Second}
	tokenURL = baseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	sendMsgURL = baseURL + "/open-apis/im/v1/messages?receive_id_type=open_id"
	editMsgURL = baseURL + "/open-apis/im/v1/messages/"
	return c
}

// TestSendTokenErrorRefreshesAndRetries：token 过期 → 强制刷新 → 重试成功
func TestSendTokenErrorRefreshesAndRetries(t *testing.T) {
	srv, sendCalls, tokenCalls := newMockServer(t, 1)
	defer srv.Close()

	c := newTestClient(srv.URL)
	// 塞一个已过期 token，强制走刷新路径
	c.token = "stale-token"
	c.tokenExp = time.Now().Add(-time.Minute)

	err := c.Send(context.Background(), "ou_test", "hello")
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if *sendCalls != 2 {
		t.Errorf("want 2 send calls (1 fail + 1 retry), got %d", *sendCalls)
	}
	if *tokenCalls < 1 {
		t.Errorf("want >=1 token refresh call, got %d", *tokenCalls)
	}
}

// TestSendNetworkErrorRetries：全部失败 → 3 次退避重试
func TestSendNetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/auth/") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tok-1", "expire": 7200})
			return
		}
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	start := time.Now()
	err := c.Send(context.Background(), "ou_test", "hello")
	if err == nil {
		t.Fatal("want error after all retries")
	}
	elapsed := time.Since(start)
	if elapsed < 3*time.Second {
		t.Errorf("retry backoff too short: %v (want >=3s for 1s+2s)", elapsed)
	}
}

// TestSendAndGetIDTokenError：sendAndGetID 同样走 token 刷新重试
func TestSendAndGetIDTokenError(t *testing.T) {
	srv, sendCalls, _ := newMockServer(t, 1)
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.token = "stale-token"
	c.tokenExp = time.Now().Add(-time.Minute)

	id, err := c.sendAndGetID("ou_test", "hello")
	if err != nil {
		t.Fatalf("sendAndGetID failed: %v", err)
	}
	if id != "om-123" {
		t.Errorf("want message_id om-123, got %q", id)
	}
	if *sendCalls != 2 {
		t.Errorf("want 2 send calls, got %d", *sendCalls)
	}
}

// TestEditMessageTokenError：编辑同样 token 刷新重试
func TestEditMessageTokenError(t *testing.T) {
	var mu sync.Mutex
	editCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Path, "/auth/") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tok-1", "expire": 7200})
			return
		}
		if r.Method == http.MethodPut {
			editCalls++
			if editCalls <= 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "Invalid access token"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.token = "stale-token"
	c.tokenExp = time.Now().Add(-time.Minute)

	if err := c.EditMessage("om-123", "new content"); err != nil {
		t.Fatalf("EditMessage failed: %v", err)
	}
	if editCalls != 2 {
		t.Errorf("want 2 edit calls, got %d", editCalls)
	}
}
