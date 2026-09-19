// Package audio wraps raw PCM in container headers. The worker produces PCM16; the
// gateway is what a client's `format` is honoured by.
package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// WAV constants for PCM16 mono, the only shape the worker emits.
const (
	HeaderSize    = 44
	bitsPerSample = 16
	channels      = 1
	pcmFormatCode = 1
)

// UnknownSize marks a streaming header whose length is not yet known. Players accept it
// and read until the connection closes (docs/api/openapi.yaml, stream endpoint).
const UnknownSize = 0xFFFFFFFF

// WriteHeader writes a 44-byte RIFF header.
//
// dataSize is the PCM byte count, or UnknownSize while streaming: a stream cannot know
// its length before it has finished producing it.
func WriteHeader(w io.Writer, sampleRate int32, dataSize uint32) error {
	if sampleRate <= 0 {
		return fmt.Errorf("write wav header: sample rate %d is not positive", sampleRate)
	}

	byteRate := uint32(sampleRate) * channels * bitsPerSample / 8
	blockAlign := uint16(channels * bitsPerSample / 8)

	riffSize := uint32(UnknownSize)
	if dataSize != UnknownSize {
		if dataSize > math.MaxUint32-HeaderSize {
			return fmt.Errorf("write wav header: %d bytes exceeds what RIFF can describe", dataSize)
		}
		riffSize = dataSize + HeaderSize - 8
	}

	header := make([]byte, 0, HeaderSize)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, riffSize)
	header = append(header, "WAVE"...)
	header = append(header, "fmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16) // PCM fmt chunk size
	header = binary.LittleEndian.AppendUint16(header, pcmFormatCode)
	header = binary.LittleEndian.AppendUint16(header, channels)
	header = binary.LittleEndian.AppendUint32(header, uint32(sampleRate))
	header = binary.LittleEndian.AppendUint32(header, byteRate)
	header = binary.LittleEndian.AppendUint16(header, blockAlign)
	header = binary.LittleEndian.AppendUint16(header, bitsPerSample)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, dataSize)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write wav header: %w", err)
	}
	return nil
}

// DurationMS is how long `bytes` of PCM16 mono lasts at sampleRate.
//
// The result is clamped: a duration past int32 milliseconds is 24 days of audio, which
// the contract cannot produce, and silently wrapping it would report a negative length.
func DurationMS(pcmBytes int, sampleRate int32) int32 {
	if sampleRate <= 0 || pcmBytes <= 0 {
		return 0
	}

	samples := int64(pcmBytes) / (bitsPerSample / 8)
	ms := samples * 1000 / int64(sampleRate)
	if ms > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(ms) //nolint:gosec // clamped to MaxInt32 on the line above
}

// PCMSize is the byte count as a RIFF header expresses it, or an error when the audio is
// larger than the format can describe.
func PCMSize(pcmBytes int) (uint32, error) {
	if pcmBytes < 0 {
		return 0, fmt.Errorf("negative pcm size %d", pcmBytes)
	}
	if int64(pcmBytes) > math.MaxUint32-HeaderSize {
		return 0, fmt.Errorf("%d bytes exceeds what RIFF can describe", pcmBytes)
	}
	return uint32(pcmBytes), nil //nolint:gosec // bounds checked immediately above
}
