package protocol

import "testing"

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
