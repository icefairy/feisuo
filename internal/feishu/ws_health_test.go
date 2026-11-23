package feishu

import (
	"testing"
	"time"
)

// C 方案：wsIsDead 基于「断连状态 + 断连时长」而非「空闲事件时长」判活。
// 因此连接正常时，哪怕很久没有入站事件（长任务场景）也绝不判死。
func TestWSIsDead(t *testing.T) {
	c := &Client{wsDeadline: 5 * time.Minute}

	// 从未连接成功（首连/重连进行中）：交给 larkws 自己重试，不干预
	if c.wsIsDead() {
		t.Fatal("never-connected client must not be judged dead")
	}

	// 连接正常（哪怕刚 markConnected）：不判死
	c.wsMarkConnected()
	if c.wsIsDead() {
		t.Fatal("connected client must not be dead")
	}

	// 刚断连，仍在阈值内：不判死（larkws 会自带重连）
	c.wsMarkBroken()
	if c.wsIsDead() {
		t.Fatal("client broken just now (within threshold) must not be dead yet")
	}

	// 断连且超过阈值未恢复：判死
	c.wsConnMu.Lock()
	c.wsBrokenSince = time.Now().Add(-1 * time.Hour)
	c.wsConnMu.Unlock()
	if !c.wsIsDead() {
		t.Fatal("client broken beyond threshold must be dead")
	}

	// 重连恢复：立即不判死
	c.wsMarkConnected()
	if c.wsIsDead() {
		t.Fatal("reconnected client must not be dead")
	}

	// 禁用阈值：永不判死
	c2 := &Client{wsDeadline: 0}
	c2.wsMarkConnected()
	c2.wsMarkBroken()
	if c2.wsIsDead() {
		t.Fatal("threshold disabled must never be dead")
	}
}
