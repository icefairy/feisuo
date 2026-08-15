package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"feisuo/internal/session"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	os.MkdirAll(dir, 0755)
	return &Store{dir: dir}, nil
}

func (s *Store) Save(se *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(map[string]interface{}{
		"name":       se.Name,
		"status":     se.Status,
		"message":    se.Message,
		"system_prompt": se.SystemPrompt,
		"started_at": se.StartedAt.Format("2006-01-02 15:04:05"),
		"stopped_at": se.StoppedAt,
		"error":      se.Err,
		"updated_at": time.Now().Format("2006-01-02T15:04:05"),
	}, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(s.dir, se.Name+".json")
	os.WriteFile(path, data, 0644)
}

func (s *Store) Close() {}

// Record 一条已持久化的会话元数据
type Record struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Message   string  `json:"message"`
	SystemPrompt string `json:"system_prompt"`
	StartedAt string  `json:"started_at"`
	StoppedAt *string `json:"stopped_at"`
	Error     string  `json:"error"`
	UpdatedAt string  `json:"updated_at"`
}

// Load 读取目录下所有已持久化的会话记录（用于重启后恢复）。
// 返回 map[name]Record。
func (s *Store) Load() map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]Record{}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.Name != "" {
			out[rec.Name] = rec
		}
	}
	return out
}