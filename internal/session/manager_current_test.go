package session

import "testing"

// TestCurrentFallback：未 /TO 切换时普通消息发默认会话
func TestCurrentFallback(t *testing.T) {
	m := New()
	if got := m.Current("ou_user", "main"); got != "main" {
		t.Fatalf("未设置 current 应回退 main, got %q", got)
	}
}

// TestSetCurrentSwitches：/TO 切换后普通消息发往新会话
func TestSetCurrentSwitches(t *testing.T) {
	m := New()
	m.SetCurrent("ou_user", "work")
	if got := m.Current("ou_user", "main"); got != "work" {
		t.Fatalf("切换后应发 work, got %q", got)
	}
}

// TestCurrentPerSender：不同 sender 各自独立（互不干扰）
func TestCurrentPerSender(t *testing.T) {
	m := New()
	m.SetCurrent("ou_a", "work")
	m.SetCurrent("ou_b", "ops")
	if got := m.Current("ou_a", "main"); got != "work" {
		t.Fatalf("ou_a 应发 work, got %q", got)
	}
	if got := m.Current("ou_b", "main"); got != "ops" {
		t.Fatalf("ou_b 应发 ops, got %q", got)
	}
	if got := m.Current("ou_c", "main"); got != "main" {
		t.Fatalf("ou_c 未设置应回退 main, got %q", got)
	}
}

// TestCurrentReSwitch：明确 /TO 可精确切回
func TestCurrentReSwitch(t *testing.T) {
	m := New()
	m.SetCurrent("ou_user", "work")
	m.SetCurrent("ou_user", "main")
	if got := m.Current("ou_user", "main"); got != "main" {
		t.Fatalf("再次 /TO 应切回 main, got %q", got)
	}
}
