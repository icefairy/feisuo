// Package auth 实现飞书用户认证：白名单授权 + 待授权申请记录。
// 授权列表存 <data_dir>/authorized.json，独立文件即时刷新（不依赖 yaml 重载）。
package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store 授权管理
type Store struct {
	path string
	mu   sync.Mutex
	data *data
}

type data struct {
	Authorized []string `json:"authorized"` // 已授权用户 ID
	Pending    []string `json:"pending"`    // 待授权申请者 ID（未授权用户发过消息）
	UpdatedAt  string   `json:"updated_at"`
}

// Open 打开（或创建）授权文件。dir 为数据目录（与会话元数据同级）。
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, "authorized.json")
	s := &Store{path: path, data: &data{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = &data{}
			return s.saveLocked()
		}
		return err
	}
	var d data
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	if d.Authorized == nil {
		d.Authorized = []string{}
	}
	if d.Pending == nil {
		d.Pending = []string{}
	}
	s.data = &d
	return nil
}

func (s *Store) saveLocked() error {
	s.data.UpdatedAt = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	os.MkdirAll(dir, 0755)
	// 原子写：先写临时文件再 rename，避免跨进程读写撕裂
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// reloadLocked 重新从磁盘加载授权文件（跨进程 approve/revoke 后运行中进程立即感知）。
// 调用方必须已持有 s.mu。文件不存在则视为空。
func (s *Store) reloadLocked() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = &data{}
		}
		return
	}
	var d data
	if err := json.Unmarshal(raw, &d); err != nil {
		return // 解析失败（如跨进程写中途）保留现有内存数据
	}
	if d.Authorized == nil {
		d.Authorized = []string{}
	}
	if d.Pending == nil {
		d.Pending = []string{}
	}
	s.data = &d
}

// IsAuthorized 判断用户是否已授权。每次实时读文件，CLI approve 后立即生效。
func (s *Store) IsAuthorized(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return contains(s.data.Authorized, id)
}

// RecordRequest 记录一次未授权访问（去重追加到 pending）。
// 返回 true 表示本次是首次记录（新申请者）。
func (s *Store) RecordRequest(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	if contains(s.data.Pending, id) {
		return false
	}
	s.data.Pending = append(s.data.Pending, id)
	s.saveLocked()
	return true
}

// Approve 授权用户 ID。返回 (是否新增授权, 是否从 pending 移除)。
func (s *Store) Approve(id string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	added := false
	if !contains(s.data.Authorized, id) {
		s.data.Authorized = append(s.data.Authorized, id)
		added = true
	}
	removed := false
	for i, p := range s.data.Pending {
		if p == id {
			s.data.Pending = append(s.data.Pending[:i], s.data.Pending[i+1:]...)
			removed = true
			break
		}
	}
	s.saveLocked()
	return added, removed
}

// Revoke 撤销授权。返回是否确实撤销。
func (s *Store) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	revoked := false
	for i, a := range s.data.Authorized {
		if a == id {
			s.data.Authorized = append(s.data.Authorized[:i], s.data.Authorized[i+1:]...)
			revoked = true
			break
		}
	}
	if revoked {
		s.saveLocked()
	}
	return revoked
}

// List 返回授权列表与待授权列表（快照，实时读文件）。
func (s *Store) List() (authorized, pending []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	authorized = append([]string{}, s.data.Authorized...)
	pending = append([]string{}, s.data.Pending...)
	return
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
