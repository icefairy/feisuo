package session

import "testing"

// TestWorkDirPerSession：工作目录按会话保存，互不影响
func TestWorkDirPerSession(t *testing.T) {
	m := New()
	m.Create("repo")
	m.Create("ops")
	if got := m.WorkDir("repo"); got != "" {
		t.Fatalf("新建会话工作目录应为空, got %q", got)
	}
	if !m.SetWorkDir("repo", "/data/codes/feisuo") {
		t.Fatal("SetWorkDir 应成功")
	}
	if got := m.WorkDir("repo"); got != "/data/codes/feisuo" {
		t.Fatalf("工作目录读回不一致, got %q", got)
	}
	if got := m.WorkDir("ops"); got != "" {
		t.Fatalf("其他会话不应受影响, got %q", got)
	}
	if got := m.WorkDir("missing"); got != "" {
		t.Fatalf("不存在的会话应返回空, got %q", got)
	}
	if m.SetWorkDir("missing", "/tmp") {
		t.Fatal("不存在的会话 SetWorkDir 应失败")
	}
}

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
