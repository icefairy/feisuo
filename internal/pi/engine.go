package pi

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

type Config struct {
	Binary   string
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Timeout  int
}

type Message struct {
	Content string
	Sender  string
}

// Engine 封装 pi CLI 调用
type Engine struct {
	cfg Config
}

func New(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// Execute 执行 pi 命令，流式返回每一行
func (e *Engine) Execute(ctx context.Context, session, prompt string,
	onChunk func(string), onDone func(string, error)) {

	timeout := e.cfg.Timeout
	if timeout == 0 {
		timeout = 300
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	args := []string{
		"-p", prompt,
		"--mode", "text",
		"--no-session",
		"--provider", e.cfg.Provider,
		"--model", e.cfg.Model,
	}
	if e.cfg.APIKey != "" {
		args = append(args, "--api-key", e.cfg.APIKey)
	}

	binary := e.cfg.Binary
	if binary == "" {
		binary = "pi"
	}

	cmd := exec.CommandContext(ctx, binary, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		onDone("", fmt.Errorf("stdout pipe: %w", err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		onDone("", fmt.Errorf("stderr pipe: %w", err))
		return
	}

	if err := cmd.Start(); err != nil {
		onDone("", fmt.Errorf("start: %w", err))
		return
	}
	log.Printf("[pi/%s] pid=%d", session, cmd.Process.Pid)

	// stderr goroutine
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				log.Printf("[pi/%s] stderr: %s", session, line)
			}
		}
	}()

	// stdout 流式
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var full strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if onChunk != nil {
			onChunk(line)
		}
		full.WriteString(line)
		full.WriteString("\n")
	}

	if err := cmd.Wait(); err != nil {
		onDone(strings.TrimSpace(full.String()), fmt.Errorf("wait: %w", err))
		return
	}

	final := strings.TrimSpace(full.String())
	if final == "" {
		final = "（Pi 返回为空）"
	}
	log.Printf("[pi/%s] done len=%d", session, len(final))
	onDone(final, nil)
}