// Package speech turns a complete assistant reply into speakable plain text.
// Replies arrive as Markdown; complete Markdown structures (code blocks, links,
// inline code, emphasis, headings, lists and tables) are cleaned before any
// line breaks become spoken pauses.
package speech

import (
	"regexp"
	"strings"
)

// MaxInputBytes bounds how much of a reply is cleaned. Only the first 8000
// bytes of spoken text are read aloud, so the rest is never needed.
const MaxInputBytes = 64 << 10

// maxSpan bounds the search for the end of one link or inline code span, so a
// reply full of unmatched brackets or backticks costs linear time.
const maxSpan = 1024

var (
	fenceOpen      = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	urlPattern     = regexp.MustCompile(`https?://[^\s<>"'，。！？、；：（）【】《》「」]+`)
	listMarker     = regexp.MustCompile(`^(\s*)([-*+]|\d{1,3}[.)])\s+`)
	headingMarker  = regexp.MustCompile(`^\s*#{1,6}\s*`)
	quoteMarker    = regexp.MustCompile(`^\s*>+\s?`)
	emphasis       = regexp.MustCompile(`(\*\*|__|\*|~~)`)
	tableRule      = regexp.MustCompile(`^[\s|:\-]+$`)
	spaceRun       = regexp.MustCompile(`[ \t\f\v]+`)
	onlyPunctOrSym = regexp.MustCompile(`^[\p{P}\p{S}\s]*$`)
)

func isHorizontalRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return false
	}
	var marker byte
	count := 0
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c != '-' && c != '*' && c != '_' {
			return false
		}
		if marker == 0 {
			marker = c
		} else if c != marker {
			return false
		}
		count++
	}
	return count >= 3
}

func matchClosingFence(line string, ch byte, minLen int) bool {
	trimmed := strings.TrimRight(line, " \t")
	spaces := 0
	for spaces < len(trimmed) && spaces < 4 && trimmed[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 || spaces >= len(trimmed) {
		return false
	}
	count := 0
	for i := spaces; i < len(trimmed); i++ {
		if trimmed[i] != ch {
			return false
		}
		count++
	}
	return count >= minLen
}

// extractMarkdown strips block markup and cleans inline Markdown across whole
// paragraphs/blocks before any sentence splitting runs.
func extractMarkdown(text string) []string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var out []string
	var para []string
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		cleaned := cleanInline(strings.Join(para, "\n"))
		para = para[:0]
		for _, line := range strings.Split(cleaned, "\n") {
			if unit := finalizeLine(line); unit != "" {
				out = append(out, unit)
			}
		}
	}

	inFence := false
	var fenceChar byte
	var fenceLen int

	for _, raw := range lines {
		if inFence {
			if matchClosingFence(raw, fenceChar, fenceLen) {
				inFence = false
			}
			continue
		}
		if m := fenceOpen.FindStringSubmatch(raw); m != nil {
			marker := m[1]
			info := m[2]
			// Backtick fence info strings cannot contain backticks (inline ```code```).
			if marker[0] == '`' && strings.ContainsRune(info, '`') {
				para = append(para, raw)
				continue
			}
			flushPara()
			inFence = true
			fenceChar = marker[0]
			fenceLen = len(marker)
			continue
		}

		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || tableRule.MatchString(trimmed) || isHorizontalRule(raw) {
			flushPara()
			continue
		}

		line := quoteMarker.ReplaceAllString(raw, "")
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}

		if headingMarker.MatchString(line) {
			flushPara()
			stripped := headingMarker.ReplaceAllString(line, "")
			if unit := finalizeLine(cleanInline(stripped)); unit != "" {
				out = append(out, unit)
			}
			continue
		}
		if listMarker.MatchString(line) {
			flushPara()
			stripped := listMarker.ReplaceAllString(line, "$1")
			if unit := finalizeLine(cleanInline(stripped)); unit != "" {
				out = append(out, unit)
			}
			continue
		}
		if strings.Contains(line, "|") {
			flushPara()
			if unit := finalizeLine(cleanInline(line)); unit != "" {
				out = append(out, unit)
			}
			continue
		}
		para = append(para, line)
	}
	flushPara()
	return out
}

func finalizeLine(line string) string {
	line = strings.TrimSpace(spaceRun.ReplaceAllString(line, " "))
	line = strings.Trim(line, "，, ")
	if line == "" || onlyPunctOrSym.MatchString(line) {
		return ""
	}
	return line
}

