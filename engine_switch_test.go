package main

import (
	"context"
	"sync"
	"testing"

	"feisuo/internal/config"
	"feisuo/internal/engine"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/store"
)

// fakeEngine 是 engine.Engine 的最小实现，记录是否被 Close。
type fakeEngine struct {
	mu     sync.Mutex
	closed bool
	name   string
}

func (f *fakeEngine) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
func (f *fakeEngine) Execute(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error)) {
}
func (f *fakeEngine) ExecuteSteer(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error)) {
}
func (f *fakeEngine) Steer(session, msg string) error { return nil }
func (f *fakeEngine) Abort(session string) error      { return nil }
func (f *fakeEngine) Reset(session string) error      { return nil }
func (f *fakeEngine) Kill(session string)             {}
func (f *fakeEngine) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}
func (f *fakeEngine) SessionCount() int                        { return 0 }
func (f *fakeEngine) SetSessionList(fn func() []string)        {}
func (f *fakeEngine) SetSystemPromptOf(fn func(string) string) {}
func (f *fakeEngine) SetWorkDirOf(fn func(string) string)      {}
func (f *fakeEngine) asEngine() engine.Engine                  { return f }

func TestEngineSwitcherSwapClosesOld(t *testing.T) {
	old := &fakeEngine{name: "old"}
	sw := newEngineSwitcher(old.asEngine())

	got, ok := sw.get().(*fakeEngine)
	if !ok || got != old {
		t.Fatal("get() 应返回初始引擎")
	}

	next := &fakeEngine{name: "next"}
	sw.swap(next.asEngine())

	if !old.wasClosed() {
		t.Fatal("swap 应关闭旧引擎")
	}
	if got := sw.get(); got != next {
		t.Fatalf("swap 后 get() 应返回新引擎, got=%v", got)
	}
}

func TestEngineSwitcherNilSafe(t *testing.T) {
	sw := newEngineSwitcher(nil)
	// 各方法在底层引擎为 nil 时不应 panic
	sw.Kill("x")
	sw.Close()
	if sw.SessionCount() != 0 {
		t.Fatal("nil 引擎 SessionCount 应返回 0")
	}
}

// TestSwitchCommandSwapsAndPersists：通过 handleCommand 走 /switch pi，
// 验证底层引擎被替换（旧引擎关闭）且选择持久化到磁盘。
func TestSwitchCommandSwapsAndPersists(t *testing.T) {
	dir := t.TempDir()
	old := &fakeEngine{name: "old"}
	sw := newEngineSwitcher(old.asEngine())

	sm := session.New()
	srv, fc := mockFeishu()
	defer srv.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Sessions.DataDir = dir
	cfg.Sessions.DefaultName = "main"

	cmd := protocol.New().Parse("/switch pi")
	if cmd.Cmd != "switch" || cmd.Arg != "pi" {
		t.Fatalf("解析 /switch pi 失败: %+v", cmd)
	}
	handleCommand(context.Background(), cmd, fc, sm, "ou_u", st, func(string) {}, sw, cfg, "main")

	if !old.wasClosed() {
		t.Fatal("/switch 应关闭旧引擎进程")
	}
	if got := config.LoadEngineChoice(dir); got != "pi" {
		t.Fatalf("引擎选择应持久化为 pi, got %q", got)
	}
}
