package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
)

type fragmented struct {
	reader io.Reader
	size   int
}

func (f fragmented) Read(buffer []byte) (int, error) {
	return f.reader.Read(buffer[:min(len(buffer), f.size)])
}

func TestResamplingSurvivesEveryReadBoundary(t *testing.T) {
	var source []byte
	for _, sample := range []int16{0, 3000, 6000, 9000} {
		source = binary.LittleEndian.AppendUint16(source, uint16(sample))
	}
	for size := 1; size <= len(source); size++ {
		var result []int16
		err := StreamPCM(fragmented{bytes.NewReader(source), size}, 16000, func(chunk []byte) error {
			for index := 0; index < len(chunk); index += 2 {
				result = append(result, int16(binary.LittleEndian.Uint16(chunk[index:])))
			}
			return nil
		})
		if err != nil || !reflect.DeepEqual(result, []int16{0, 2000, 4000, 6000, 8000, 9000}) {
			t.Fatalf("size %d: %v %v", size, result, err)
		}
	}
}

func TestPCMRejectsOddAndEmptyAudio(t *testing.T) {
	for _, input := range [][]byte{nil, {0}} {
		if err := StreamPCM(bytes.NewReader(input), 24000, func([]byte) error { return nil }); !errors.Is(err, ErrPCM) {
			t.Fatal(err)
		}
	}
}
