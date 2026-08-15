// Package bridge 实现飞梭会话间协作：pi 回复中输出 <bridge to="会话">消息</bridge>
// 标记，feisuo 检测后转发给目标会话，目标会话的回复流式回传给同一用户。
// 支持链式 A→B→C，Depth 字段防环（>MaxDepth 丢弃）。
package bridge

import (
	"log"
	"regexp"
	"strings"
)

// MaxDepth 最大转发深度（防环：A→B→A→B... 死循环）
const MaxDepth = 5

var (
	// bridgeRe 匹配 <bridge to="会话名">任意内容</bridge>，不贪婪
	bridgeRe = regexp.MustCompile(`<bridge\s+to="([^"]+)"\s*>(.*?)</bridge>`)
)

// Request 一条转发请求
type Request struct {
	Target  string // 目标会话名
	Content string // 要转发的消息内容（标记内部原文）
}

// Parse 从 pi 最终回复中提取所有 bridge 转发请求，同时返回剔除标记后的纯文本。
// 多个标记按出现顺序依次提取。
func Parse(final string) (clean string, reqs []Request) {
	clean = final
	for {
		loc := bridgeRe.FindStringSubmatchIndex(clean)
		if loc == nil {
			break
		}
		// loc: [fullStart fullEnd group1Start group1End group2Start group2End]
		target := clean[loc[2]:loc[3]]
		content := strings.TrimSpace(clean[loc[4]:loc[5]])
		reqs = append(reqs, Request{Target: target, Content: content})
		log.Printf("[bridge] %q -> %q (content=%d chars)", target, content, len(content))
		// 剔除标记：保留标记前+标记后的内容，避免用户看到协议噪音
		clean = clean[:loc[0]] + clean[loc[1]:]
	}
	return strings.TrimSpace(clean), reqs
}

// BridgePrompt 返回注入 pi system prompt 的桥接协议说明。
// sessions 为当前会话名列表（让模型知道可以发给谁）。
func BridgePrompt(sessions []string) string {
	var b strings.Builder
	b.WriteString("\n\n[飞梭会话协作协议]\n")
	b.WriteString("你可以请其他会话的助手帮你处理任务。方法：在回复中输出标记\n")
	b.WriteString("<bridge to=\"会话名\">要发给对方的完整消息</bridge>\n")
	b.WriteString("飞梭会把标记内部的消息转发给目标会话，对方回复会直接发给用户。\n")
	b.WriteString("可用会话：" + strings.Join(sessions, ", ") + "\n")
	b.WriteString("规则：\n")
	b.WriteString("1. to 必须是上面列出的会话名之一；\n")
	b.WriteString("2. 标记内部消息要完整自包含（对方看不到本会话对话历史）；\n")
	b.WriteString("3. 一次最多输出 2 个标记；\n")
	b.WriteString("4. 普通回复不要输出该标记（否则会被误转发）；\n")
	b.WriteString("5. 若目标会话不存在，标记会被忽略。\n")
	return b.String()
}