// findBacktickClose finds the run of exactly n backticks closing a code span
// opened before start, looking no further than maxSpan bytes.
func findBacktickClose(s string, start, n int) (int, int, bool) {
	s = s[:min(len(s), start+maxSpan)]
	for i := start; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return i, j, true
		}
		i = j
	}
	return 0, 0, false
}

// matchLink matches [label](target) or ![label](target) at start; a link
// longer than maxSpan bytes is left as text.
func matchLink(s string, start int) (label string, end int, ok bool) {
	s = s[:min(len(s), start+maxSpan)]
	pos := start
	if pos < len(s) && s[pos] == '!' {
		pos++
	}
	if pos >= len(s) || s[pos] != '[' {
		return "", 0, false
	}
	pos++
	labelStart := pos
	brackets := 1
	for pos < len(s) {
		if s[pos] == '\\' && pos+1 < len(s) {
			pos += 2
			continue
		}
		if s[pos] == '`' {
			n := 0
			for pos+n < len(s) && s[pos+n] == '`' {
				n++
			}
			if _, closeEnd, found := findBacktickClose(s, pos+n, n); found {
				pos = closeEnd
				continue
			}
			pos += n
			continue
		}
		switch s[pos] {
		case '[':
			brackets++
		case ']':
			brackets--
			if brackets == 0 {
				label = s[labelStart:pos]
				pos++
				goto checkTarget
			}
		}
		pos++
	}
	return "", 0, false

checkTarget:
	if pos >= len(s) || s[pos] != '(' {
		return "", 0, false
	}
	pos++
	parens := 1
	inQuote := byte(0)
	for pos < len(s) {
		if s[pos] == '\\' && pos+1 < len(s) {
			pos += 2
			continue
		}
		if inQuote != 0 {
			if s[pos] == inQuote {
				inQuote = 0
			}
			pos++
			continue
		}
		switch s[pos] {
		case '"':
			inQuote = '"'
		case '(':
			parens++
		case ')':
			parens--
			if parens == 0 {
				return label, pos + 1, true
			}
		}
		pos++
	}
	return "", 0, false
}

func cleanInline(s string) string {
	var out strings.Builder
	plainStart := 0
	flushPlain := func(end int) {
		if end > plainStart {
			out.WriteString(cleanTextRun(s[plainStart:end]))
		}
	}
	for i := 0; i < len(s); {
		switch s[i] {
		case '`':
			n := 0
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			if closeStart, closeEnd, ok := findBacktickClose(s, i+n, n); ok {
				flushPlain(i)
				inner := s[i+n : closeStart]
				if len(inner) >= 2 && inner[0] == ' ' && inner[len(inner)-1] == ' ' && strings.TrimSpace(inner) != "" {
					inner = inner[1 : len(inner)-1]
				}
				out.WriteString(cleanTextRun(inner))
				i = closeEnd
				plainStart = i
				continue
			}
			i += n
		case '[', '!':
			if s[i] == '!' && (i+1 >= len(s) || s[i+1] != '[') {
				i++
				continue
			}
			if label, end, ok := matchLink(s, i); ok {
				flushPlain(i)
				cleanedLabel := strings.ReplaceAll(cleanInline(label), "\n", " ")
				out.WriteString(cleanedLabel)
				i = end
				plainStart = i
				continue
			}
			i++
		default:
			i++
		}
	}
	flushPlain(len(s))
	return out.String()
}

func cleanTextRun(s string) string {
	text := urlPattern.ReplaceAllStringFunc(s, func(match string) string {
		trimmed := strings.TrimRight(match, ".,;:!?)]")
		return "链接" + match[len(trimmed):]
	})
	text = emphasis.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "|", "，")
	text = strings.Map(func(r rune) rune {
		if isEmoji(r) {
			return -1
		}
		return r
	}, text)
	return text
}

func isEmoji(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || (r >= 0xFE00 && r <= 0xFE0F) || r == 0x200D
}

// SpokenText converts a complete Markdown reply into clean plain text ready for
// whole-reply speech synthesis, preserving natural line breaks as pauses. Only
// the first MaxInputBytes of the reply are read.
func SpokenText(text string) string {
	if len(text) > MaxInputBytes {
		text = strings.ToValidUTF8(text[:MaxInputBytes], "")
	}
	return strings.Join(extractMarkdown(text), "\n")
}
