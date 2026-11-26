package session

import (
	"testing"
	"time"
)

// TestIdleSessions：只回收非 busy、队列为空且闲置超阈值的会话。
func TestIdleSessions(t *testing.T) {
	m := New()
	m.Create("idle")
	m.Create("busy")
	m.Create("queued")
	m.Create("stopped")

	// 全部伪造成很久没活动
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{"idle", "busy", "queued", "stopped"} {
		s, _ := m.Get(name)
		s.LastActiveAt = old
	}

	m.SetStatus("busy", "busy")
	m.Enqueue("queued", Message{Content: "hi", Sender: "u"}) // 入队会刷新活跃时间，且队列非空
	m.Kill("stopped")

	got := m.IdleSessions(time.Hour)
	if len(got) != 1 || got[0] != "idle" {
		t.Fatalf("应只回收 idle 会话, got %v", got)
	}

	// Touch 后不再算闲置
	m.Touch("idle")
	if got := m.IdleSessions(time.Hour); len(got) != 0 {
		t.Fatalf("Touch 后不应再回收, got %v", got)
	}

	// timeout<=0 表示关闭回收
	if got := m.IdleSessions(0); len(got) != 0 {
		t.Fatalf("timeout=0 应关闭回收, got %v", got)
	}
}
