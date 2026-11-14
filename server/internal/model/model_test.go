package model

import (
	"strings"
	"testing"
)

func TestWindowsRetainUTF8AndIDShape(t *testing.T) {
	text := strings.Repeat("你好🙂", 1000)
	var restored strings.Builder
	for offset := 0; offset < len(text); {
		window, err := TextWindow(text, offset, 2048, 7)
		if err != nil || window.End <= offset {
			t.Fatalf("window: %+v %v", window, err)
		}
		restored.WriteString(window.Text)
		offset = window.End
	}
	if restored.String() != text {
		t.Fatal("text changed")
	}
	if _, err := TextWindow(text, 1, 2048, 7); err == nil {
		t.Fatal("mid-rune offset accepted")
	}
	if !ValidID(NewID()) || ValidID("../../state.db") {
		t.Fatal("invalid UUID handling")
	}
}
