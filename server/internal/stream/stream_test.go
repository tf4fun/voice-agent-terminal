package stream

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMediaAcrossEveryFragmentBoundary(t *testing.T) {
	input := "前文 ![图](data:image/png;base64," + strings.Repeat("a+/=", 1000) + ") 后文"
	for size := 1; size < 80; size++ {
		var filter MediaFilter
		var out strings.Builder
		for offset := 0; offset < len(input); offset += size {
			end := min(offset+size, len(input))
			out.Write(filter.Write([]byte(input[offset:end])))
		}
		out.Write(filter.Flush())
		if out.String() != "前文 ![图]([media omitted]) 后文" {
			t.Fatalf("size %d: %q", size, out.String())
		}
	}
}

func TestSSEStripsMultiMegabyteMediaBeforeJSONDecode(t *testing.T) {
	input := io.MultiReader(strings.NewReader(": keepalive\r\n\r\ndata: {\"text\":\"前文 data:image/png;base64,"),
		strings.NewReader(strings.Repeat("A", 8<<20)), strings.NewReader(") 后文\"}\r\n\r\ndata: [DONE]\r\n\r\n"))
	count := 0
	err := ReadSSE(input, func(event string, data []byte) error {
		count++
		if count == 1 {
			if len(data) > 128 {
				t.Fatalf("unbounded JSON: %d", len(data))
			}
			var payload map[string]string
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["text"] != "前文 [media omitted]) 后文" {
				t.Fatal(payload)
			}
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || count != 2 {
		t.Fatalf("events %d: %v", count, err)
	}
}

func TestPlainOversizedEventIsExplicitFailure(t *testing.T) {
	err := ReadSSE(strings.NewReader("data: \""+strings.Repeat("x", 300<<10)+"\"\n\n"), func(string, []byte) error { t.Fatal("oversized event emitted"); return nil })
	if !errors.Is(err, ErrEventTooLarge) {
		t.Fatal(err)
	}
}

func TestSSENamesMultilineAndIncompleteTail(t *testing.T) {
	var names []string
	var payloads []string
	err := ReadSSE(strings.NewReader("event: hermes.status\ndata: {\"ok\":\ndata: true}\n\ndata: incomplete"), func(event string, data []byte) error {
		names = append(names, event)
		payloads = append(payloads, string(data))
		return nil
	})
	if !errors.Is(err, io.EOF) || len(names) != 1 || names[0] != "hermes.status" || payloads[0] != "{\"ok\":\ntrue}" {
		t.Fatalf("%v %v %v", names, payloads, err)
	}
}
