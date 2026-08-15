package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"feisuo/internal/config"
	"feisuo/internal/feishu"
	"feisuo/internal/intent"
	"feisuo/internal/pi"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/store"
	"time"
)

func main() {
	cfg, err := config.Load(findConfig())
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	s, err := store.Open(cfg.Sessions.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer s.Close()

	sm := session.New()

	piEng := pi.New(pi.Config{
		Binary:   cfg.Pi.Binary,
		Provider: cfg.Pi.Provider,
		Model:    cfg.Pi.Model,
		BaseURL:  cfg.Pi.BaseURL,
		APIKey:   cfg.Pi.APIKey,
		Timeout:  cfg.Pi.Timeout,
	})
	defer piEng.Close()

	fc := feishu.New(feishu.Config{
		AppID:     cfg.Feishu.AppID,
		AppSecret: cfg.Feishu.AppSecret,
		BotName:   cfg.Feishu.BotName,
		WsURL:     cfg.Feishu.WsURL,
	})

	parser := protocol.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 意图路由（普通消息自动解析发往哪个会话；/ 开头命令不受影响）
	var router *intent.Router
	if cfg.Intent.Enabled {
		router = intent.New(cfg.Intent.Enabled, cfg.Intent.Model, cfg.Intent.BaseURL, cfg.Intent.APIKey, cfg.Intent.Timeout)
		log.Printf("意图路由已启用: model=%s", cfg.Intent.Model)
	} else {
		log.Printf("意图路由未启用（普通消息直接发默认会话）")
	}

	// 用 started 记录已启动的会话循环
	started := make(map[string]bool)

	startLoop := func(name string) {
		if started[name] {
			return
		}
		started[name] = true
		go startSessionLoop(ctx, name, sm, piEng, fc, s)
	}

	sm.EnsureDefault(cfg.Sessions.DefaultName)
	startLoop(cfg.Sessions.DefaultName)

	err = fc.Start(ctx, func(ev feishu.Event) {
		handleFeishuEvent(ctx, ev, fc, parser, sm, piEng, s, startLoop, router, cfg.Sessions.DefaultName)
	})
	if err != nil {
		log.Fatalf("feishu: %v", err)
	}

	fmt.Printf("飞梭 feisuo 启动\n")
	fmt.Printf("  飞书: %s | 默认会话: %s\n", cfg.Feishu.BotName, cfg.Sessions.DefaultName)
	fmt.Printf("  Pi: %s @ %s\n", cfg.Pi.Model, cfg.Pi.BaseURL)
	fmt.Printf("  命令: /TO <会话> <消息> | /NEW <名> | /LIST | /STATUS | /KILL <名>\n")
	fmt.Println("  Ctrl+C 退出")

	go handleLocalInput(ctx, parser, sm, piEng, fc, s, startLoop, cfg.Sessions.DefaultName)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\n正在退出...")
	cancel()
}

// startSessionLoop 每个会话的消费循环
func startSessionLoop(ctx context.Context, name string, sm *session.Manager, piEng *pi.Engine, fc *feishu.Client, st *store.Store) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			se, ok := sm.Get(name)
			if !ok {
				return
			}
			// 会话已停止（Kill 后队列为 nil）：退场，等待 Reopen 后由外部重启循环
			queue := se.QueueChan()
			if queue == nil {
				log.Printf("[session/%s] stopped, loop exiting", name)
				return
			}
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-queue:
				if !ok {
					return
				}
				sm.SetStatus(name, "busy")
				sm.SetMessage(name, msg.Content)
				st.Save(se)
				log.Printf("[session/%s] processing: %s", name, msg.Content)

				// 通知飞书（流式编辑模式：一条消息内滚动更新）
				fc.StreamBegin(msg.Sender, name)

				piEng.Execute(ctx, name, msg.Content,
					func(chunk string) {
						sm.SetMessage(name, chunk)
						fc.StreamChunk(msg.Sender, name, chunk)
						st.Save(se)
					},
					func(final string, err error) {
						if err != nil {
							sm.SetStatus(name, "error")
							se.Err = err.Error()
							sm.SetMessage(name, err.Error())
							fc.StreamEnd(msg.Sender, name, "失败: "+err.Error(), false)
						} else {
							sm.SetStatus(name, "done")
							sm.SetMessage(name, final)
							fc.StreamEnd(msg.Sender, name, final, true)
						}
						st.Save(se)
						time.Sleep(2 * time.Second)
						sm.SetStatus(name, "idle")
					})
			}
		}
	}
}

