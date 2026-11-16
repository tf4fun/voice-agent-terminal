// Package speech turns a complete assistant reply into sentences to speak.
// Replies arrive as Markdown; the spoken form drops markup, code blocks and
// link targets, and long runs without punctuation are split at a soft break.
package speech

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSentenceRunes bounds one spoken unit; a longer run without sentence
// punctuation is split at its last soft break (a comma or colon).
const MaxSentenceRunes = 80

const fence = "```"

// nextRaw returns the raw unit starting at or after byte offset and the offset
// after it; a code block is returned as an empty unit. ok is false only when
// nothing follows.
func nextRaw(text string, offset int) (string, int, bool) {
	rest := text[offset:]
	trimmed := strings.TrimLeft(rest, " \t\r\n")
	start := offset + len(rest) - len(trimmed)
	if start >= len(text) {
		return "", len(text), true
	}
	// A code block is skipped whole; an unclosed one runs to the end.
	if strings.HasPrefix(trimmed, fence) {
		closing := strings.Index(trimmed[len(fence):], fence)
		if closing < 0 {
			return "", len(text), true
		}
		end := start + len(fence) + closing + len(fence)
		if newline := strings.IndexByte(text[end:], '\n'); newline >= 0 {
			end += newline + 1
		} else {
			end = len(text)
		}
		return "", end, true
	}
	limit := len(text)
	if block := strings.Index(text[start:], fence); block > 0 {
		limit = start + block // Never read into a following code block.
	}
	runes := 0
	soft := -1
	scan := start
	// A list marker such as "1. " is not a sentence end.
	if marker := listMarker.FindStringIndex(text[start:limit]); marker != nil {
		scan = start + marker[1]
	}
	for i := scan; i < limit; {
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		runes++
		if hardStop(r, text, i, limit) {
			return text[start:i], extendClosers(text, i, limit), true
		}
		if softStop(r) {
			soft = i
		}
		if runes >= MaxSentenceRunes {
			if soft > start {
				return text[start:soft], soft, true
			}
			return text[start:i], i, true
		}
	}
	if limit > start {
		return text[start:limit], limit, true
	}
	return "", offset, false
}

func hardStop(r rune, text string, after, limit int) bool {
	switch r {
	case '。', '！', '？', '；', '…', '\n':
		return true
	case '!', '?', ';':
		return after >= limit || isSpace(text, after)
	case '.':
		// A period ends a sentence only before whitespace: 3.14 and example.com do not.
		return after < limit && isSpace(text, after)
	}
	return false
}

func softStop(r rune) bool {
	switch r {
	case '，', '、', '：', ',', ':', '）', ')':
		return true
	}
	return false
}

func isSpace(text string, at int) bool {
	r, _ := utf8.DecodeRuneInString(text[at:])
	return unicode.IsSpace(r)
}

// extendClosers keeps closing quotes and brackets with their sentence.
func extendClosers(text string, at, limit int) int {
	for at < limit {
		r, size := utf8.DecodeRuneInString(text[at:])
		if !strings.ContainsRune("”’」』）)\"'】》", r) {
			break
		}
		at += size
	}
	return at
}

var (
	linkPattern    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	urlPattern     = regexp.MustCompile(`https?://\S+`)
	inlineCode     = regexp.MustCompile("`([^`]*)`")
	listMarker     = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s+`)
	headingMarker  = regexp.MustCompile(`^\s*#{1,6}\s*`)
	quoteMarker    = regexp.MustCompile(`^\s*>+\s?`)
	emphasis       = regexp.MustCompile(`(\*\*|__|\*|~~)`)
	tableRule      = regexp.MustCompile(`^[\s|:\-]+$`)
	spaceRun       = regexp.MustCompile(`\s+`)
	onlyPunctOrSym = regexp.MustCompile(`^[\p{P}\p{S}\s]*$`)
)

// Clean converts one Markdown sentence to plain speakable text.
func Clean(raw string) string {
	text := strings.TrimSpace(raw)
	if tableRule.MatchString(text) {
		return ""
	}
	text = headingMarker.ReplaceAllString(text, "")
	text = quoteMarker.ReplaceAllString(text, "")
	text = listMarker.ReplaceAllString(text, "$1")
	text = linkPattern.ReplaceAllString(text, "$1")
	text = urlPattern.ReplaceAllString(text, "链接")
	text = inlineCode.ReplaceAllString(text, "$1")
	text = emphasis.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "|", "，")
	text = strings.Map(func(r rune) rune {
		if isEmoji(r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(spaceRun.ReplaceAllString(text, " "))
	text = strings.Trim(text, "，, ")
	if onlyPunctOrSym.MatchString(text) {
		return ""
	}
	return text
}

func isEmoji(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || (r >= 0xFE00 && r <= 0xFE0F) || r == 0x200D
}

// Sentences splits a complete reply into speakable units. Units that clean up
// to nothing (pure markup) are skipped.
func Sentences(text string) []string {
	var out []string
	for offset := 0; offset < len(text); {
		raw, next, ok := nextRaw(text, offset)
		if !ok || next <= offset {
			break
		}
		offset = next
		if spoken := Clean(raw); spoken != "" {
			out = append(out, spoken)
		}
	}
	return out
}
