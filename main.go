package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"feisuo/internal/auth"
	"feisuo/internal/bridge"
	"feisuo/internal/config"
	"feisuo/internal/feishu"
	"feisuo/internal/intent"
	"feisuo/internal/pi"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/store"
	"time"
)

// runAuthCmd 处理认证子命令：approve <ID> / revoke <ID> / list-auth / list_pending
// 需要读取配置获取 data_dir（authorized.json 存于此目录）。
func runAuthCmd(cmd string, args []string) {
	cfg, err := config.Load(findConfig())
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	as, err := auth.Open(cfg.Sessions.DataDir)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	switch cmd {
	case "approve":
		if len(args) < 1 || args[0] == "" {
			fmt.Println("用法: feisuo approve <用户ID>")
			os.Exit(1)
		}
		id := args[0]
		added, removed := as.Approve(id)
		authList, _ := as.List()
		fmt.Printf("✅ 已授权: %s (新增=%v, 从待授权移除=%v)\n", id, added, removed)
		fmt.Printf("当前已授权 %d 人\n", len(authList))
	case "revoke":
		if len(args) < 1 || args[0] == "" {
			fmt.Println("用法: feisuo revoke <用户ID>")
			os.Exit(1)
		}
		id := args[0]
		if as.Revoke(id) {
			fmt.Printf("🗑 已撤销授权: %s\n", id)
		} else {
			fmt.Printf("⚠️ 用户 %s 不在授权列表\n", id)
			os.Exit(1)
		}
	case "list-auth", "list_pending":
		authList, pending := as.List()
		fmt.Println("📋 已授权用户:")
		if len(authList) == 0 {
			fmt.Println("  （无）")
		}
		for _, id := range authList {
			fmt.Printf("  ✅ %s\n", id)
		}
		fmt.Println("\n⏳ 待授权申请:")
		if len(pending) == 0 {
			fmt.Println("  （无）")
		}
		for _, id := range pending {
			fmt.Printf("  🆕 %s\n", id)
		}
	}
}

