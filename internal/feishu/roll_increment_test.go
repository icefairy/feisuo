package feishu

import "testing"

func TestRollIncrement(t *testing.T) {
	cases := []struct {
		full   string
		sent   int
		want   string
	}{
		{"AAAA", 0, "AAAA"},         // 首次 roll：初始卡片只有"处理中"，增量=全文
		{"AAAABBBB", 4, "BBBB"},     // 第二次 roll：只发增量，不重复 AAAA
		{"AAAABBBBCCCC", 8, "CCCC"}, // 第三次 roll：增量=剩余
		{"中文测试", 2, "测试"},        // rune 边界：中文每字 1 rune，发过"中文"(2) 后增量="测试"
		{"DONE", 4, ""},             // sentLen==len：无增量
	}
	for _, c := range cases {
		got := rollIncrement(c.full, c.sent)
		if got != c.want {
			t.Fatalf("rollIncrement(%q,%d)=%q want %q", c.full, c.sent, got, c.want)
		}
	}
}
