package session

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Content string
	Sender  string
}

type Session struct {
	Name       string
	Status     string // idle | busy | done | error | stopped
	Message    string
	Queue      chan Message
	StartedAt  time.Time
	StoppedAt  *time.Time
	Err        string
	mu         sync.Mutex
}

func NewSession(name string) *Session {
	return &Session{
		Name:      name,
		Status:    "idle",
		Queue:     make(chan Message, 10),
		StartedAt: time.Now(),
	}
}

type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

func New() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

func (m *Manager) EnsureDefault(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		m.sessions[name] = NewSession(name)
	}
}

func (m *Manager) Create(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; ok {
		return
	}
	m.sessions[name] = NewSession(name)
}

// Queue 返回当前队列（Kill 后为 nil，表示会话已停止）
func (s *Session) QueueChan() chan Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Queue
}

func (m *Manager) Exists(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.sessions[name]
	return ok
}

func (m *Manager) Enqueue(name string, msg Message) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	s.mu.Lock()
	if s.Status == "stopped" {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	select {
	case s.Queue <- msg:
		return true
	default:
		return false
	}
}

func (m *Manager) Get(name string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[name]
	return s, ok
}

func (m *Manager) SetStatus(name, status string) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.Status = status
	s.mu.Unlock()
}

func (m *Manager) SetMessage(name, msg string) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.Message = msg
	s.mu.Unlock()
}

func (m *Manager) Kill(name string) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.Status = "stopped"
	now := time.Now()
	s.StoppedAt = &now
	s.mu.Unlock()

	// 队列关闭后置 nil（nil channel 的 send 走 select default 返回 false，安全）
	s.mu.Lock()
	q := s.Queue
	s.Queue = nil
	s.mu.Unlock()
	if q != nil {
		close(q)
	}
}

// Reopen 复活一个已停止的会话：重建队列、重置状态。
// 用于 /KILL 之后再次给该会话发消息（自动重启 pi 进程 + 消费循环）。
func (m *Manager) Reopen(name string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != "stopped" {
		return false
	}
	s.Status = "idle"
	s.Err = ""
	s.Message = ""
	s.StoppedAt = nil
	s.Queue = make(chan Message, 10)
	return true
}

func (m *Manager) Rename(oldName, newName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[oldName]; ok {
		delete(m.sessions, oldName)
		s.Name = newName
		m.sessions[newName] = s
		return true
	}
	return false
}

func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.sessions))
	for name := range m.sessions {
		names = append(names, name)
	}
	return names
}

func (m *Manager) FormatStatus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.sessions) == 0 {
		return "（无会话）"
	}

	var lines []string
	for _, s := range m.sessions {
		s.mu.Lock()
		icon := statusIcon(s.Status)
		lines = append(lines, fmt.Sprintf("  %s %-12s %s", icon, s.Name, statusDesc(s)))
		s.mu.Unlock()
	}
	return strings.Join(lines, "\n")
}

func statusIcon(status string) string {
	switch status {
	case "idle":
		return "🟢"
	case "busy":
		return "🔄"
	case "done":
		return "✅"
	case "error":
		return "❌"
	case "stopped":
		return "🔴"
	default:
		return "⚪"
	}
}

func statusDesc(s *Session) string {
	switch s.Status {
	case "idle":
		return "等待中"
	case "busy":
		return fmt.Sprintf("执行中... (%s)", s.Message[:min(30, len(s.Message))])
	case "done":
		return fmt.Sprintf("已完成 (%s)", s.Message[:min(40, len(s.Message))])
	case "error":
		return "失败: " + s.Err
	case "stopped":
		return "已停止"
	default:
		return ""
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}