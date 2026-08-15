package store

import (
	"os"
	"path/filepath"
	"testing"

	"feisuo/internal/session"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 保存一个活跃会话 + 一个 stopped 会话
	se1 := session.NewSession("ice")
	s.Save(se1)

	se2 := session.NewSession("work")
	se2.Status = "stopped"
	s.Save(se2)

	records := s.Load()
	if len(records) != 2 {
		t.Fatalf("Load() = %d records, want 2", len(records))
	}
	if r, ok := records["ice"]; !ok || r.Status != "idle" {
		t.Errorf("ice record missing or wrong status: %+v", r)
	}
	if r, ok := records["work"]; !ok || r.Status != "stopped" {
		t.Errorf("work record missing or wrong status: %+v", r)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "empty")
	s, _ := Open(dir)
	records := s.Load()
	if len(records) != 0 {
		t.Errorf("Load() on empty dir = %d records, want 0", len(records))
	}
	// 目录自动创建
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}
