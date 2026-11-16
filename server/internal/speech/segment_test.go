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

func TestCodeBlocksAreSkipped(t *testing.T) {
	// An unclosed block at the end is dropped too.
	got := Sentences("先看代码：\n```go\nfmt.Println(1)\n```\n结束了。\n```sh\nls")
	if want := []string{"先看代码：", "结束了。"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
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

func TestMarkdownCleanedBeforePausesPreservesLinksAndCodeSpans(t *testing.T) {
	// Link text containing sentence-ending punctuation must stay intact instead
	// of splitting mid-link and exposing "](链接".
	input := "详情：[说明。](https://example.com/path)"
	if got := SpokenText(input); got != "详情：说明。" {
		t.Fatalf("SpokenText=%q", got)
	}
	if got := Sentences(input); !reflect.DeepEqual(got, []string{"详情：说明。"}) {
		t.Fatalf("Sentences=%q", got)
	}

	// Links and inline code longer than 80 runes must be cleaned whole.
	longLabel := strings.Repeat("长标题说明文字", 15) + "。还有补充！"
	longLink := "请查看[" + longLabel + "](https://en.wikipedia.org/wiki/Go_(programming_language))。"
	if got := SpokenText(longLink); got != "请查看"+longLabel+"。" {
		t.Fatalf("long link SpokenText=%q", got)
	}
	for _, unit := range Sentences(longLink) {
		if strings.Contains(unit, "http") || strings.Contains(unit, "](") || strings.Contains(unit, "链接") {
			t.Fatalf("leaked link markup in unit: %q", unit)
		}
	}

	longCode := strings.Repeat("identifier_segment_", 6)
	codeText := "运行 `" + longCode + "` 完成。"
	if got := SpokenText(codeText); got != "运行 "+longCode+" 完成。" {
		t.Fatalf("long inline code SpokenText=%q", got)
	}

	// Fenced code blocks with tilde fences, indented fences, or nested backticks.
	fenced := "前文。\n   ~~~markdown\n```not-closing\n[inside](https://skip.example)\n~~~\n后文。"
	if got := SpokenText(fenced); got != "前文。\n后文。" {
		t.Fatalf("fenced SpokenText=%q", got)
	}
}
