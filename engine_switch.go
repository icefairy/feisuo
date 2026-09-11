package main

import (
	"context"
	"fmt"
	"sync"

	"feisuo/internal/engine"
)

// engineSwitcher 持有当前生效的引擎，支持运行时无锁切换（/switch 命令）。
// 所有会话循环都通过它访问引擎；切换时关闭旧引擎的全部常驻进程、换上新引擎，
// 已在跑的任务会在旧引擎关闭时通过错误回调自然结束。
//
// 并发安全：底层引擎指针由 RWMutex 保护。读（调用各方法）走 RLock 仅取指针，
// 取完即释放，不阻塞切换；切换走 Lock，替换指针后关闭旧引擎。
type engineSwitcher struct {
	mu      sync.RWMutex
	current engine.Engine
}

func newEngineSwitcher(e engine.Engine) *engineSwitcher {
	return &engineSwitcher{current: e}
}

func (s *engineSwitcher) get() engine.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// swap 关闭旧引擎并启用新引擎。已在进行的调用持有旧引擎副本，会自然结束；
// 之后的调用全部命中新引擎，因此 /switch 立即对新消息生效。
func (s *engineSwitcher) swap(next engine.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.current
	s.current = next
	if old != nil {
		old.Close()
	}
}

func (s *engineSwitcher) Execute(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error)) {
	if e := s.get(); e != nil {
		e.Execute(ctx, session, prompt, onChunk, onDone)
	} else if onDone != nil {
		onDone("", fmt.Errorf("engine not ready"))
	}
}

func (s *engineSwitcher) ExecuteSteer(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error)) {
	if e := s.get(); e != nil {
		e.ExecuteSteer(ctx, session, prompt, onChunk, onDone)
	} else if onDone != nil {
		onDone("", fmt.Errorf("engine not ready"))
	}
}

func (s *engineSwitcher) Steer(session, msg string) error {
	if e := s.get(); e != nil {
		return e.Steer(session, msg)
	}
	return fmt.Errorf("engine not ready")
}

func (s *engineSwitcher) Abort(session string) error {
	if e := s.get(); e != nil {
		return e.Abort(session)
	}
	return fmt.Errorf("engine not ready")
}

func (s *engineSwitcher) Reset(session string) error {
	if e := s.get(); e != nil {
		return e.Reset(session)
	}
	return fmt.Errorf("engine not ready")
}

func (s *engineSwitcher) Kill(session string) {
	if e := s.get(); e != nil {
		e.Kill(session)
	}
}

func (s *engineSwitcher) Close() {
	if e := s.get(); e != nil {
		e.Close()
	}
}

func (s *engineSwitcher) SessionCount() int {
	if e := s.get(); e != nil {
		return e.SessionCount()
	}
	return 0
}

func (s *engineSwitcher) SetSessionList(fn func() []string) {
	if e := s.get(); e != nil {
		e.SetSessionList(fn)
	}
}

func (s *engineSwitcher) SetSystemPromptOf(fn func(string) string) {
	if e := s.get(); e != nil {
		e.SetSystemPromptOf(fn)
	}
}
