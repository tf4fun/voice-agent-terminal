package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

var ErrEventTooLarge = errors.New("upstream_event_too_large")

// ReadSSE sanitizes raw media data while reading fixed-size line fragments.
// Sanitized JSON, event names and incomplete field prefixes each have a bound.
func ReadSSE(input io.Reader, emit func(string, []byte) error) error {
	reader := bufio.NewReaderSize(input, 4096)
	var data bytes.Buffer
	var filter MediaFilter
	eventName := ""
	lineKind := ""
	prefix := ""
	firstDataByte := false
	write := func(fragment []byte) error {
		filtered := filter.Write(fragment)
		if data.Len()+len(filtered) > 256<<10 {
			return ErrEventTooLarge
		}
		_, _ = data.Write(filtered)
		return nil
	}
	for {
		fragment, readErr := reader.ReadSlice('\n')
		lineEnd := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if lineEnd {
			fragment = bytes.TrimSuffix(fragment[:len(fragment)-1], []byte{'\r'})
		}
		if lineKind == "" && prefix == "" && lineEnd && len(fragment) == 0 {
			if data.Len() > 0 {
				if data.Len()+len(filter.pending) > 256<<10 {
					return ErrEventTooLarge
				}
				data.Write(filter.Flush())
				if err := emit(eventName, data.Bytes()); err != nil {
					return err
				}
			}
			data.Reset()
			filter = MediaFilter{}
			eventName = ""
		} else {
			position := 0
			for position < len(fragment) && lineKind == "" {
				ch := fragment[position]
				position++
				if lineKind == "" {
					prefix += string(ch)
					if ch == ':' {
						lineKind = strings.TrimSuffix(prefix, ":")
						if lineKind != "data" && lineKind != "event" {
							lineKind = "ignore"
						}
						if lineKind == "data" {
							if data.Len() > 0 {
								if err := write([]byte{'\n'}); err != nil {
									return err
								}
							}
							firstDataByte = true
						}
						prefix = ""
					} else if len(prefix) > 16 {
						lineKind, prefix = "ignore", ""
					}
				}
			}
			if lineKind == "data" && position < len(fragment) {
				if firstDataByte {
					firstDataByte = false
					if fragment[position] == ' ' {
						position++
					}
				}
				if err := write(fragment[position:]); err != nil {
					return err
				}
			} else if lineKind == "event" {
				if len(prefix)+len(fragment)-position > 64 {
					return ErrEventTooLarge
				}
				prefix += string(fragment[position:])
			}
			if lineEnd {
				if lineKind == "event" {
					eventName = strings.TrimSpace(prefix)
				}
				lineKind, prefix = "", ""
			}
		}
		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			if errors.Is(readErr, io.EOF) {
				return io.EOF
			}
			return readErr
		}
	}
}
