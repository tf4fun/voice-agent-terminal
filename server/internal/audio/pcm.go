package audio

import (
	"encoding/binary"
	"errors"
	"io"
)

var ErrPCM = errors.New("invalid_pcm_audio")

// StreamPCM emits bounded 24 kHz mono PCM16 chunks. The 16 kHz path linearly
// interpolates with a rational phase carried across input reads.
func StreamPCM(input io.Reader, rate int, emit func([]byte) error) error {
	if rate != 16000 && rate != 24000 {
		return ErrPCM
	}
	buffer := make([]byte, 3200)
	output := make([]byte, 0, 9600)
	var pending byte
	hasPending := false
	var previous int16
	index, nextPosition := int64(0), int64(0)
	appendSample := func(sample int16) error {
		output = binary.LittleEndian.AppendUint16(output, uint16(sample))
		if len(output) >= 9600 {
			if err := emit(output); err != nil {
				return err
			}
			output = output[:0]
		}
		return nil
	}
	consume := func(sample int16) error {
		if rate == 24000 {
			return appendSample(sample)
		}
		if index > 0 {
			for nextPosition < index*3 {
				fraction := nextPosition - (index-1)*3
				value := int64(previous) + (int64(sample)-int64(previous))*fraction/3
				if err := appendSample(int16(value)); err != nil {
					return err
				}
				nextPosition += 2
			}
		}
		previous = sample
		index++
		return nil
	}
	count := 0
	for {
		n, err := input.Read(buffer)
		position := 0
		if hasPending && n > 0 {
			if err := consume(int16(uint16(pending) | uint16(buffer[0])<<8)); err != nil {
				return err
			}
			count++
			position, hasPending = 1, false
		}
		for position+1 < n {
			if err := consume(int16(binary.LittleEndian.Uint16(buffer[position : position+2]))); err != nil {
				return err
			}
			count++
			position += 2
		}
		if position < n {
			pending, hasPending = buffer[position], true
		}
		if len(output) > 0 {
			if err := emit(output); err != nil {
				return err
			}
			output = output[:0]
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
	}
	if hasPending || count == 0 {
		return ErrPCM
	}
	if rate == 16000 {
		if err := consume(previous); err != nil {
			return err
		}
	}
	if len(output) > 0 {
		return emit(output)
	}
	return nil
}
