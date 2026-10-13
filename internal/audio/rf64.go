package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/asticode/go-astiav"
)

type pcmOutput interface {
	io.WriteSeeker
	Sync() error
	Close() error
}

// writeRF64 owns out and closes it on every path. Per-operation I/O dependencies
// allow fault injection without process-global hooks.
func writeRF64(ctx context.Context, out pcmOutput, f pcmFormat, produce func(func([]float32) error) error) (err error) {
	defer func() { err = errors.Join(err, out.Close()) }()
	if produce == nil {
		return errors.New("nil PCM producer")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	mux, err := astiav.AllocOutputFormatContext(nil, "wav", "")
	if err != nil {
		return err
	}
	defer mux.Free()
	var ioErr error
	pb, err := astiav.AllocIOContext(32768, true, nil, func(offset int64, whence int) (int64, error) {
		if whence == 0x10000 {
			return 0, errors.New("size query unsupported")
		}
		n, e := out.Seek(offset, whence)
		if e != nil {
			ioErr = errors.Join(ioErr, e)
		}
		return n, e
	}, func(b []byte) (int, error) {
		if e := ctx.Err(); e != nil {
			ioErr = errors.Join(ioErr, e)
			return 0, e
		}
		n, e := out.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e != nil {
			ioErr = errors.Join(ioErr, e)
		}
		return n, e
	})
	if err != nil {
		return err
	}
	defer pb.Free()
	mux.SetPb(pb)
	st := mux.NewStream(nil)
	if st == nil {
		return errors.New("allocating RF64 stream")
	}
	cp := st.CodecParameters()
	cp.SetMediaType(astiav.MediaTypeAudio)
	cp.SetCodecID(astiav.CodecIDPcmF32Le)
	cp.SetSampleFormat(astiav.SampleFormatFlt)
	cp.SetSampleRate(f.rate)
	cp.SetChannelLayout(f.layout)
	st.SetTimeBase(astiav.NewRational(1, f.rate))
	opts := astiav.NewDictionary()
	defer opts.Free()
	if err = opts.Set("rf64", "always", 0); err != nil {
		return err
	}
	if err = mux.WriteHeader(opts); err != nil {
		return errors.Join(err, ioErr)
	}
	packet := astiav.AllocPacket()
	if packet == nil {
		return errors.New("allocating RF64 packet")
	}
	defer packet.Free()
	bytes := make([]byte, pcmChunkFrames*f.channels*4)
	var frames int64
	emit := func(samples []float32) error {
		if len(samples)%f.channels != 0 {
			return errors.New("partial PCM frame")
		}
		for len(samples) > 0 {
			if e := ctx.Err(); e != nil {
				return e
			}
			n := min(len(samples), len(bytes)/4)
			b := bytes[:n*4]
			for i, v := range samples[:n] {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return errors.New("nonfinite PCM sample")
				}
				binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
			}
			count := int64(n / f.channels)
			if frames > math.MaxInt64-count {
				return errors.New("PCM length overflow")
			}
			if e := packet.FromData(b); e != nil {
				return e
			}
			packet.SetStreamIndex(st.Index())
			packet.SetPts(frames)
			packet.SetDts(frames)
			packet.SetDuration(count)
			packet.SetPos(-1)
			e := mux.WriteFrame(packet)
			packet.Unref()
			if e != nil || ioErr != nil {
				return errors.Join(e, ioErr)
			}
			frames += count
			samples = samples[n:]
		}
		return nil
	}
	if err = produce(emit); err != nil {
		return errors.Join(err, ioErr)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = mux.WriteTrailer()
	pb.Flush()
	if err != nil || ioErr != nil {
		return errors.Join(err, ioErr)
	}
	if err = out.Sync(); err != nil {
		return fmt.Errorf("sync RF64: %w", err)
	}
	return ctx.Err()
}

// WriteRF64 streams interleaved float32 at 44100 Hz into an exclusive RF64 file.
// On any failure only the file created by this call is removed.
func WriteRF64(ctx context.Context, path string, channels int, produce func(func([]float32) error) error) error {
	f, e := bookPCMFormat(channels)
	if e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	out, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	created, e := out.Stat()
	if e != nil {
		return errors.Join(e, out.Close())
	}
	if e = writeRF64(ctx, out, f, produce); e != nil {
		current, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			return e
		}
		if statErr != nil {
			return errors.Join(e, statErr)
		}
		if os.SameFile(created, current) {
			return errors.Join(e, os.Remove(path))
		}
		return e
	}
	return nil
}
