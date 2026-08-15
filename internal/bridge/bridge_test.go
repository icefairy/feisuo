package bridge

import "testing"

func TestParseSingle(t *testing.T) {
	final := "这个问题让 ice 查一下。<bridge to=\"ice\">请查一下天气</bridge>回复结束"
	clean, reqs := Parse(final)
	want := "这个问题让 ice 查一下。回复结束"
	if clean != want {
		t.Errorf("clean = %q, want %q", clean, want)
	}
	if len(reqs) != 1 {
		t.Fatalf("reqs = %d, want 1", len(reqs))
	}
	if reqs[0].Target != "ice" || reqs[0].Content != "请查一下天气" {
		t.Errorf("req = %+v", reqs[0])
	}
}

func TestParseMultiple(t *testing.T) {
	final := `<bridge to="ice">A</bridge>中间<bridge to="work">B</bridge>`
	clean, reqs := Parse(final)
	if clean != "中间" {
		t.Errorf("clean = %q, want 中间", clean)
	}
	if len(reqs) != 2 {
		t.Fatalf("reqs = %d, want 2", len(reqs))
	}
	if reqs[0].Target != "ice" || reqs[0].Content != "A" {
		t.Errorf("req0 = %+v", reqs[0])
	}
	if reqs[1].Target != "work" || reqs[1].Content != "B" {
		t.Errorf("req1 = %+v", reqs[1])
	}
}

func TestParseNoMarker(t *testing.T) {
	final := "你好，我是正常回复"
	clean, reqs := Parse(final)
	if clean != "你好，我是正常回复" {
		t.Errorf("clean = %q", clean)
	}
	if len(reqs) != 0 {
		t.Errorf("reqs = %d, want 0", len(reqs))
	}
}

func TestParseHTMLSafe(t *testing.T) {
	// HTML 标签不误判：无 to= 属性
	final := "参考 <div>布局</div> 和 <span style=\"x\">样式</span>"
	clean, reqs := Parse(final)
	if clean != final {
		t.Errorf("clean = %q, want unchanged", clean)
	}
	if len(reqs) != 0 {
		t.Errorf("reqs = %d, want 0", len(reqs))
	}
}

func TestParseContentTrimmed(t *testing.T) {
	final := `<bridge to="ice">  带空白的消息  </bridge>`
	_, reqs := Parse(final)
	if len(reqs) != 1 || reqs[0].Content != "带空白的消息" {
		t.Errorf("req = %+v, want trimmed content", reqs)
	}
}

func TestMaxDepth(t *testing.T) {
	if MaxDepth != 5 {
		t.Errorf("MaxDepth = %d, want 5", MaxDepth)
	}
}
