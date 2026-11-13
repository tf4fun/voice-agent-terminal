package stream

import (
	"bytes"
	"strings"
)

// MediaFilter carries only a short possible data-URI prefix between fragments.
// The same filter can sanitize JSON source before decoding or decoded reply text.
type MediaFilter struct {
	pending  string
	skipping bool
}

func base64Byte(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("+/=_-", rune(ch))
}

func (f *MediaFilter) Write(input []byte) []byte {
	var out bytes.Buffer
	for _, ch := range input {
		if f.skipping {
			if base64Byte(ch) {
				continue
			}
			f.skipping = false
		}
		if f.pending == "" {
			if ch == 'd' {
				f.pending = "d"
			} else {
				out.WriteByte(ch)
			}
			continue
		}
		if len(f.pending) < len("data:") {
			candidate := f.pending + string(ch)
			if strings.HasPrefix("data:", candidate) {
				f.pending = candidate
				continue
			}
			out.WriteString(f.pending)
			f.pending = ""
			if ch == 'd' {
				f.pending = "d"
			} else {
				out.WriteByte(ch)
			}
			continue
		}
		f.pending += string(ch)
		if ch == ',' {
			prefix := strings.ReplaceAll(f.pending, `\/`, "/")
			media := strings.HasPrefix(prefix, "data:image/") || strings.HasPrefix(prefix, "data:audio/") || strings.HasPrefix(prefix, "data:video/") || strings.HasPrefix(prefix, "data:application/")
			if media && strings.HasSuffix(prefix, ";base64,") {
				out.WriteString("[media omitted]")
				f.skipping = true
			} else {
				out.WriteString(f.pending)
			}
			f.pending = ""
			continue
		}
		if len(f.pending) > 160 || ch <= ' ' || ch == '"' || ch == ')' || ch == '(' {
			out.WriteString(f.pending)
			f.pending = ""
		}
	}
	return out.Bytes()
}

func (f *MediaFilter) Flush() []byte {
	text := []byte(f.pending)
	f.pending = ""
	f.skipping = false
	return text
}
