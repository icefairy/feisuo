package pi

import "testing"

func TestSanitizeSessionID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"main", "main"},
		{"ice", "ice"},
		{"我的会话", "dev"}, // 非 ASCII 全转 -→ Trim 为空→回退 dev
		{"work-2", "work-2"},
		{"a_b", "a_b"},
		{"", "dev"}, // 空回退 dev
	}
	for _, c := range cases {
		got := sanitizeSessionID(c.in)
		if got != c.want {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionIDPrefix(t *testing.T) {
	// 每个会话固定前缀 feisuo-，确保跨重启稳定
	got := "feisuo-" + sanitizeSessionID("ice")
	want := "feisuo-ice"
	if got != want {
		t.Errorf("session id = %q, want %q", got, want)
	}
}

func TestAbortNoProcess(t *testing.T) {
	// 没有常驻进程的会话：Abort 应返回错误，且不得隐式启动新进程（否则会泄漏空闲进程）
	e := New(Config{})
	if err := e.Abort("ghost"); err == nil {
		t.Fatal("Abort on session without rpc process should return error")
	}
	if n := e.SessionCount(); n != 0 {
		t.Errorf("Abort must not spawn a process, session count = %d", n)
	}
}
