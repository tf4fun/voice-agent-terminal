package stream

import (
	"bytes"
	"strings"
)

// MediaFilter retains a bounded URI candidate between fragments. Its zero value
// filters decoded text. JSON mode preserves the original source spelling of all
// non-media text and carries at most one six-byte escape across fragments.
type MediaFilter struct {
	pending   string
	prefix    string
	skipping  bool
	jsonMode  bool
	inString  bool
	escape    [6]byte
	escapeLen int
}

func NewJSONMediaFilter() MediaFilter {
	return MediaFilter{jsonMode: true}
}

func base64Byte(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("+/=_-", rune(ch))
}

func jsonEscape(value []byte) (rune, bool) {
	switch value[1] {
	case '"', '\\', '/':
		return rune(value[1]), true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case 'u':
		var code rune
		for _, ch := range value[2:] {
			code *= 16
			switch {
			case ch >= '0' && ch <= '9':
				code += rune(ch - '0')
			case ch >= 'a' && ch <= 'f':
				code += rune(ch - 'a' + 10)
			case ch >= 'A' && ch <= 'F':
				code += rune(ch - 'A' + 10)
			default:
				return 0, false
			}
		}
		return code, true
	default:
		return 0, false
	}
}

func (f *MediaFilter) flushPrefix(out *bytes.Buffer) {
	out.WriteString(f.pending)
	f.pending, f.prefix = "", ""
}

func (f *MediaFilter) token(ch rune, source []byte, out *bytes.Buffer) {
	if f.skipping {
		if ch >= 0 && ch <= 127 && base64Byte(byte(ch)) {
			return
		}
		f.skipping = false
	}
	if ch < 0 || ch > 127 {
		f.flushPrefix(out)
		out.Write(source)
		return
	}
	normalized := byte(ch)
	if normalized >= 'A' && normalized <= 'Z' {
		normalized += 'a' - 'A'
	}
	if f.prefix != "" && len(f.prefix) < len("data:") {
		candidate := f.prefix + string(normalized)
		if strings.HasPrefix("data:", candidate) {
			f.prefix = candidate
			f.pending += string(source)
			return
		}
		f.flushPrefix(out)
	}
	if f.prefix == "" {
		if normalized == 'd' {
			f.prefix = "d"
			f.pending = string(source)
		} else {
			out.Write(source)
		}
		return
	}
	f.prefix += string(normalized)
	f.pending += string(source)
	if ch == ',' {
		media := strings.HasPrefix(f.prefix, "data:image/") || strings.HasPrefix(f.prefix, "data:audio/") || strings.HasPrefix(f.prefix, "data:video/") || strings.HasPrefix(f.prefix, "data:application/")
		if media && strings.HasSuffix(f.prefix, ";base64,") {
			out.WriteString("[media omitted]")
			f.skipping = true
			f.pending, f.prefix = "", ""
		} else {
			f.flushPrefix(out)
		}
		return
	}
	if len(f.prefix) > 160 || ch <= ' ' || ch == '"' || ch == ')' || ch == '(' {
		f.flushPrefix(out)
	}
}

func (f *MediaFilter) Write(input []byte) []byte {
	var out bytes.Buffer
	for index, ch := range input {
		source := input[index : index+1]
		if !f.jsonMode {
			f.token(rune(ch), source, &out)
			continue
		}
		if f.escapeLen > 0 {
			f.escape[f.escapeLen] = ch
			f.escapeLen++
			needed := 2
			if f.escapeLen >= 2 && f.escape[1] == 'u' {
				needed = 6
			}
			if f.escapeLen < needed {
				continue
			}
			value, valid := jsonEscape(f.escape[:f.escapeLen])
			if !valid {
				value = -1
			}
			f.token(value, f.escape[:f.escapeLen], &out)
			f.escapeLen = 0
			continue
		}
		if !f.inString {
			out.WriteByte(ch)
			if ch == '"' {
				f.inString = true
			}
			continue
		}
		if ch == '\\' {
			f.escape[0] = ch
			f.escapeLen = 1
			continue
		}
		if ch == '"' {
			f.flushPrefix(&out)
			f.skipping = false
			f.inString = false
			out.WriteByte(ch)
			continue
		}
		f.token(rune(ch), source, &out)
	}
	return out.Bytes()
}

func (f *MediaFilter) Flush() []byte {
	text := append([]byte(f.pending), f.escape[:f.escapeLen]...)
	f.pending, f.prefix = "", ""
	f.skipping, f.inString = false, false
	f.escapeLen = 0
	return text
}
