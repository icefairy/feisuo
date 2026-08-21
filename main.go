package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"feisuo/internal/auth"
	"feisuo/internal/bridge"
	"feisuo/internal/config"
	"feisuo/internal/feishu"
	"feisuo/internal/pi"
	"feisuo/internal/protocol"
	"feisuo/internal/session"
	"feisuo/internal/socket"
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
	// 参数扫描：-h/--help/-c，然后用剩余参数判断子命令
	var configPath string
	var remaining []string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-h", "--help":
			printUsage()
			return
		case "-c":
			if i+1 < len(os.Args) {
				configPath = os.Args[i+1]
				i++
			} else {
				log.Fatal("-c 需要指定配置文件路径")
			}
		default:
			remaining = append(remaining, os.Args[i])
		}
	}

	// 子命令模式：feisuo approve <ID> / revoke <ID> / list-auth / attach <会话> [--as <openid>]
	if len(remaining) >= 1 {
		switch remaining[0] {
		case "approve", "revoke", "list-auth", "list_pending":
			runAuthCmd(remaining[0], remaining[1:])
			return
		case "attach":
			runAttachCmd(remaining[1:])
			return
		}
	}

	if configPath == "" {
		configPath = findConfig()
	}
	cfg, err := config.Load(configPath)
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

	// 会话介入通道（Linux: Unix socket；Windows: 本地 TCP）：tmux/终端里 feisuo attach <会话> 可实时看/输入
	sockPath := socket.DefaultAddr(cfg.Sessions.DataDir)
	sockSrv := socket.NewServer(sockPath)
	if err := sockSrv.Start(); err != nil {
		log.Printf("[socket] start failed: %v", err)
	} else {
		defer sockSrv.Close()
	}

	sm := session.New()

	// attach 注入的输入（tmux 介入）：转发到对应会话。
	// startLoop 定义前先注入占位（只 Enqueue），startLoop 定义后重新注入完整版（含 Reopen+startLoop）。
	sockSrv.OnInput = func(sessionName, content, sender string) {
		if sessionName == "" {
			sessionName = cfg.Sessions.DefaultName
		}
		if !sm.Exists(sessionName) {
			log.Printf("[socket] attach input to non-existent session %q", sessionName)
			sessionName = cfg.Sessions.DefaultName
		}
		sm.Enqueue(sessionName, session.Message{Content: content, Sender: sender})
	}

	// pi 超时：nil→默认 1800，显式 0→不超时（空闲等待用户时不被 30 分钟 timer 误中断）
	piTimeout := 0
	if cfg.Pi.Timeout != nil {
		piTimeout = *cfg.Pi.Timeout
	}
	piEng := pi.New(pi.Config{
		Binary:   cfg.Pi.Binary,
		Provider: cfg.Pi.Provider,
		Model:    cfg.Pi.Model,
		BaseURL:  cfg.Pi.BaseURL,
		APIKey:   cfg.Pi.APIKey,
		Timeout:  piTimeout,
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

	// 用 started 记录已启动的会话循环
	started := make(map[string]bool)

	var startLoop func(string)
	startLoop = func(name string) {
		if started[name] {
			return
		}
		started[name] = true
		go startSessionLoop(ctx, name, sm, piEng, fc, s, startLoop, sockSrv)
	}

	// startLoop 定义完成后，注入完整版 OnInput（stopped 会话 Reopen + 重启循环）
	sockSrv.OnInput = func(sessionName, content, sender string) {
		if sessionName == "" {
			sessionName = cfg.Sessions.DefaultName
		}
		if !sm.Exists(sessionName) {
			log.Printf("[socket] attach input to non-existent session %q", sessionName)
			sessionName = cfg.Sessions.DefaultName
		}
		if sm.Reopen(sessionName) {
			startLoop(sessionName)
			log.Printf("[socket] session %q reopened via attach", sessionName)
		}
		if !sm.Enqueue(sessionName, session.Message{Content: content, Sender: sender}) {
			log.Printf("[socket] session %q queue full, message dropped", sessionName)
		}
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

	// 飞书 access token 刷新守护（单例，随主 ctx 生命周期）
	go fc.RefreshTokenLoop(ctx)
	err = fc.Start(ctx, func(ev feishu.Event) {		// 授权门卫：未授权用户拒绝一切操作，仅回复授权指引
		if ev.SenderID != "" && !authStore.IsAuthorized(ev.SenderID) {
			first := authStore.RecordRequest(ev.SenderID)
			log.Printf("[auth] unauthorized sender %q (first=%v) blocked: %q", ev.SenderID, first, ev.Message.Content)
			fc.Reply(ev.SenderID, fmt.Sprintf("🔒 你还没有授权使用飞梭。\n你的用户 ID: `%s`\n请管理员执行: `feisuo approve %s`\n授权后即可使用。", ev.SenderID, ev.SenderID))
			return
		}
		handleFeishuEvent(ctx, ev, fc, parser, sm, piEng, s, startLoop, cfg.Sessions.DefaultName)
	})
	if err != nil {
		log.Fatalf("feishu: %v", err)
	}

	fmt.Printf("飞梭 feisuo 启动\n")
	fmt.Printf("  飞书: %s | 默认会话: %s\n", cfg.Feishu.BotName, cfg.Sessions.DefaultName)
	fmt.Printf("  Pi: %s @ %s\n", cfg.Pi.Model, cfg.Pi.BaseURL)
	fmt.Printf("  命令: /TO <会话> [<消息>] | /NEW <名> | /LIST | /STATUS | /KILL <名>\n")
	fmt.Println("  Ctrl+C 退出")

	go handleLocalInput(ctx, parser, sm, piEng, fc, s, startLoop, cfg.Sessions.DefaultName)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\n正在退出...")
	cancel()
}

// startSessionLoop 每个会话的消费循环
func startSessionLoop(ctx context.Context, name string, sm *session.Manager, piEng *pi.Engine, fc *feishu.Client, st *store.Store, startLoop func(string), sockSrv *socket.Server) {
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
				if sockSrv != nil {
					sockSrv.Broadcast(fmt.Sprintf("👤 [%s] %s", name, msg.Content))
				}

				// 确定飞书发送目标：attach 介入(local)时用最后活跃的飞书用户
				feishuTarget := msg.Sender
				if msg.Sender == "local" {
					if last := sm.GetLastSender(name); last != "" {
						feishuTarget = last
					}
				}

				// 通知飞书（流式编辑模式：一条消息内滚动更新）
				fc.StreamBegin(feishuTarget, name)

				piEng.Execute(ctx, name, msg.Content,
					func(chunk string) {
						sm.SetMessage(name, chunk)
						fc.StreamChunk(feishuTarget, name, chunk)
						st.Save(se)
						if sockSrv != nil {
							sockSrv.Broadcast(fmt.Sprintf("🤖 [%s] %s", name, chunk))
						}
					},
					func(final string, err error) {
						if err != nil {
							sm.SetStatus(name, "error")
							se.Err = err.Error()
							sm.SetMessage(name, err.Error())
							fc.StreamEnd(feishuTarget, name, "失败: "+err.Error(), false)
							if sockSrv != nil {
								sockSrv.Broadcast(fmt.Sprintf("❌ [%s] 失败: %s", name, err.Error()))
							}
						} else {
							// 解析 bridge 协作标记：剔除标记后发送给用户，转发内容给目标会话
							clean, reqs := bridge.Parse(final)
							sm.SetStatus(name, "done")
							sm.SetMessage(name, clean)
							fc.StreamEnd(feishuTarget, name, clean, true)
							if sockSrv != nil {
								sockSrv.Broadcast(fmt.Sprintf("✅ [%s] %s", name, clean))
							}

							// 转发 bridge 请求（防环：Depth > MaxDepth 丢弃；自转发忽略）
							for _, req := range reqs {
								if req.Target == name {
									log.Printf("[bridge] self-forward %q -> %q ignored", name, req.Target)
									fc.Reply(feishuTarget, fmt.Sprintf("⚠️ 会话 `%s` 不能转发给自己，请直接处理或指定其他会话", name))
									continue
								}
								if msg.Depth+1 > bridge.MaxDepth {
									log.Printf("[bridge] depth %d > max %d, drop %q -> %q (loop guard)", msg.Depth+1, bridge.MaxDepth, name, req.Target)
									fc.Reply(feishuTarget, fmt.Sprintf("⚠️ 转发链过深（>%d 层），已停止 `%s` 的转发请求", bridge.MaxDepth, req.Target))
									continue
								}
								if !sm.Exists(req.Target) {
									log.Printf("[bridge] target %q not exists, drop", req.Target)
									fc.Reply(feishuTarget, fmt.Sprintf("⚠️ 会话 `%s` 不存在，无法转发", req.Target))
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
	parser *protocol.Parser, sm *session.Manager, piEng *pi.Engine, st *store.Store, startLoop func(string), defaultName string) {

	msg := ev.Message
	sender := ev.SenderID

	cmd := parser.Parse(msg.Content)

	if cmd.Type == protocol.CmdCommand {
		handleCommand(ctx, cmd, fc, sm, sender, st, startLoop, piEng, defaultName)
		return
	}

	// 普通消息：发往当前对话的会话（/TO 切换后记住；未切换时默认会话）
	target := sm.Current(sender, defaultName)
	if target != defaultName && !sm.Exists(target) {
		log.Printf("[dispatch] current session %q not exists, fallback to %q", target, defaultName)
		target = defaultName
	}
	// stopped 会话自动复活（重建队列 + 重启循环）
	if sm.Exists(target) && sm.Reopen(target) {
		startLoop(target)
		log.Printf("[dispatch] session %q reopened", target)
	}
	if !sm.Enqueue(target, session.Message{Content: msg.Content, Sender: sender}) {
		fc.Reply(sender, fmt.Sprintf("⚠️ 会话 `%s` 队列已满，消息被丢弃", target))
	}
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
		// parser 已拆分好：cmd.Session=会话名, cmd.Arg=消息（可能为空，仅切换）
		name := cmd.Session
		if name == "" {
			fc.Reply(sender, "❌ /TO <会话> [<消息>] — 请指定会话名")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，先 /NEW %s", name, name))
			return
		}
		// 切换当前会话：此 sender 后续普通消息默认发往这里
		sm.SetCurrent(sender, name)
		// stopped 会话自动复活
		if sm.Reopen(name) {
			startLoop(name)
			log.Printf("[session/%s] reopened via /TO", name)
		}
		if cmd.Arg != "" {
			sm.Enqueue(name, session.Message{
				Content: cmd.Arg,
				Sender:  sender,
			})
		} else {
			fc.Reply(sender, fmt.Sprintf("🔀 已切换到会话 `%s`，后续消息将发送到这里（/TO %s <消息> 可带消息发送）", name, name))
		}

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

	case "reset":
		// /RESET <会话> → 透传 pi 原生 new_session：清空该会话的对话上下文（进程不重启）
		name := cmd.Arg
		if name == "" {
			fc.Reply(sender, "❌ /RESET <会话> — 请指定会话名")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，用 /LIST 查看", name))
			return
		}
		if err := piEng.Reset(name); err != nil {
			fc.Reply(sender, fmt.Sprintf("❌ 重置会话 `%s` 失败: %v", name, err))
			return
		}
		// 重置后若会话曾在处理中，busy 状态清掉
		if se, ok := sm.Get(name); ok {
			se.Err = ""
			sm.SetStatus(name, "idle")
			st.Save(se)
		}
		fc.Reply(sender, fmt.Sprintf("🔄 已重置会话 `%s`（对话上下文已清空，历史无法恢复）", name))

	case "compact":
		// /COMPACT <会话> → 透传 pi 原生 /compact：压缩当前会话上下文（保留关键信息，降 token 占用）
		name := cmd.Arg
		if name == "" {
			fc.Reply(sender, "❌ /COMPACT <会话> — 请指定会话名")
			return
		}
		if !sm.Exists(name) {
			fc.Reply(sender, fmt.Sprintf("❌ 会话 `%s` 不存在，用 /LIST 查看", name))
			return
		}
		fc.Reply(sender, fmt.Sprintf("🗜 正在压缩会话 `%s` 的上下文…", name))
		piEng.Execute(ctx, name, "/compact",
			func(chunk string) {
				// 流式进度：实时推送到飞书卡片（与正常对话同路径）
				fc.StreamChunk(sender, name, chunk)
			},
			func(final string, err error) {
				if err != nil {
					fc.Reply(sender, fmt.Sprintf("❌ 压缩会话 `%s` 失败: %v", name, err))
					return
				}
				fc.Reply(sender, fmt.Sprintf("✅ 会话 `%s` 上下文已压缩\n\n%s", name, final))
			})
		return

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
  /TO <会话> [<消息>]  切换到指定会话（可带消息直接发送）
  /NEW <名称> [--role "描述"]  创建新会话（可带角色）
  /ROLE <会话> [--role "描述"] 设置/查看会话角色
  /LIST               列出所有会话
  /STATUS             查看会话状态
  /KILL <会话>        终止会话（上下文保留，复活后恢复）
  /RESET <会话>       重置会话（透传 pi /new：清空对话上下文）
  /COMPACT <会话>     压缩会话上下文（透传 pi /compact：保留关键信息降 token）
  /NAME <旧> <新>     重命名会话
  普通消息            发给当前会话（/TO 切换后发往新会话）`)

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
			// 用 Scanner 读整行（fmt.Scanln 只读单 token，/RESET main 会被拆坏）
			fmt.Print("feisuo> ")
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			if !sc.Scan() {
				return
			}
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if line == "quit" || line == "exit" {
				return
			}
			cmd := parser.Parse(line)
			if cmd.Type == protocol.CmdCommand {
				handleCommand(ctx, cmd, fc, sm, "local", st, startLoop, piEng, defaultName)
			} else {
				// 本地输入同样发往当前对话的会话（/TO 切换后记住）
				target := sm.Current("local", defaultName)
				if target != defaultName && !sm.Exists(target) {
					target = defaultName
				}
				if sm.Exists(target) && sm.Reopen(target) {
					startLoop(target)
				}
				sm.Enqueue(target, session.Message{Content: line, Sender: "local"})
			}
		}
	}
}

// runAttachCmd 介入指定会话：实时显示收发 + stdin 输入。
// 用法: feisuo attach <会话> [--as <飞书用户ID>]
//  - 不传 --as：本地介入（Sender=local，回复只在本地显示）
//  - 传 --as <ID>：双通道（回复同时发飞书该用户 + 本地显示）
func runAttachCmd(args []string) {
	var sessionName, asOpenID string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--as":
			if i+1 < len(args) {
				asOpenID = args[i+1]
				i++
			}
		default:
			if sessionName == "" {
				sessionName = args[i]
			}
		}
	}
	if sessionName == "" {
		fmt.Println("用法: feisuo attach <会话名> [--as <飞书用户ID>]")
		os.Exit(1)
	}

	cfg, err := config.Load(findConfig())
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	sockPath := socket.DefaultAddr(cfg.Sessions.DataDir)
	cli, err := socket.Connect(sockPath)
	if err != nil {
		log.Fatalf("attach: %v（feisuo 未运行？socket=%s）", err, sockPath)
	}
	defer cli.Close()

	sender := "local"
	if asOpenID != "" {
		sender = asOpenID
	}
	fmt.Printf("📡 已介入会话 [%s]，输入消息回车发送（Ctrl+C 退出）\n", sessionName)
	if sender == "local" {
		fmt.Println("   模式: 仅本地显示（不带 --as，回复不发飞书）")
	} else {
		fmt.Println("   模式: 双通道（回复同时发飞书 + 本地显示）")
	}

	// 异步收广播
	go func() {
		for {
			line, ok := cli.NextEvent()
			if !ok {
				fmt.Println("\n（连接已断开）")
				os.Exit(0)
			}
			if line != "" {
				fmt.Println(line)
			}
		}
	}()

	// stdin 输入
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		if text == "exit" || text == "quit" {
			break
		}
		if err := cli.SendInput(sessionName, text, sender); err != nil {
			fmt.Printf("发送失败: %v\n", err)
			break
		}
	}
}

func findConfig() string {
	// 按优先级搜索配置文件：当前目录 → 系统路径 → 用户目录
	paths := []string{
		"feisuo.yaml",
		"/etc/feisuo/feisuo.yaml",
		filepath.Join(mustHomeDir(), ".feisuo", "feisuo.yaml"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "feisuo.yaml" // 找不到时返回默认（会报错）
}

func mustHomeDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return "."
	}
	return home
}

func printUsage() {
	fmt.Println(`飞梭 (feisuo) — 飞书 AI Agent 网关

用法:
  feisuo [-c <配置文件>]
  feisuo -h | --help
  feisuo <子命令> [参数]

选项:
  -c <文件>    指定配置文件路径（默认：当前目录 feisuo.yaml → ~/.feisuo/feisuo.yaml）
  -h, --help   显示帮助信息

子命令:
  approve <用户ID>       批准飞书用户授权
  revoke <用户ID>        撤销飞书用户授权
  list-auth              列出已授权用户
  list_pending           列出待授权用户
  attach <会话> [--as <openid>]  实时介入会话（本地终端）

配置文件搜索顺序:
  1. -c 参数指定的路径
  2. 当前目录 ./feisuo.yaml
  3. ~/.feisuo/feisuo.yaml

示例:
  feisuo                                    # 使用默认配置启动
  feisuo -c /etc/feisuo/feisuo.yaml         # 指定配置文件
  feisuo approve ou_xxxxx                    # 批准用户授权
  feisuo attach main                        # 介入 main 会话`)
}