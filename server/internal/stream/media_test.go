package stream

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestJSONMediaEscapesAcrossFragments(t *testing.T) {
	cases := []string{
		`data:image/png;base64,AAAA\/BBBB`,
		`data:audio/ogg;base64,AAAA\u002fBBBB\u002FCCCC\u002b\u003d`,
		`\u0064ata\u003aimage\u002fpng;base64,\u0041\/BBBB`,
		`DATA:VIDEO/MP4;BASE64,AAAA\/BBBB`,
	}
	for _, media := range cases {
		input := `{"text":"前文 ` + media + `) 后文\nC:\\work\\file \"quoted\" \u6e25 \ud83d\ude00","tail":"done"}`
		expected := `{"text":"前文 [media omitted]) 后文\nC:\\work\\file \"quoted\" \u6e25 \ud83d\ude00","tail":"done"}`
		for size := 1; size <= len(input); size++ {
			filter := NewJSONMediaFilter()
			var output strings.Builder
			for offset := 0; offset < len(input); offset += size {
				output.Write(filter.Write([]byte(input[offset:min(offset+size, len(input))])))
			}
			output.Write(filter.Flush())
			if output.String() != expected || !json.Valid([]byte(output.String())) {
				t.Fatalf("media %q size %d output %q", media, size, output.String())
			}
		}
	}
}

func TestMediaPreservesNonMediaEscapes(t *testing.T) {
	inputs := []string{
		`{"text":"C:\\directory\\file \"quoted\" \/ \u002f \b\f\n\r\t \u6e25 \ud83d\ude00"}`,
		`{"text":"data:text/plain;base64,AAAA\/BBBB) data:example data tail"}`,
		`{"text":"data:image\\/png;base64,AAAA) literal escaped header"}`,
		`{"text":"data:image/png;base64,AAAA\\u002FBBBB) literal slash"}`,
	}
	expected := append([]string(nil), inputs...)
	expected[3] = `{"text":"[media omitted]\\u002FBBBB) literal slash"}`
	for index, input := range inputs {
		filter := NewJSONMediaFilter()
		var output strings.Builder
		for offset := range input {
			output.Write(filter.Write([]byte(input[offset : offset+1])))
		}
		output.Write(filter.Flush())
		if output.String() != expected[index] || !json.Valid([]byte(output.String())) {
			t.Fatalf("input %q output %q", input, output.String())
		}
	}
	// Decoded text has no JSON escape syntax. A literal backslash ends the URI.
	for _, suffix := range []string{`\u002FBBBB) after`, `\/BBBB) after`, `\nBBBB) after`} {
		var filter MediaFilter
		input := `before data:image/png;base64,AAAA` + suffix
		output := string(filter.Write([]byte(input))) + string(filter.Flush())
		if output != `before [media omitted]`+suffix {
			t.Fatalf("decoded text %q became %q", input, output)
		}
	}
}

func TestJSONMediaStateRemainsBounded(t *testing.T) {
	filter := NewJSONMediaFilter()
	var output strings.Builder
	feed := func(part string) {
		output.Write(filter.Write([]byte(part)))
		if len(filter.pending) > 6*161 || len(filter.prefix) > 161 || filter.escapeLen > 6 {
			t.Fatal("media filter state exceeds bounded prefix and escape")
		}
	}
	feed(`{"text":"`)
	for _, ch := range `data:image/png;base64,` {
		const hex = "0123456789abcdef"
		feed(`\u00` + string(hex[byte(ch)>>4]) + string(hex[byte(ch)&15]))
	}
	for count := 0; count < 1024; count++ {
		feed(strings.Repeat(`AAAA\/\u002f\u002F\u002b\u003d`, 128))
	}
	feed(`) after"}`)
	output.Write(filter.Flush())
	if output.String() != `{"text":"[media omitted]) after"}` {
		t.Fatalf("unexpected filtered length %d", output.Len())
	}
}

func TestSSEEscapedMediaDoesNotConsumeEventBudget(t *testing.T) {
	for _, slash := range []string{`\/`, `\u002f`, `\u002F`} {
		input := `data: {"text":"before data:image/png;base64,AAAA` + slash + strings.Repeat("B", 300<<10) + `) after"}` + "\n\n"
		emitted := false
		err := ReadSSE(strings.NewReader(input), func(_ string, payload []byte) error {
			emitted = true
			var body map[string]string
			if err := json.Unmarshal(payload, &body); err != nil {
				return err
			}
			var textFilter MediaFilter
			output := string(textFilter.Write([]byte(body["text"]))) + string(textFilter.Flush())
			if output != "before [media omitted]) after" {
				t.Fatalf("escaped media leaked: %q", output)
			}
			return nil
		})
		if !errors.Is(err, io.EOF) || !emitted {
			t.Fatalf("slash %q emitted %t error %v", slash, emitted, err)
		}
	}
}

func TestJSONMediaKeepsMalformedEscapesInvalid(t *testing.T) {
	for _, input := range []string{`{"text":"data:image/png;base64,AAAA\qBBBB"}`, `{"text":"data:image/png;base64,AAAA\u00xFBBBB"}`} {
		filter := NewJSONMediaFilter()
		output := append(filter.Write([]byte(input)), filter.Flush()...)
		if json.Valid(output) {
			t.Fatalf("invalid input was converted into valid JSON: %q", output)
		}
	}
}
