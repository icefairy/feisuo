package store

import (
	"encoding/json"
	"os"
	"path/filepath"
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