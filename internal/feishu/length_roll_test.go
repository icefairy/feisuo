package feishu

import (
	"strings"
	"testing"
)

// TestShouldRollByLength 验证超长拆条的触发条件：
// full 总长 ≥ maxMsgRunes 且增量（fullLen - sentLen）≥ maxMsgRunes 时拆条。
func TestShouldRollByLength(t *testing.T) {
	aa := strings.Repeat("a", maxMsgRunes) // 恰好一卡
	rest := "中"                            // 1 rune
	cases := []struct {
		name   string
		full   string
		sent   int
		want   bool
	}{
		{"full 未到上限不拆", strings.Repeat("a", maxMsgRunes-1), 0, false},
		{"full 刚到上限但无增量不拆", aa, maxMsgRunes, false}, // 刚 roll 完 sentLen==fullLen，避免连续空卡
		{"full 刚过上限、增量小不拆", aa + rest, maxMsgRunes, false}, // 增量仅 1 字，不够一卡
		{"full 到上限且增量满一卡要拆", strings.Repeat("a", maxMsgRunes*2), maxMsgRunes, true},
		{"中文 rune 计数（2000 中文字=2000 rune，全增量）", strings.Repeat("中", maxMsgRunes), 0, true},
		{"中文 rune 计数：增量仅半卡不拆", strings.Repeat("中", maxMsgRunes), maxMsgRunes / 2, false},
	}
	for _, c := range cases {
		if got := shouldRollByLength(c.full, c.sent); got != c.want {
			t.Fatalf("[%s] shouldRollByLength(fullLen=%d,sent=%d)=%v want %v",
				c.name, len([]rune(c.full)), c.sent, got, c.want)
		}
	}
}

// TestRollByLengthRange 用 5000 字流式喂入模拟每次流式触发长度拆条，
// 校验累计 roll 次数（≥2）且每个 roll 的增量不包含上一张已展示内容。
// 通过按 chunk 追加到 st.full、再以 shouldRollByLength+rollIncrement 复现
// StreamChunk 的长度触发语义。
func TestRollByLengthRange(t *testing.T) {
	// 一段超长文本（5000 rune），流式分多 chunk 喂入
	long := strings.Repeat("数据", 2500) // 5000 rune
	chunks := 50
	perChunk := len([]rune(long)) / chunks

	st := &streamState{full: "", sentLen: 0}
	rounds := 0
	seenStart := 0 // 已展示内容的 rune 边界（叠加每次 roll 的增量）

	for i := 0; i < chunks; i++ {
		start := i * perChunk
		end := len([]rune(long))
		if (i+1)*perChunk < end {
			end = (i + 1) * perChunk
		}
		chunk := string([]rune(long)[start:end])
		st.full += chunk

		if shouldRollByLength(st.full, st.sentLen) {
			inc := rollIncrement(st.full, st.sentLen)
			// 增量必须紧接已展示内容之后（不重复、不跳过）
			want := string([]rune(long)[seenStart : seenStart+len([]rune(inc))])
			if inc != want {
				t.Fatalf("round %d: 增量不含/错位。got 前缀=%q want=%q",
					rounds, prefix(inc, 20), prefix(want, 20))
			}
			seenStart += len([]rune(inc))
			st.sentLen = len([]rune(st.full))
			rounds++
		}
	}
	// 触发至少 2 次 roll
	if rounds < 2 {
		t.Fatalf("期望 ≥2 次 roll，实际 %d 次", rounds)
	}
	// 已 roll 的新卡增量已覆盖 seenStart 之前的内容且互不重复（contiguity 已保证无重复）。
	// 剩余尾部（不足一卡）未 roll，属正常：由 StreamEnd/flushStream 编辑到当前消息兜底。
}

func prefix(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}