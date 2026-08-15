// Package intent 提供基于小模型的意图路由：把用户的模糊输入解析成
// 目标会话 + 清洗后的消息，用于 /TO 精确匹配之外的智能分发。
package intent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Action 意图动作类型
type Action string

const (
	ActionSend Action = "send" // 普通消息：路由到 target 会话
	ActionList Action = "list" // 获取会话列表
	ActionNew  Action = "new"  // 新建会话 target
	ActionKill Action = "kill" // 关闭会话 target
)

// Route 意图路由结果
type Route struct {
	Action  Action // send | list | new | kill
	Target  string // 目标会话名（空 = 默认会话）
	Content string // 清洗后的消息（空 = 原文）
	Role    string // new 时会话角色（可空）
}

// Router 用小模型解析用户意图，把消息路由到对应会话
type Router struct {
	enabled bool
	model   string
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

// New 创建 Router
func New(enabled bool, model, baseURL, apiKey string, timeoutSec int) *Router {
	if timeoutSec <= 0 {
		timeoutSec = 8
	}
	return &Router{
		enabled: enabled,
		model:   model,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		timeout: time.Duration(timeoutSec) * time.Second,
		client:  &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}
}

// Enabled 返回是否启用意图路由
func (r *Router) Enabled() bool {
	return r.enabled
}

// Resolve 解析消息归属：sessions 为当前会话名列表，defaultName 为默认会话。
// 返回 Route（动作 + 目标 + 内容）。任何失败（模型不可用/超时/解析错误）都回退到
// ActionSend 到默认会话、保留原文。
// 路由优先级：显式前缀（main: / @main / <会话>: / @<会话>）> LLM 意图 > 默认会话。
func (r *Router) Resolve(sessions []string, defaultName, userText string) Route {
	// 1. 显式前缀强制路由（不走模型，零延迟零成本）
	if t, c, ok := matchExplicitPrefix(sessions, defaultName, userText); ok {
		log.Printf("[intent] explicit prefix -> target=%q content=%q", t, c)
		return Route{Action: ActionSend, Target: t, Content: c}
	}
	// 2. LLM 意图解析
	if !r.enabled || r.model == "" || r.apiKey == "" {
		return Route{Action: ActionSend, Target: defaultName, Content: userText}
	}

	prompt := buildPrompt(sessions, defaultName, userText)
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	resp, err := r.callLLM(ctx, prompt)
	if err != nil {
		log.Printf("[intent] resolve failed, fallback to %q: %v", defaultName, err)
		return Route{Action: ActionSend, Target: defaultName, Content: userText}
	}

	parsed := parseJSON(resp)
	// 无有效目标：回退默认会话，保留原文（new/kill 的目标是新建/删除，不要求已存在，
	// 但 send 的目标必须存在；list 不需要 target）
	switch parsed.Action {
	case ActionSend:
		if parsed.Target == "" || !contains(sessions, parsed.Target) {
			log.Printf("[intent] invalid target in response %q, fallback to %q", resp, defaultName)
			return Route{Action: ActionSend, Target: defaultName, Content: userText}
		}
	case ActionNew, ActionKill:
		if parsed.Target == "" {
			log.Printf("[intent] missing target for %s in %q, fallback to send", parsed.Action, resp)
			return Route{Action: ActionSend, Target: defaultName, Content: userText}
		}
	case ActionList:
		// 不需要 target
	default:
		log.Printf("[intent] unknown action in %q, treat as send", resp)
		parsed.Action = ActionSend
	}
	if parsed.Content == "" && parsed.Action == ActionSend {
		parsed.Content = userText
	}
	log.Printf("[intent] %q -> action=%s target=%q content=%q", userText, parsed.Action, parsed.Target, parsed.Content)
	return parsed
}

// matchExplicitPrefix 识别显式路由前缀：
//
//	main: 消息 / @main 消息 / <会话>: 消息 / @<会话> 消息
//
// 返回 (target, cleanedContent, matched)。
func matchExplicitPrefix(sessions []string, defaultName, text string) (string, string, bool) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	// 收集有效会话名（含默认会话），用于前缀匹配
	valid := append([]string{defaultName}, sessions...)
	for _, name := range valid {
		if name == "" {
			continue
		}
		for _, prefix := range []string{name + ":", "@" + name, "发给" + name, "跟" + name + "说"} {
			if strings.HasPrefix(lower, prefix) {
				rest := strings.TrimSpace(trimmed[len(prefix):])
				return name, rest, true
			}
		}
	}
	return "", "", false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// buildPrompt 构造意图路由提示词：告知会话列表 + 动作说明 + JSON 样例
func buildPrompt(sessions []string, defaultName, userText string) string {
	var b strings.Builder
	b.WriteString("你是飞书机器人\"飞梭\"的消息路由助手。系统中有以下会话：\n")
	b.WriteString("- ")
	b.WriteString(strings.Join(sessions, "\n- "))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("其中 \"%s\" 是默认会话（不需要指定会话时都用它）。\n\n", defaultName))
	b.WriteString("用户发来一条消息，可能是普通对话，也可能是会话管理指令。请解析出 action：\n")
	b.WriteString("\"action\" 取值：\n")
	b.WriteString("- \"send\"：普通对话消息（默认）；\n")
	b.WriteString("- \"list\"：用户想查看/获取会话列表（如\"有哪些会话\"\"看看我的会话\"\"列出会话\"）；\n")
	b.WriteString("- \"new\"：用户想新建会话，target 为新会话名（如\"新建一个叫work的会话\"）；\n")
	b.WriteString("- \"kill\"：用户想关闭/删除某个会话，target 为会话名（如\"把ice关了吧\"\"删除work会话\"）。\n")
	b.WriteString("role 字段：新建会话时，若用户描述了会话的角色/职责/人设（如\"翻译助手\"\"负责股票分析\"），提取为 role 字符串；无描述则为空字符串。\n")
	b.WriteString("路由规则：\n")
	b.WriteString("1. **默认目标永远是 \"main\"**——绝大多数消息就是发给 main 的普通对话（action=send）；\n")
	b.WriteString("2. 只有消息里**明确提到**某个其他会话名（如\"发给ice\"\"ice帮我看看\"\"跟ice说\"\"work: xxx\"），目标才是那个会话；\n")
	b.WriteString("3. 拿不准时一律选 main + send，不要猜测；\n")
	b.WriteString("4. content 为去掉路由意图后的纯消息内容（如\"ice帮我看看\"→\"帮我看看\"）；若无变化则保留原文。\n")
	b.WriteString("只输出 JSON，不要输出任何解释或 markdown 代码块。\n\n")
	b.WriteString("样例（会话列表: main, ice, work，默认 main）：\n")
	b.WriteString("用户: 在吗\n输出: {\"action\":\"send\",\"target\":\"main\",\"content\":\"在吗\"}\n")
	b.WriteString("用户: ice 帮我看看这个代码\n输出: {\"action\":\"send\",\"target\":\"ice\",\"content\":\"帮我看看这个代码\"}\n")
	b.WriteString("用户: 跟ice说 今天辛苦了\n输出: {\"action\":\"send\",\"target\":\"ice\",\"content\":\"今天辛苦了\"}\n")
	b.WriteString("用户: 有哪些会话啊\n输出: {\"action\":\"list\",\"target\":\"main\",\"content\":\"\"}\n")
	b.WriteString("用户: 新建一个会话叫 test\n输出: {\"action\":\"new\",\"target\":\"test\",\"content\":\"\",\"role\":\"\"}\n")
	b.WriteString("用户: 新建一个翻译助手会话叫 fanyi，负责中英翻译\n输出: {\"action\":\"new\",\"target\":\"fanyi\",\"content\":\"\",\"role\":\"负责中英翻译的翻译助手\"}\n")
	b.WriteString("用户: 把 ice 会话关掉\n输出: {\"action\":\"kill\",\"target\":\"ice\",\"content\":\"\"}\n")
	b.WriteString("用户: 你好啊\n输出: {\"action\":\"send\",\"target\":\"main\",\"content\":\"你好啊\"}\n\n")
	b.WriteString("现在解析：\n用户: ")
	b.WriteString(userText)
	b.WriteString("\n输出: ")
	return b.String()
}

