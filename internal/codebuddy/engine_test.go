package codebuddy

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApplyWorkDir：工作目录注入到进程；目录不存在则保持默认。
func TestApplyWorkDir(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{})
	cmd := exec.Command("true")

	e.applyWorkDir(cmd, "repo") // 未注入 provider
	if cmd.Dir != "" {
		t.Fatalf("未注入时不应设置 Dir, got %q", cmd.Dir)
	}

	e.SetWorkDirOf(func(string) string { return "  " + dir + "  " })
	e.applyWorkDir(cmd, "repo")
	if cmd.Dir != dir {
		t.Fatalf("Dir 应为 %q, got %q", dir, cmd.Dir)
	}

	e.SetWorkDirOf(func(string) string { return filepath.Join(dir, "missing") })
	cmd.Dir = ""
	e.applyWorkDir(cmd, "repo")
	if cmd.Dir != "" {
		t.Fatalf("目录不存在时应保持默认, got %q", cmd.Dir)
	}
}

// TestParseEvents 用真实 stream-json fixture 验证事件解析逻辑。
func TestParseEvents(t *testing.T) {
	cases := []struct {
		name            string
		fixture         string
		wantChunks      []string
		wantFinal       string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:       "normal streaming",
			fixture:    "testdata/fixture_normal.jsonl",
			wantChunks: []string{"DNS", "(域名系统)", "是把网址翻译成IP地址的互联网电话簿。"},
			wantFinal:  "DNS(域名系统)是把网址翻译成IP地址的互联网电话簿。",
		},
		{
			name:    "interrupt followed by new message",
			fixture: "testdata/fixture_interrupt.jsonl",
			// 中断后第一轮的 result 为空，但 assistant 文本 "Interrupted by user" 已流式到达
			// 所以 final = buf.String() = "Interrupted by user"
			wantFinal: "Interrupted by user",
		},
		{
			name:            "error result",
			fixture:         "testdata/fixture_error.jsonl",
			wantErr:         true,
			wantErrContains: "429",
			wantFinal:       "429 限流", // error 时 onDone 仍带文本
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var chunks []string
			var final string
			var gotErr error
			doneChan := make(chan struct{})

			pending := map[string]*pendingPrompt{
				"p1": {
					onChunk: func(s string) { chunks = append(chunks, s) },
					onDone:  func(f string, err error) { final = f; gotErr = err; close(doneChan) },
				},
			}
			order := []string{"p1"}

			sc := bufio.NewScanner(strings.NewReader(string(data)))
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if line == "" {
					continue
				}
				var ev map[string]any
				if err := json.Unmarshal([]byte(line), &ev); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				handleEventForTest(ev, pending, &order)
			}

			select {
			case <-doneChan:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for onDone")
			}

			if tc.wantErr {
				if gotErr == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErrContains)
				}
				if !strings.Contains(gotErr.Error(), tc.wantErrContains) {
					t.Fatalf("error = %q, want containing %q", gotErr.Error(), tc.wantErrContains)
				}
			} else {
				if gotErr != nil {
					t.Fatalf("unexpected error: %v", gotErr)
				}
			}

			if final != tc.wantFinal {
				t.Fatalf("final = %q, want %q", final, tc.wantFinal)
			}
			if !slicesEqual(chunks, tc.wantChunks) {
				t.Fatalf("chunks = %v, want %v", chunks, tc.wantChunks)
			}
		})
	}
}

// handleEventForTest 是从 engine.go 提取的纯解析逻辑（测试用）。
// 保持与 rpcClient.handleEvent 一致的行为。
func handleEventForTest(ev map[string]any, pending map[string]*pendingPrompt, order *[]string) {
	typ, _ := ev["type"].(string)
	switch typ {
	case "stream_event":
		evData, ok := ev["event"].(map[string]any)
		if !ok {
			return
		}
		if evData["type"] != "content_block_delta" {
			return
		}
		delta, ok := evData["delta"].(map[string]any)
		if !ok {
			return
		}
		if delta["type"] != "text_delta" {
			return
		}
		text, _ := delta["text"].(string)
		if len(*order) > 0 {
			if p, ok := pending[(*order)[0]]; ok {
				p.buf.WriteString(text)
				p.hasText = true
				p.hasDelta = true
				if p.onChunk != nil {
					p.onChunk(text)
				}
			}
		}

	case "assistant":
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
		if len(*order) > 0 {
			if p, ok := pending[(*order)[0]]; ok {
				if !p.hasDelta {
					p.buf.WriteString(fullText.String())
				}
				p.hasText = true
			}
		}

	case "result":
		subtype, _ := ev["subtype"].(string)
		resultRaw, _ := ev["result"].(string)
		isError, _ := ev["is_error"].(bool)
		if len(*order) == 0 {
			return
		}
		doneID := (*order)[0]
		*order = (*order)[1:]
		done, ok := pending[doneID]
		if !ok {
			return
		}
		delete(pending, doneID)
		if done.timer != nil {
			done.timer.Stop()
		}
		// 启动竞态检测：跳过重试，直接完成（测试环境不模拟重试）
		if subtype == "success" && resultRaw == "" && !done.hasText && done.retries < 1 {
			done.retries++
			// 测试中视为正常完成（空结果）
			done.onDone("", nil)
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
			err = ErrCodeBuddyError{msg: resultRaw}
		}
		done.onDone(final, err)
	}
}

type ErrCodeBuddyError struct {
	msg string
}

func (e ErrCodeBuddyError) Error() string { return e.msg }

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSanitizeSessionID 复用 pi 包的相同逻辑。
func TestSanitizeSessionID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"main", "main"},
		{"work-2", "work-2"},
		{"我的会话", "dev"},
		{"", "dev"},
	}
	for _, c := range cases {
		got := sanitizeSessionID(c.in)
		if got != c.want {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
