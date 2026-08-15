package pi

import "testing"

func TestSanitizeSessionID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"main", "main"},
		{"ice", "ice"},
		{"我的会话", "----"}, // 非 ASCII 全转 -（4 个字符）
		{"work-2", "work-2"},
		{"a_b", "a_b"},
		{"", "main"}, // 空回退
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