func handleFeishuEvent(ctx context.Context, ev feishu.Event, fc *feishu.Client,
	parser *protocol.Parser, sm *session.Manager, piEng *pi.Engine, st *store.Store, startLoop func(string), router *intent.Router, defaultName string) {

	msg := ev.Message
	sender := ev.SenderID

	cmd := parser.Parse(msg.Content)

	if cmd.Type == protocol.CmdCommand {
		handleCommand(ctx, cmd, fc, sm, sender, st, startLoop, piEng, defaultName)
		return
	}

	// 非命令消息：先尝试意图路由（自动解析发往哪个会话 + 会话管理指令）
	if router != nil && router.Enabled() {
		route := router.Resolve(sm.List(), defaultName, msg.Content)
		switch route.Action {
		case intent.ActionList:
			fc.Reply(sender, "📋 当前会话:\n"+sm.FormatStatus())
			return
		case intent.ActionNew:
			name := route.Target
			if sm.Exists(name) {
				fc.Reply(sender, fmt.Sprintf("⚠️ 会话 `%s` 已存在，用 /LIST 查看", name))
				return
			}
			sm.Create(name)
			startLoop(name)
			if se, ok := sm.Get(name); ok {
				st.Save(se)
			}
			fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`", name))
			return
		case intent.ActionKill:
			name := route.Target
			if !sm.Exists(name) {
				fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，用 /LIST 查看", name))
				return
			}
			if name == defaultName {
				fc.Reply(sender, "❌ 不能关闭默认会话 `main`")
				return
			}
			piEng.Kill(name)
			sm.Kill(name)
			fc.Reply(sender, fmt.Sprintf("🗑 已关闭会话: `%s`（pi 进程已终止）", name))
			return
		}

		// send：目标会话可能已停止（Kill 过），自动复活
		target, content := route.Target, route.Content
		if target != defaultName && !sm.Exists(target) {
			log.Printf("[intent] target %q not exists, fallback to %q", target, defaultName)
			fc.Reply(sender, fmt.Sprintf("🤖 你想发给会话 `%s`，但它不存在。可 /NEW %s 创建，或 /TO %s <消息> 精确发送。已回退到默认会话。", target, target, target))
			target = defaultName
			content = msg.Content
		}
		// stopped 会话自动复活（重建队列 + 重启循环）
		if sm.Exists(target) && sm.Reopen(target) {
			startLoop(target)
			log.Printf("[intent] session %q reopened", target)
		}
		sm.Enqueue(target, session.Message{Content: content, Sender: sender})
		return
	}

	target := "main"
	// stopped 会话自动复活（重建队列 + 重启循环）
	if sm.Exists(target) && sm.Reopen(target) {
		startLoop(target)
		log.Printf("[intent] session %q reopened", target)
	}
	sm.Enqueue(target, session.Message{Content: msg.Content, Sender: sender})
}

func handleCommand(ctx context.Context, cmd protocol.Command, fc *feishu.Client,
	sm *session.Manager, sender string, st *store.Store, startLoop func(string), piEng *pi.Engine, defaultName string) {

	switch cmd.Cmd {
	case "new":
		name := cmd.Arg
		if name == "" {
			fc.Reply(sender, "❌ /NEW <name> — 请指定会话名称")
			return
		}
		sm.Create(name)
		startLoop(name)
		if se, ok := sm.Get(name); ok {
			st.Save(se)
		}
		fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`", name))

	case "list", "ls", "status":
		fc.Reply(sender, sm.FormatStatus())

	case "to":
		// parser 已拆分好：cmd.Session=会话名, cmd.Arg=消息（不要重复 SplitTwo！）
		name := cmd.Session
		if name == "" {
			fc.Reply(sender, "❌ /TO <会话> <消息> — 请指定会话名和消息")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，先 /NEW %s", name, name))
			return
		}
		// stopped 会话自动复活
		if sm.Reopen(name) {
			startLoop(name)
			log.Printf("[session/%s] reopened via /TO", name)
		}
		sm.Enqueue(name, session.Message{
			Content: cmd.Arg,
			Sender:  sender,
		})

	case "kill":
		name := cmd.Arg
		if name == "" {
			fc.Reply(sender, "❌ /KILL <会话> — 请指定会话名")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，用 /LIST 查看", name))
			return
		}
		if name == defaultName {
			fc.Reply(sender, "❌ 不能关闭默认会话 `main`")
			return
		}
		piEng.Kill(name)
		sm.Kill(name)
		fc.Reply(sender, fmt.Sprintf("🗑 已关闭会话: `%s`（pi 进程已终止）", name))

	case "name", "rename":
		parts := protocol.SplitTwo(cmd.Arg)
		if parts[0] == "" || parts[1] == "" {
			fc.Reply(sender, "❌ /NAME <旧名> <新名>")
			return
		}
		sm.Rename(parts[0], parts[1])
		fc.Reply(sender, fmt.Sprintf("✏ 会话 `%s` → `%s`", parts[0], parts[1]))

	case "help":
		fc.Reply(sender, `🤖 飞梭命令:
  /TO <会话> <消息>    发给指定会话
  /NEW <名称>         创建新会话
  /LIST               列出所有会话
  /STATUS             查看会话状态
  /KILL <会话>        终止会话
  /NAME <旧> <新>     重命名会话
  普通消息            发给默认会话(main)`)

	default:
		fc.Reply(sender, fmt.Sprintf("❌ 未知命令: %s，用 /HELP 查看", cmd.Cmd))
	}
}

func handleLocalInput(ctx context.Context, parser *protocol.Parser, sm *session.Manager,
	piEng *pi.Engine, fc *feishu.Client, st *store.Store, startLoop func(string), defaultName string) {

	for {
		select {
		case <-ctx.Done():
			return
		default:
			var line string
			fmt.Print("feisuo> ")
			if _, err := fmt.Scanln(&line); err != nil {
				return
			}
			if line == "quit" || line == "exit" {
				return
			}
			cmd := parser.Parse(line)
			if cmd.Type == protocol.CmdCommand {
				handleCommand(ctx, cmd, fc, sm, "local", st, startLoop, piEng, defaultName)
			} else {
				sm.Enqueue("main", session.Message{Content: line, Sender: "local"})
			}
		}
	}
}

func findConfig() string {
	if _, err := os.Stat("feisuo.yaml"); err == nil {
		return "feisuo.yaml"
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".feisuo", "feisuo.yaml")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return "feisuo.yaml"
}