package audio

/*
#cgo pkg-config: libavformat libavcodec libavutil libswresample
#include "pcm_native.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime/cgo"
	"syscall"
	"unsafe"

	"github.com/asticode/go-astiav"
)

const pcmChunkFrames = 16384

// pcmFormat keeps the source decoder independent of the book's fixed format.
// Future callers inside audio can supply a first-source rate and exact layout.
type pcmFormat struct {
	rate, channels int
	layout         astiav.ChannelLayout
}

func bookPCMFormat(channels int) (pcmFormat, error) {
	switch channels {
	case 1:
		return pcmFormat{44100, 1, astiav.ChannelLayoutMono}, nil
	case 2:
		return pcmFormat{44100, 2, astiav.ChannelLayoutStereo}, nil
	}
	return pcmFormat{}, fmt.Errorf("PCM channels must be mono or stereo")
}

// PCMDecoder owns one forward decoder/resampler. Close it after use. Range calls
// may move forward; reopen from source origin for backwards ranges. Buffers passed
// to consume are borrowed and valid only until consume returns.
type PCMDecoder struct {
	input         *pcmInput
	handle        cgo.Handle
	owned         io.Closer
	native        *C.BookPCM
	ctx           context.Context
	format        pcmFormat
	buffer        []float32
	begin, end    int
	position      int64
	eof           bool
	padded        bool
	inspection    bool
	boundsProblem error
	observe       func(start, end, pts, duration int64, valid bool)
}

// OpenPCM decodes the first audio stream, matching ProbeSource regardless of
// default dispositions. It never falls back to a different audio stream.
func OpenPCM(ctx context.Context, path string, channels int) (*PCMDecoder, error) {
	f, e := bookPCMFormat(channels)
	if e != nil {
		return nil, e
	}
	return openPCM(ctx, path, f)
}
func openPCM(ctx context.Context, path string, f pcmFormat) (*PCMDecoder, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	file, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	info, e := file.Stat()
	if e != nil {
		return nil, errors.Join(e, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("PCM input must be a regular file"), file.Close())
	}
	return openPCMInput(ctx, path, f, file)
}
func openPCMInput(ctx context.Context, path string, f pcmFormat, input interface {
	io.ReadSeeker
	io.Closer
}) (*PCMDecoder, error) {
	return openPCMInputMode(ctx, path, f, input, false)
}
func openPCMInputMode(ctx context.Context, path string, f pcmFormat, input interface {
	io.ReadSeeker
	io.Closer
}, inspection bool) (*PCMDecoder, error) {
	boundsErr := checkWAVBounds(ctx, input)
	if boundsErr != nil && (!inspection || !onlyMediaError(boundsErr)) {
		return nil, errors.Join(boundsErr, input.Close())
	}
	d := &PCMDecoder{ctx: ctx, format: f, buffer: make([]float32, pcmChunkFrames*f.channels), input: &pcmInput{reader: input, ctx: ctx}, owned: input}
	d.inspection = inspection
	d.boundsProblem = boundsErr
	d.handle = cgo.NewHandle(d.input)
	p, l := C.CString(path), C.CString(f.layout.String())
	defer C.free(unsafe.Pointer(p))
	defer C.free(unsafe.Pointer(l))
	if ret := C.book_pcm_open(&d.native, p, C.int(f.rate), l, C.uintptr_t(d.handle)); ret < 0 {
		e := errors.Join(astiav.Error(ret), d.input.err, d.Close())
		return nil, fmt.Errorf("opening PCM %s: %w", path, e)
	}
	if d.input.err != nil {
		e := errors.Join(d.input.err, d.Close())
		return nil, e
	}
	return d, nil
}

func (d *PCMDecoder) Close() error {
	C.book_pcm_close(&d.native)
	if d.handle != 0 {
		d.handle.Delete()
		d.handle = 0
	}
	if d.owned != nil {
		e := d.owned.Close()
		d.owned = nil
		return e
	}
	return nil
}

// Position is the next frame Range would produce.
func (d *PCMDecoder) Position() int64 { return d.position }

// SeekNear repositions the decoder shortly before frame, so a following Range
// starting at frame does not decode from the source origin. It reports false
// and leaves the decoder untouched when the container cannot seek
// sample-exactly or frame lies near the start. After an error the decoder
// must be closed.
func (d *PCMDecoder) SeekNear(frame int64) (bool, error) {
	if d.native == nil || frame < 0 {
		return false, errors.New("invalid PCM seek or closed decoder")
	}
	var position C.int64_t
	ret := C.book_pcm_seek(d.native, C.int64_t(frame), &position)
	if d.input.err != nil {
		return false, d.input.err
	}
	if ret == C.BOOK_PCM_NO_SEEK {
		return false, nil
	}
	if ret < 0 {
		return false, &pcmMediaError{fmt.Errorf("seeking PCM: %w", astiav.Error(ret))}
	}
	if int64(position) > frame {
		return false, &pcmMediaError{fmt.Errorf("PCM seek landed at frame %d, past %d", int64(position), frame)}
	}
	d.position, d.begin, d.end, d.eof, d.padded = int64(position), 0, 0, false, false
	return true, nil
}
func (d *PCMDecoder) Range(start, end int64, consume func([]float32) error) error {
	if d.native == nil || consume == nil || start < d.position || start < 0 || end < -1 || end != -1 && end <= start {
		return errors.New("invalid PCM range, consumer or closed decoder")
	}
	for end == -1 || d.position < end {
		if e := d.ctx.Err(); e != nil {
			return e
		}
		if d.begin == d.end && !d.eof {
			n := C.book_pcm_next(d.native, (*C.float)(unsafe.Pointer(&d.buffer[0])), pcmChunkFrames)
			if d.input.err != nil {
				return d.input.err
			}
			if d.observe != nil {
				var observation C.BookPCMObservation
				if C.book_pcm_observation(d.native, &observation) != 0 {
					d.observe(int64(observation.start), int64(observation.end), int64(observation.pts_us), int64(observation.duration_us), observation.valid != 0)
				}
			}
			if n < 0 {
				if errors.Is(astiav.Error(n), astiav.ErrEof) {
					d.eof = true
				} else {
					e := fmt.Errorf("decoding PCM: %w", astiav.Error(n))
					if int(n) == -int(syscall.ENOMEM) || int(n) == -int(syscall.EIO) {
						return e
					}
					return &pcmMediaError{e}
				}
			} else {
				d.begin = 0
				d.end = int(n) * d.format.channels
				for _, v := range d.buffer[:d.end] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						return errors.New("nonfinite PCM sample")
					}
				}
			}
			if n == 0 {
				continue
			}
		}
		if d.begin == d.end {
			if end != -1 && d.position < end {
				if !d.inspection && !d.padded && d.position >= start && end-d.position == 1 {
					d.padded = true
					clear(d.buffer[:d.format.channels])
					if e := consume(d.buffer[:d.format.channels]); e != nil {
						return e
					}
					d.position++
					return nil
				}
				return &pcmMediaError{fmt.Errorf("early audio EOF at frame %d, need %d", d.position, end)}
			}
			return d.boundsProblem
		}
		n := int64((d.end - d.begin) / d.format.channels)
		if d.position < start {
			n = min(n, start-d.position)
			d.begin += int(n) * d.format.channels
			d.position += n
			continue
		}
		if end != -1 {
			n = min(n, end-d.position)
		}
		b := d.buffer[d.begin : d.begin+int(n)*d.format.channels]
		if e := consume(b); e != nil {
			return e
		}
		d.begin += len(b)
		d.position += n
	}
	return d.ctx.Err()
}

// DecodePCM decodes from the first audible sample, converts to 44100 Hz, then
// slices the half-open target-frame range. endFrame=-1 reads through audible EOF.
func DecodePCM(ctx context.Context, path string, channels int, startFrame, endFrame int64, consume func([]float32) error) (err error) {
	d, e := OpenPCM(ctx, path, channels)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	return d.Range(startFrame, endFrame, consume)
}
