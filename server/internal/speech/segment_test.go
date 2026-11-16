package speech

import (
	"strings"
	"testing"
	"time"
)

func TestSpokenTextFromMarkdownReply(t *testing.T) {
	reply := "## 天气\n周六多云，22 到 28 度。适合登山！\n\n" +
		"1. **南门**上山，经[鸣春谷](https://example.com/a)。\n" +
		"- 全程约 3.5 小时 🙂\n\n" +
		"```python\nprint('skip me')\n```\n" +
		"| 名称 | 数量 |\n| --- | ---: |\n" +
		"详情见 https://example.com/page 。OK, see you at 9:41 PM. Bye"
	want := "天气\n周六多云，22 到 28 度。适合登山！\n南门上山，经鸣春谷。\n全程约 3.5 小时\n" +
		"名称 ， 数量\n详情见 链接 。OK, see you at 9:41 PM. Bye"
	if got := SpokenText(reply); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestCodeBlocksAreSkipped(t *testing.T) {
	// An unclosed block at the end is dropped too.
	got := SpokenText("先看代码：\n```go\nfmt.Println(1)\n```\n结束了。\n```sh\nls")
	if want := "先看代码：\n结束了。"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestMarkdownCleanedBeforePausesPreservesLinksAndCodeSpans(t *testing.T) {
	// Link text containing sentence-ending punctuation must stay intact instead
	// of splitting mid-link and exposing "](链接".
	input := "详情：[说明。](https://example.com/path)"
	if got := SpokenText(input); got != "详情：说明。" {
		t.Fatalf("SpokenText=%q", got)
	}

	// Links and inline code longer than 80 runes must be cleaned whole.
	longLabel := strings.Repeat("长标题说明文字", 15) + "。还有补充！"
	longLink := "请查看[" + longLabel + "](https://en.wikipedia.org/wiki/Go_(programming_language))。"
	if got := SpokenText(longLink); got != "请查看"+longLabel+"。" {
		t.Fatalf("long link SpokenText=%q", got)
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

func TestCleaningCostStaysLinear(t *testing.T) {
	// Unmatched brackets and backticks once rescanned the rest of the reply
	// for each opener; a 256 KiB reply of them took about 19 s.
	for _, reply := range []string{
		strings.Repeat("[", 2<<20),
		strings.Repeat("![a", 1<<20),
		strings.Repeat("[a](", 1<<20),
		strings.Repeat("`a``", 1<<20),
	} {
		started := time.Now()
		SpokenText(reply)
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("cleaning %q… took %v", reply[:8], elapsed)
		}
	}
}

func TestOnlyTheStartOfAReplyIsCleaned(t *testing.T) {
	reply := strings.Repeat("汉", MaxInputBytes/3) + "尾巴"
	got := SpokenText(reply)
	if strings.Contains(got, "尾巴") || len(got) > MaxInputBytes {
		t.Fatalf("read past the input bound: %d bytes", len(got))
	}
	// A link longer than the span bound stays text instead of being searched for.
	long := "[" + strings.Repeat("字", maxSpan) + "](https://example.com)"
	if got := SpokenText(long); !strings.Contains(got, "](链接)") {
		t.Fatalf("long link was matched: %q", got[len(got)-40:])
	}
}
