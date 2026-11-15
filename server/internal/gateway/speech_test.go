package gateway

import (
	"bytes"
	"context"
	"testing"
)

func readAll(t *testing.T, reader *SpeechReader) []byte {
	t.Helper()
	var out []byte
	for {
		chunk, ok, err := reader.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, chunk...)
	}
}

func TestSpeechReaderResumesAtAnyByte(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	stream.append(frame(FrameSentence, []byte{0, 0, 'h', 'i'}))
	stream.append(frame(FrameAudio, []byte{1, 2, 3, 4, 5, 6}))
	stream.append(frame(FrameAudio, []byte{7, 8}))
	stream.end(SpeechDone)
	whole := readAll(t, &SpeechReader{stream: stream, closer: func() {}})
	// Every offset, including mid-header and mid-sample, continues exactly.
	for offset := 0; offset <= len(whole); offset++ {
		got := readAll(t, &SpeechReader{stream: stream, skip: offset, closer: func() {}})
		if !bytes.Equal(got, whole[offset:]) {
			t.Fatalf("offset %d: got %v want %v", offset, got, whole[offset:])
		}
	}
}

func TestReleaseSplitsHeldFrames(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	held := append(frame(FrameAudio, []byte{1, 2}), frame(FrameAudio, []byte{3, 4, 5, 6})...)
	holding := true
	if !release(stream, &held, &holding) || holding || len(stream.frames) != 2 || len(stream.frames[1]) != 7 {
		t.Fatalf("frames %v", stream.frames)
	}
}
