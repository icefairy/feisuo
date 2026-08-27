package feishu

import (
	"testing"
	"time"
)

func TestWSIsDead(t *testing.T) {
	c := &Client{wsDeadline: 5 * time.Minute}
	// 刚 markAlive → 未超时
	c.markAlive()
	if c.wsIsDead() {
		t.Fatal("expected not dead right after markAlive")
	}
	// 模拟很久没事件
	c.wsAliveMu.Lock()
	c.wsLastEvent = time.Now().Add(-time.Hour)
	c.wsAliveMu.Unlock()
	if !c.wsIsDead() {
		t.Fatal("expected dead after 1h idle beyond 5m threshold")
	}
	// 禁用阈值时永不发生僵死判定
	c2 := &Client{wsDeadline: 0}
	c2.markAlive()
	if c2.wsIsDead() {
		t.Fatal("expected never dead when threshold disabled")
	}
}
