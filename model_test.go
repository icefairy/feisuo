package main

import (
	"context"
	"testing"

	"feisuo/internal/config"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/store"
)

func TestCurrentEngineName(t *testing.T) {
	cases := []struct {
		name, choice, yaml, want string
	}{
		{name: "choice 优先", choice: "codebuddy", yaml: "pi", want: "codebuddy"},
		{name: "无 choice 用 yaml", choice: "", yaml: "pi", want: "pi"},
		{name: "非法 choice 归 pi", choice: "weird", yaml: "codebuddy", want: "pi"},
		{name: "cb 归一", choice: "cb", yaml: "pi", want: "cb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			if tc.choice != "" {
				if err := config.SaveEngineChoice(d, tc.choice); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Config{}
			cfg.Sessions.DataDir = d
			cfg.Engine = tc.yaml
			if got := currentEngineName(cfg); got != tc.want {
				t.Fatalf("currentEngineName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentModel(t *testing.T) {
	d := t.TempDir()
	if err := config.SaveModelChoice(d, "hy4flash-free"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Sessions.DataDir = d
	cfg.CodeBuddy.Model = "cb-yaml"
	cfg.Pi.Model = "pi-yaml"
	if got := currentModel(cfg, "codebuddy"); got != "hy4flash-free" {
		t.Fatalf("choice 应优先, got %q", got)
	}
	if err := config.SaveModelChoice(d, ""); err != nil {
		t.Fatal(err)
	}
	if got := currentModel(cfg, "codebuddy"); got != "cb-yaml" {
		t.Fatalf("无 choice 应回退 codebuddy yaml, got %q", got)
	}
	if got := currentModel(cfg, "pi"); got != "pi-yaml" {
		t.Fatalf("无 choice 应回退 pi yaml, got %q", got)
	}
}

func TestModelCommandSetsPersistsAndSwaps(t *testing.T) {
	dir := t.TempDir()
	old := &fakeEngine{name: "old"}
	sw := newEngineSwitcher(old.asEngine())
	sm := session.New()
	srv, fc := mockFeishu()
	defer srv.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cmd := protocol.New().Parse("/MODEL   hy4flash-free  ")
	if cmd.Cmd != "model" {
		t.Fatalf("解析失败: %+v", cmd)
	}
	cfg := config.Config{}
	cfg.Sessions.DataDir = dir
	cfg.Engine = "pi"
	handleCommand(context.Background(), cmd, fc, sm, "ou_u", st, func(string) {}, sw, cfg, "main")

	if !old.wasClosed() {
		t.Fatal("/MODEL 应重建引擎（关闭旧引擎）")
	}
	if got := config.LoadModelChoice(dir); got != "hy4flash-free" {
		t.Fatalf("模型应持久化为 hy4flash-free, got %q", got)
	}
	// 查看分支不应清空持久化
	view := protocol.New().Parse("/MODEL")
	handleCommand(context.Background(), view, fc, sm, "ou_u", st, func(string) {}, sw, cfg, "main")
	if got := config.LoadModelChoice(dir); got != "hy4flash-free" {
		t.Fatalf("查看命令不应清空模型, got %q", got)
	}
}

func TestModelCommandReset(t *testing.T) {
	dir := t.TempDir()
	sw := newEngineSwitcher((&fakeEngine{}).asEngine())
	srv, fc := mockFeishu()
	defer srv.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveModelChoice(dir, "some-model"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Sessions.DataDir = dir
	cmd := protocol.New().Parse("/MODEL reset")
	handleCommand(context.Background(), cmd, fc, session.New(), "ou_u", st, func(string) {}, sw, cfg, "main")
	if got := config.LoadModelChoice(dir); got != "" {
		t.Fatalf("reset 应清空模型, got %q", got)
	}
}
