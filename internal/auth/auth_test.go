package auth

import (
	"path/filepath"
	"testing"
)

func TestOpenCreateAndApprove(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 新用户未授权
	if s.IsAuthorized("u1") {
		t.Error("u1 should not be authorized initially")
	}
	// 记录申请
	if !s.RecordRequest("u1") {
		t.Error("first record should return true")
	}
	if s.RecordRequest("u1") {
		t.Error("duplicate record should return false")
	}
	// 批准
	added, removed := s.Approve("u1")
	if !added || !removed {
		t.Errorf("approve: added=%v removed=%v, want true true", added, removed)
	}
	if !s.IsAuthorized("u1") {
		t.Error("u1 should be authorized after approve")
	}
	// 重复批准
	added2, _ := s.Approve("u1")
	if added2 {
		t.Error("duplicate approve should return added=false")
	}
	// 撤销
	if !s.Revoke("u1") {
		t.Error("revoke should succeed")
	}
	if s.IsAuthorized("u1") {
		t.Error("u1 should not be authorized after revoke")
	}
}

func TestPersistAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Approve("u99")

	// 重新打开（模拟重启）
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.IsAuthorized("u99") {
		t.Error("authorization should persist after reopen")
	}
}

func TestFileCreatedInDataDir(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.RecordRequest("pending1")
	if _, err := filepath.Glob(filepath.Join(dir, "authorized.json")); err != nil {
		t.Fatal(err)
	}
}
