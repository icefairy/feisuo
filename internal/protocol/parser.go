package protocol

import "strings"

type CmdType int
type Command struct {
	Type    CmdType
	Cmd     string
	Arg     string
	Session string
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

	case "new", "list", "ls", "status", "kill", "name", "rename", "help":
		return Command{Type: CmdCommand, Cmd: cmd, Arg: arg}

	default:
		// 未知命令，当普通消息处理
		return Command{Type: CmdMessage, Arg: text}
	}
}

// SplitTwo 将字符串按第一个空格拆成两部分
func SplitTwo(s string) [2]string {
	parts := strings.SplitN(s, " ", 2)
	if len(parts) < 2 {
		return [2]string{parts[0], ""}
	}
	return [2]string{parts[0], parts[1]}
}