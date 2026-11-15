package gateway

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestSpeechReaderResumesFromSentence(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	for index := 0; index < 3; index++ {
		stream.append(frame(FrameSentence, append(binary.LittleEndian.AppendUint16(nil, uint16(index)), "句"...)))
		stream.append(frame(FrameAudio, []byte{byte(index), 0}))
	}
	stream.end(SpeechDone)
	reader := &SpeechReader{stream: stream, from: 1, skipping: true, closer: func() {}}
	var kinds []byte
	var first uint16
	for {
		f, ok, err := reader.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if len(kinds) == 0 {
			first = binary.LittleEndian.Uint16(f[3:])
		}
		kinds = append(kinds, f[0])
	}
	// Sentences 1 and 2 with their audio, then the end frame.
	if len(kinds) != 5 || kinds[0] != FrameSentence || first != 1 || kinds[4] != FrameEnd {
		t.Fatalf("frames %v first %d", kinds, first)
	}
	// Resuming past the last sentence still ends the stream.
	past := &SpeechReader{stream: stream, from: 9, skipping: true, closer: func() {}}
	f, ok, _ := past.Next(context.Background())
	if !ok || f[0] != FrameEnd {
		t.Fatal("resume past the end did not end")
	}
}
