package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelChoiceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SaveModelChoice(dir, "hy4flash-free"); err != nil {
		t.Fatal(err)
	}
	if got := LoadModelChoice(dir); got != "hy4flash-free" {
		t.Fatalf("LoadModelChoice = %q, want hy4flash-free", got)
	}
	// 持久化文件名必须是 model.choice（不带 .json，避免被 store.Load 当会话记录解析）
	if _, err := os.Stat(filepath.Join(dir, "model.choice")); err != nil {
		t.Fatalf("model.choice 应存在: %v", err)
	}
}

func TestLoadModelChoiceMissing(t *testing.T) {
	if got := LoadModelChoice(t.TempDir()); got != "" {
		t.Fatalf("空目录应返回空串, got %q", got)
	}
}

func TestModelChoiceClear(t *testing.T) {
	dir := t.TempDir()
	if err := SaveModelChoice(dir, "some-model"); err != nil {
		t.Fatal(err)
	}
	if err := SaveModelChoice(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got := LoadModelChoice(dir); got != "" {
		t.Fatalf("清空后应返回空串, got %q", got)
	}
}