func main() {
	// 子命令模式：feisuo approve <ID> / revoke <ID> / list-auth
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "approve", "revoke", "list-auth", "list_pending":
			runAuthCmd(os.Args[1], os.Args[2:])
			return
		}
	}

	cfg, err := config.Load(findConfig())
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	s, err := store.Open(cfg.Sessions.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer s.Close()

	// 授权管理（authorized.json 存于同一数据目录，命令行 approve/revoke 即时生效）
	authStore, err := auth.Open(cfg.Sessions.DataDir)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	authList, pending := authStore.List()
	log.Printf("[auth] authorized users: %d, pending: %d", len(authList), len(pending))

	sm := session.New()

	piEng := pi.New(pi.Config{
		Binary:   cfg.Pi.Binary,
		Provider: cfg.Pi.Provider,
		Model:    cfg.Pi.Model,
		BaseURL:  cfg.Pi.BaseURL,
		APIKey:   cfg.Pi.APIKey,
		Timeout:  cfg.Pi.Timeout,
	})
	// 注入会话列表提供者：pi 启动时生成 bridge 协作协议（可转发给哪些会话）
	piEng.SessionList = func() []string { return sm.List() }
	// 注入会话角色提供者：pi 启动时注入自定义系统提示词
	piEng.SystemPromptOf = func(name string) string { return sm.Role(name) }
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

	var startLoop func(string)
	startLoop = func(name string) {
		if started[name] {
			return
		}
		started[name] = true
		go startSessionLoop(ctx, name, sm, piEng, fc, s, startLoop)
	}

	sm.EnsureDefault(cfg.Sessions.DefaultName)
	startLoop(cfg.Sessions.DefaultName)

	// 恢复持久会话：读取磁盘上已保存的会话（除显式关闭的 stopped 外全部重建）
	restored := 0
	for name, rec := range s.Load() {
		if name == cfg.Sessions.DefaultName {
			continue // 默认会话已建立
		}
		if rec.Status == "stopped" {
			log.Printf("[restore] skip stopped session %q", name)
			continue
		}
		sm.Create(name)
		if rec.SystemPrompt != "" {
			sm.SetRole(name, rec.SystemPrompt)
		}
		startLoop(name)
		restored++
		log.Printf("[restore] session %q restored (status=%s)", name, rec.Status)
	}
	if restored > 0 {
		log.Printf("[restore] %d persistent session(s) restored", restored)
	}

	err = fc.Start(ctx, func(ev feishu.Event) {
		// 授权门卫：未授权用户拒绝一切操作，仅回复授权指引
		if ev.SenderID != "" && !authStore.IsAuthorized(ev.SenderID) {
			first := authStore.RecordRequest(ev.SenderID)
			log.Printf("[auth] unauthorized sender %q (first=%v) blocked: %q", ev.SenderID, first, ev.Message.Content)
			fc.Reply(ev.SenderID, fmt.Sprintf("🔒 你还没有授权使用飞梭。\n你的用户 ID: `%s`\n请管理员执行: `feisuo approve %s`\n授权后即可使用。", ev.SenderID, ev.SenderID))
			return
		}
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
func startSessionLoop(ctx context.Context, name string, sm *session.Manager, piEng *pi.Engine, fc *feishu.Client, st *store.Store, startLoop func(string)) {
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
							// 解析 bridge 协作标记：剔除标记后发送给用户，转发内容给目标会话
							clean, reqs := bridge.Parse(final)
							sm.SetStatus(name, "done")
							sm.SetMessage(name, clean)
							fc.StreamEnd(msg.Sender, name, clean, true)

							// 转发 bridge 请求（防环：Depth > MaxDepth 丢弃；自转发忽略）
							for _, req := range reqs {
								if req.Target == name {
									log.Printf("[bridge] self-forward %q -> %q ignored", name, req.Target)
									fc.Reply(msg.Sender, fmt.Sprintf("⚠️ 会话 `%s` 不能转发给自己，请直接处理或指定其他会话", name))
									continue
								}
								if msg.Depth+1 > bridge.MaxDepth {
									log.Printf("[bridge] depth %d > max %d, drop %q -> %q (loop guard)", msg.Depth+1, bridge.MaxDepth, name, req.Target)
									fc.Reply(msg.Sender, fmt.Sprintf("⚠️ 转发链过深（>%d 层），已停止 `%s` 的转发请求", bridge.MaxDepth, req.Target))
									continue
								}
								if !sm.Exists(req.Target) {
									log.Printf("[bridge] target %q not exists, drop", req.Target)
									fc.Reply(msg.Sender, fmt.Sprintf("⚠️ 会话 `%s` 不存在，无法转发", req.Target))
									continue
								}
								log.Printf("[bridge] %s -> %s: %s", name, req.Target, req.Content)
								// 目标会话可能已停止：自动复活
								if sm.Reopen(req.Target) {
									startLoop(req.Target)
								}
								sm.Enqueue(req.Target, session.Message{
									Content: req.Content,
									Sender:  msg.Sender,
									Depth:   msg.Depth + 1,
									Origin:  name,
								})
							}
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
			if route.Role != "" {
				sm.SetRole(name, route.Role)
			}
			startLoop(name)
			if se, ok := sm.Get(name); ok {
				st.Save(se)
			}
			if route.Role != "" {
				fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`，角色: %s", name, route.Role))
			} else {
				fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`", name))
			}
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
			if se, ok := sm.Get(name); ok {
				st.Save(se) // 持久化 stopped 状态：重启后不恢复
			}
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
			fc.Reply(sender, "❌ /NEW <name> [--role \"描述\"] — 请指定会话名称")
			return
		}
		sm.Create(name)
		if cmd.Role != "" {
			sm.SetRole(name, cmd.Role)
		}
		startLoop(name)
		if se, ok := sm.Get(name); ok {
			st.Save(se)
		}
		if cmd.Role != "" {
			fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`，角色: %s", name, cmd.Role))
		} else {
			fc.Reply(sender, fmt.Sprintf("✅ 已创建会话: `%s`", name))
		}

	case "role":
		// /ROLE <会话> [--role "描述"]  → 设置/查看会话角色
		name := cmd.Arg
		if name == "" {
			fc.Reply(sender, "❌ /ROLE <会话> [--role \"描述\"] — 设置或查看会话角色")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，先 /NEW %s", name, name))
			return
		}
		if cmd.Role != "" {
			sm.SetRole(name, cmd.Role)
			// 角色变更需要重启 pi 进程才能生效（下次消息自动拉起新进程）
			piEng.Kill(name)
			if se, ok := sm.Get(name); ok {
				st.Save(se)
			}
			fc.Reply(sender, fmt.Sprintf("🎭 已更新会话 `%s` 角色，下次消息生效", name))
		} else {
			role := sm.Role(name)
			if role == "" {
				fc.Reply(sender, fmt.Sprintf("🎭 会话 `%s` 无自定义角色（默认助手）", name))
			} else {
				fc.Reply(sender, fmt.Sprintf("🎭 会话 `%s` 角色: %s", name, role))
			}
		}
		return

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
		if se, ok := sm.Get(name); ok {
			st.Save(se) // 持久化 stopped 状态：重启后不恢复
		}
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
  /NEW <名称> [--role "描述"]  创建新会话（可带角色）
  /ROLE <会话> [--role "描述"] 设置/查看会话角色
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