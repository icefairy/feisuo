package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestContinuationText(t *testing.T) {
	cases := []struct {
		name         string
		shown, final string
		want         string
	}{
		{"无已显示内容发全文", "", "hello", "hello"},
		{"已显示前缀只发尾部", "ABC", "ABCDEF", "DEF"},
		{"不是前缀则发全文", "ABC", "XYZ", "XYZ"},
		{"边界空格去掉前导空白", "hello ", "hello world", "world"},
		{"全部已显示则空增量", "ABCDEF", "ABCDEF", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := continuationText(tc.shown, tc.final); got != tc.want {
				t.Fatalf("continuationText(%q, %q) = %q, want %q", tc.shown, tc.final, got, tc.want)
			}
		})
	}
}

func TestRollPiecesNoDuplicate(t *testing.T) {
	full := "AAAA BBBB CCCC"
	shown := "AAAA BBBB"
	oldCard, inc := rollPieces(full, shown)
	if oldCard != shown {
		t.Fatalf("旧卡应 finalize 为已显示内容, got %q want %q", oldCard, shown)
	}
	if strings.Contains(inc, shown) {
		t.Fatalf("新卡增量不应重复旧卡全文, inc=%q", inc)
	}
	if inc != "CCCC" {
		t.Fatalf("新卡增量应为 CCCC, got %q", inc)
	}
}

// TestStreamEndFallbackSendsOnlyTail：fallback 时只补发用户未看到的尾部，
// 不再重发完整 final（避免 ⚡ 半成品 + ✅ 全文重复）。
func TestStreamEndFallbackSendsOnlyTail(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0, "tenant_access_token": "t", "expire": 7200,
			})
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Content string `json:"content"`
		}
		json.Unmarshal(body, &payload)
		var inner struct {
			Text string `json:"text"`
		}
		json.Unmarshal([]byte(payload.Content), &inner)
		mu.Lock()
		sent = append(sent, inner.Text)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0, "data": map[string]interface{}{"message_id": "m"},
		})
	}))
	defer srv.Close()
	fc := New(Config{AppID: "a", AppSecret: "s", BotName: "飞梭"})
	fc.SetBaseURL(srv.URL)

	key := "ou|main"
	fc.streamMu.Lock()
	fc.streams[key] = &streamState{
		msgID: "m1", openID: "ou", session: "main",
		full: "ABCDEF", shown: "ABC", fallback: true,
	}
	fc.streamMu.Unlock()

	fc.StreamEnd("ou", "main", "ABCDEF", true)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("fallback 应发出一条续发消息")
	}
	got := sent[0]
	if !strings.Contains(got, "DEF") {
		t.Fatalf("应包含未显示的尾部 DEF, got %q", got)
	}
	if strings.Contains(got, "ABC") {
		t.Fatalf("不应重复已显示的 ABC, got %q", got)
	}
}
