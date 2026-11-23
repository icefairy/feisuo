package protocol

import (
	"strings"
	"testing"
)

func TestParseRoleNew(t *testing.T) {
	c := New().Parse(`/NEW fanyi --role "你是资深中英翻译，风格简洁"`)
	if c.Cmd != "new" || c.Arg != "fanyi" || c.Role != "你是资深中英翻译，风格简洁" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseNewNoRole(t *testing.T) {
	c := New().Parse(`/NEW ops`)
	if c.Cmd != "new" || c.Arg != "ops" || c.Role != "" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseRoleUpdate(t *testing.T) {
	c := New().Parse(`/ROLE fanyi --role "改成日语翻译"`)
	if c.Cmd != "role" || c.Arg != "fanyi" || c.Role != "改成日语翻译" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseRoleView(t *testing.T) {
	c := New().Parse(`/ROLE fanyi`)
	if c.Cmd != "role" || c.Arg != "fanyi" || c.Role != "" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseRoleSingleQuote(t *testing.T) {
	c := New().Parse(`/NEW t --role '单引号角色'`)
	if c.Cmd != "new" || c.Arg != "t" || c.Role != "单引号角色" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseRoleNoQuote(t *testing.T) {
	c := New().Parse(`/NEW t --role 简单角色`)
	if c.Cmd != "new" || c.Arg != "t" || c.Role != "简单角色" {
		t.Errorf("got cmd=%s arg=%q role=%q", c.Cmd, c.Arg, c.Role)
	}
}

func TestParseNewDir(t *testing.T) {
	c := New().Parse(`/NEW repo --dir /data/codes/feisuo`)
	if c.Cmd != "new" || c.Arg != "repo" || c.WorkDir != "/data/codes/feisuo" {
		t.Errorf("got cmd=%s arg=%q dir=%q", c.Cmd, c.Arg, c.WorkDir)
	}
}

func TestParseNewRoleAndDir(t *testing.T) {
	c := New().Parse(`/NEW repo --role "代码审查" --dir "/data/codes/my repo"`)
	if c.Cmd != "new" || c.Arg != "repo" || c.Role != "代码审查" || c.WorkDir != "/data/codes/my repo" {
		t.Errorf("got cmd=%s arg=%q role=%q dir=%q", c.Cmd, c.Arg, c.Role, c.WorkDir)
	}
}

func TestParseCD(t *testing.T) {
	c := New().Parse(`/CD repo /data/codes/feisuo`)
	if c.Cmd != "cd" || c.Session != "repo" || c.Arg != "/data/codes/feisuo" {
		t.Errorf("got cmd=%s session=%q arg=%q", c.Cmd, c.Session, c.Arg)
	}
}

func TestParseCDViewOnly(t *testing.T) {
	c := New().Parse(`/CD repo`)
	if c.Cmd != "cd" || c.Session != "repo" || c.Arg != "" {
		t.Errorf("got cmd=%s session=%q arg=%q", c.Cmd, c.Session, c.Arg)
	}
}

func TestParseToUnaffected(t *testing.T) {
	c := New().Parse(`/TO ice 帮我看看 --role 不是角色`)
	if c.Cmd != "to" || c.Session != "ice" || c.Role != "" {
		t.Errorf("got cmd=%s session=%q role=%q", c.Cmd, c.Session, c.Role)
	}
}

// TestParseToSwitchOnly：/TO 不带消息 = 纯切换（session 有值，arg 空）
func TestParseToSwitchOnly(t *testing.T) {
	c := New().Parse(`/TO work`)
	if c.Cmd != "to" || c.Session != "work" || c.Arg != "" {
		t.Errorf("got cmd=%s session=%q arg=%q", c.Cmd, c.Session, c.Arg)
	}
}

func TestParseModel(t *testing.T) {
	cases := []struct {
		in, arg string
	}{
		{`/MODEL hy4flash`, "hy4flash"},
		{`/MODEL`, ""},
		{`/MODEL reset`, "reset"},
		{`/MODEL   deepseek-v4.1-flash  `, "deepseek-v4.1-flash"},
	}
	for _, tc := range cases {
		c := New().Parse(tc.in)
		// parser 只做整体 trim，多空格会留下前导空格；命令处理处再 TrimSpace
		if c.Cmd != "model" || strings.TrimSpace(c.Arg) != tc.arg {
			t.Errorf("Parse(%q): got cmd=%q arg=%q, want cmd=model arg=%q", tc.in, c.Cmd, c.Arg, tc.arg)
		}
	}
}
