package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// checkWAVBounds validates declared RIFF/RF64 byte extents against the input.
// It never infers sample counts from duration, bitrate, or timestamps. A zero
// or sentinel streaming length remains unknown; non-WAV input is untouched.
// Only fixed-size headers are read; audio payloads and metadata are skipped.
func checkWAVBounds(ctx context.Context, input io.ReadSeeker) (err error) {
	original, err := input.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	defer func() { _, restoreErr := input.Seek(original, io.SeekStart); err = errors.Join(err, restoreErr) }()
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var header [12]byte
	if _, err = io.ReadFull(input, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		return err
	}
	kind := string(header[:4])
	if (kind != "RIFF" && kind != "RF64") || string(header[8:]) != "WAVE" {
		return nil
	}
	size, err := input.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if size < 12 {
		return &pcmMediaError{fmt.Errorf("truncated WAV header")}
	}
	riffSize := uint64(binary.LittleEndian.Uint32(header[4:8]))
	if riffSize != 0 && riffSize != math.MaxUint32 && riffSize > uint64(size-8) {
		return &pcmMediaError{fmt.Errorf("truncated WAV: RIFF declares %d bytes, input has %d", riffSize+8, size)}
	}
	// Bytes outside a declared RIFF form can be unrelated trailing metadata.
	// They are not chunk headers and must not create guessed audio bounds.
	limit := size
	if riffSize != 0 && riffSize != math.MaxUint32 {
		limit = int64(riffSize) + 8
	}
	var rf64Data uint64
	var haveDS64 bool
	for offset := int64(12); offset < limit; {
		if limit-offset < 8 {
			return &pcmMediaError{fmt.Errorf("truncated WAV chunk header")}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = input.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		var chunk [8]byte
		if _, err = io.ReadFull(input, chunk[:]); err != nil {
			return fmt.Errorf("truncated WAV chunk header: %w", err)
		}
		offset += 8
		n := uint64(binary.LittleEndian.Uint32(chunk[4:]))
		id := string(chunk[:4])
		if id == "data" && (n == 0 || n == math.MaxUint32) {
			if kind == "RF64" && haveDS64 && rf64Data != 0 {
				n = rf64Data
			} else {
				return nil
			}
		}
		if n > uint64(limit-offset) {
			return &pcmMediaError{fmt.Errorf("truncated WAV %s: declares %d bytes, only %d remain", id, n, limit-offset)}
		}
		if kind == "RF64" && id == "ds64" {
			if n < 28 {
				return &pcmMediaError{fmt.Errorf("invalid RF64 ds64 length")}
			}
			var ds64 [28]byte
			if _, err = io.ReadFull(input, ds64[:]); err != nil {
				return err
			}
			declaredRIFF := binary.LittleEndian.Uint64(ds64[:8])
			if declaredRIFF != 0 && declaredRIFF > uint64(size-8) {
				return &pcmMediaError{fmt.Errorf("truncated RF64: ds64 declares %d bytes, input has %d", declaredRIFF, size-8)}
			}
			if declaredRIFF != 0 {
				limit = int64(declaredRIFF) + 8
				if limit < offset+int64(n) {
					return &pcmMediaError{fmt.Errorf("invalid RF64 form size")}
				}
			}
			rf64Data = binary.LittleEndian.Uint64(ds64[8:16])
			haveDS64 = true
		}
		// The comparison above ensures conversion/addition cannot overflow int64.
		offset += int64(n)
		if n&1 != 0 {
			if offset == limit {
				return &pcmMediaError{fmt.Errorf("truncated WAV chunk padding")}
			}
			offset++
		}
	}
	return nil
}
