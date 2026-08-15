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

// TestCrossProcessApprove：模拟运行中进程 + 外部 CLI 进程 approve。
// 运行中进程每次 IsAuthorized 实时读文件，外部写入后应立即感知（无需重启）。
func TestCrossProcessApprove(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir) // 模拟运行中的 feisuo 进程

	// 模拟外部 CLI：直接打开同一文件 approve
	cli, _ := Open(dir)
	cli.Approve("u_ext")

	// 运行中进程立即感知（不重新 Open、不重启）
	if !s.IsAuthorized("u_ext") {
		t.Error("running process should see external approve immediately (hot reload)")
	}

	// 反向：CLI revoke 后运行中进程也立即感知
	cli.Revoke("u_ext")
	if s.IsAuthorized("u_ext") {
		t.Error("running process should see external revoke immediately (hot reload)")
	}
}
