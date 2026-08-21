package protocol

import "strings"

type CmdType int
type Command struct {
	Type    CmdType
	Cmd     string
	Arg     string
	Session string
	// Role 从 --role "..." 提取的系统提示词（NEW/ROLE 命令用）
	Role string
}

const (
	CmdMessage CmdType = iota
	CmdCommand
)

type Parser struct{}

func New() *Parser {
	return &Parser{}
}

func (p *Parser) Parse(text string) Command {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return Command{Type: CmdMessage, Arg: text}
	}

	text = strings.TrimPrefix(text, "/")
	text = strings.TrimSpace(text)

	parts := strings.SplitN(text, " ", 3)
	cmd := strings.ToLower(parts[0])
	arg := ""
	session := ""

	if len(parts) >= 2 {
		arg = strings.Join(parts[1:], " ")
	}

	switch cmd {
	case "to":
		// /TO <session> <message>
		sParts := SplitTwo(arg)
		session = sParts[0]
		arg = sParts[1]
		return Command{Type: CmdCommand, Cmd: cmd, Arg: arg, Session: session}

	case "new", "list", "ls", "status", "kill", "reset", "compact", "name", "rename", "help", "role":
		// 提取 --role "..."（NEW/ROLE 命令）
		role := ""
		arg = extractRole(arg, &role)
		return Command{Type: CmdCommand, Cmd: cmd, Arg: arg, Role: role}

	default:
		// 未知命令，当普通消息处理
		return Command{Type: CmdMessage, Arg: text}
	}
}

// extractRole 从参数中提取 --role "..." 或 --role "..."（支持单双引号、无引号到行尾）。
// 找到后从 arg 中移除该段，返回剩余部分。
func extractRole(arg string, role *string) string {
	lower := strings.ToLower(arg)
	idx := strings.Index(lower, "--role")
	if idx < 0 {
		return arg
	}
	rest := arg[idx+len("--role"):]
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return strings.TrimSpace(arg[:idx])
	}
	var val string
	switch rest[0] {
	case '"':
		// 双引号：到下一个未转义引号
		end := 1
		for end < len(rest) {
			if rest[end] == '"' && rest[end-1] != '\\' {
				break
			}
			end++
		}
		val = strings.ReplaceAll(rest[1:end], `\"`, `"`)
		rest = rest[end+1:]
	case '\'':
		end := 1
		for end < len(rest) {
			if rest[end] == '\'' && rest[end-1] != '\\' {
				break
			}
			end++
		}
		val = strings.ReplaceAll(rest[1:end], `\'`, `'`)
		rest = rest[end+1:]
	default:
		// 无引号：到下一个空格或行尾
		parts := strings.SplitN(rest, " ", 2)
		val = parts[0]
		if len(parts) == 2 {
			rest = parts[1]
		} else {
			rest = ""
		}
	}
	*role = strings.TrimSpace(val)
	// 移除 --role 段（连同引号），保留前后内容
	before := arg[:idx]
	after := rest
	return strings.TrimSpace(before + " " + after)
}

// SplitTwo 将字符串按第一个空格拆成两部分
func SplitTwo(s string) [2]string {
	parts := strings.SplitN(s, " ", 2)
	if len(parts) < 2 {
		return [2]string{parts[0], ""}
	}
	return [2]string{parts[0], parts[1]}
}
