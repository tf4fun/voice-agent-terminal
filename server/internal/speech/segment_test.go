package speech

import (
	"reflect"
	"strings"
	"testing"
)

func TestSentencesFromMarkdownReply(t *testing.T) {
	reply := "## 天气\n周六多云，22 到 28 度。适合登山！\n\n" +
		"1. **南门**上山，经[鸣春谷](https://example.com/a)。\n" +
		"- 全程约 3.5 小时 🙂\n\n" +
		"```python\nprint('skip me')\n```\n" +
		"| 名称 | 数量 |\n| --- | ---: |\n" +
		"详情见 https://example.com/page 。OK, see you at 9:41 PM. Bye"
	got := Sentences(reply)
	want := []string{"天气", "周六多云，22 到 28 度。", "适合登山！", "南门上山，经鸣春谷。", "全程约 3.5 小时",
		"名称 ， 数量", "详情见 链接 。", "OK, see you at 9:41 PM.", "Bye"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestStreamingWaitsForCompleteSentences(t *testing.T) {
	text := "周六多云，22 到"
	if _, _, ok := Next(text, 0, false); ok {
		t.Fatal("incomplete sentence spoken")
	}
	text += " 28 度。适合"
	s, next, ok := Next(text, 0, false)
	if !ok || s != "周六多云，22 到 28 度。" {
		t.Fatalf("%q %v", s, ok)
	}
	if _, _, ok := Next(text, next, false); ok {
		t.Fatal("tail spoken before it is complete")
	}
	if s, _, ok := Next(text, next, true); !ok || s != "适合" {
		t.Fatalf("final tail %q %v", s, ok)
	}
	// An open code block holds back everything after it.
	code := "先看代码：\n```go\nfmt.Println(1)"
	s, next, _ = Next(code, 0, false)
	if s != "先看代码：" {
		t.Fatalf("%q", s)
	}
	if _, _, ok := Next(code, next, false); ok {
		t.Fatal("spoke inside an open code block")
	}
	code += "\n```\n结束了。"
	if s, _, ok := Next(code, next, false); !ok || s != "结束了。" {
		t.Fatalf("after closed block %q %v", s, ok)
	}
}

func TestLongRunsSplitAtSoftBreaks(t *testing.T) {
	long := strings.Repeat("很长的一段话没有句号，", 12)
	got := Sentences(long)
	for _, s := range got {
		if n := len([]rune(s)); n > MaxSentenceRunes {
			t.Fatalf("unit of %d runes", n)
		}
		// Units end at a comma (trimmed when spoken), never mid-phrase.
		if !strings.HasSuffix(s, "句号") {
			t.Fatalf("split mid-phrase: %q", s)
		}
	}
	strip := func(s string) string { return strings.ReplaceAll(s, "，", "") }
	if strip(strings.Join(got, "")) != strip(long) {
		t.Fatalf("lost text: %q", got)
	}
}
