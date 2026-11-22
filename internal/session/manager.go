package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Content string
	Sender  string
	// Depth 转发深度（0=用户消息，每经过一次 bridge 转发 +1，>MaxBridgeDepth 丢弃防环）
	Depth int
	// Origin 转发来源会话名（空=用户直接消息）
	Origin string
	// Interrupt 标记这条消息要打断当前 pi 任务（streamingBehavior=steer）。
	// 仅由直接飞书用户消息在会话 busy 时设置；bridge 转发/attach 介入不打断。
	Interrupt bool
}

type Session struct {
	Name         string
	Status       string // idle | busy | done | error | stopped
	Message      string
	Queue        chan Message
	StartedAt    time.Time
	StoppedAt    *time.Time
	Err          string
	SystemPrompt string // 会话角色/系统提示词（可空）
	WorkDir      string // 会话工作目录（绝对路径，可空=用 feisuo 启动目录）
	LastSender   string // 最后活跃的飞书用户（attach 介入时用此 ID 发送回复）
	mu           sync.Mutex
}

func NewSession(name string) *Session {
	return &Session{
		Name:      name,
		Status:    "idle",
		Queue:     make(chan Message, 100), // 增大队列容量，防止排队满丢消息
		StartedAt: time.Now(),
	}
}

type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex

	// current 记录每个 sender 当前正在对话的会话（/TO 切换后记住，普通消息发往这里）
	curMu   sync.Mutex
	current map[string]string
}

func New() *Manager {
	return &Manager{sessions: make(map[string]*Session), current: make(map[string]string)}
}

// SetCurrent 记录 sender 当前正在对话的会话（/TO <会话> 切换时调用）。
// 此后该 sender 的普通消息默认发往此会话。
func (m *Manager) SetCurrent(sender, name string) {
	m.curMu.Lock()
	defer m.curMu.Unlock()
	m.current[sender] = name
}

// Current 返回 sender 当前对话的会话名；未设置过则回退 defaultName。
func (m *Manager) Current(sender, defaultName string) string {
	m.curMu.Lock()
	defer m.curMu.Unlock()
	if name, ok := m.current[sender]; ok && name != "" {
		return name
	}
	return defaultName
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

// SetRole 设置会话系统提示词（角色）。返回是否成功（会话必须存在）。
func (m *Manager) SetRole(name, role string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	s.SystemPrompt = role
	s.mu.Unlock()
	return true
}

// Role 返回会话系统提示词（不存在返回空）。
func (m *Manager) Role(name string) string {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SystemPrompt
}

// SetWorkDir 设置会话工作目录（由调用方校验为存在的绝对路径）。返回是否成功。
func (m *Manager) SetWorkDir(name, dir string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	s.WorkDir = dir
	s.mu.Unlock()
	return true
}

// WorkDir 返回会话工作目录（不存在或未设置返回空）。
func (m *Manager) WorkDir(name string) string {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.WorkDir
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
	// 记录最后活跃的飞书用户（local 介入不更新，保持原飞书用户 ID）
	if msg.Sender != "" && msg.Sender != "local" {
		s.LastSender = msg.Sender
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

// Status 返回会话当前状态（idle | busy | done | error | stopped）。不存在返回空。
func (m *Manager) Status(name string) string {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
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

// SetLastSender 记录会话最后活跃的飞书用户（消息入队时更新）。
func (m *Manager) SetLastSender(name, sender string) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSender = sender
}

// GetLastSender 返回会话最后活跃的飞书用户；无则返回空。
func (m *Manager) GetLastSender(name string) string {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastSender
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
		line := fmt.Sprintf("  %s %-12s %s", icon, s.Name, statusDesc(s))
		if s.SystemPrompt != "" {
			line += " 🎭" + truncateRole(s.SystemPrompt, 24)
		}
		if s.WorkDir != "" {
			line += " 📁" + filepath.Base(s.WorkDir)
		}
		lines = append(lines, line)
		s.mu.Unlock()
	}
	return strings.Join(lines, "\n")
}

func truncateRole(role string, max int) string {
	r := []rune(role)
	if len(r) <= max {
		return " " + role
	}
	return " " + string(r[:max]) + "…"
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