// callLLM 调用 OpenAI 兼容端点
func (r *Router) callLLM(ctx context.Context, prompt string) (string, error) {
	payload := map[string]interface{}{
		"model": r.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
		"max_tokens":  100,
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST",
		r.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if resp.StatusCode != 200 || len(result.Choices) == 0 {
		return "", fmt.Errorf("llm status=%d choices=%d", resp.StatusCode, len(result.Choices))
	}
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

// parseJSON 从模型输出中提取 Route。
// 模型可能输出纯 JSON、带前后缀文本、或 code fence。action 缺省为 send。
func parseJSON(s string) Route {
	s = strings.TrimSpace(s)
	// 去掉 ```json ... ``` fence
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	// 提取第一个 { ... } 片段
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return Route{}
	}
	s = s[start : end+1]

	var v struct {
		Action  string `json:"action"`
		Target  string `json:"target"`
		Content string `json:"content"`
		Role    string `json:"role"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return Route{}
	}
	action := Action(strings.TrimSpace(strings.ToLower(v.Action)))
	switch action {
	case ActionList, ActionNew, ActionKill:
	default:
		action = ActionSend
	}
	return Route{
		Action:  action,
		Target:  strings.TrimSpace(v.Target),
		Content: strings.TrimSpace(v.Content),
		Role:    strings.TrimSpace(v.Role),
	}
}
